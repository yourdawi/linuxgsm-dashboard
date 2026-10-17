package backend

import (
	"bufio"
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// DockerExecConfig describes a command run inside a container.
type DockerExecConfig struct {
	Cmd        []string
	User       string
	WorkingDir string
	Env        []string
}

// Exec runs a command inside a running container and returns its exit code. stdout and stderr
// are demultiplexed into stdio; stdin is sent and closed when it is set. When ctx ends, the
// stream is closed and ctx.Err() is returned. The command itself keeps running in the container
// until it finishes, because the API cannot kill an exec process.
func (c *DockerClient) Exec(ctx context.Context, container string, cfg DockerExecConfig, stdio ExecIO) (int, error) {
	create := map[string]interface{}{
		"AttachStdin":  stdio.Stdin != nil,
		"AttachStdout": true,
		"AttachStderr": true,
		"Tty":          false,
		"Cmd":          cfg.Cmd,
		"User":         cfg.User,
		"WorkingDir":   cfg.WorkingDir,
		"Env":          cfg.Env,
	}
	var created struct {
		ID string `json:"Id"`
	}
	if err := c.do(ctx, http.MethodPost, "/containers/"+url.PathEscape(container)+"/exec", nil, create, &created); err != nil {
		return -1, &StartError{Err: err}
	}
	if created.ID == "" {
		return -1, &StartError{Err: fmt.Errorf("docker API returned no exec id")}
	}

	conn, br, err := c.startExec(ctx, created.ID)
	if err != nil {
		return -1, &StartError{Err: err}
	}
	defer conn.Close()

	done := make(chan struct{})
	defer close(done)
	go func() {
		select {
		case <-ctx.Done():
			conn.Close()
		case <-done:
		}
	}()

	if stdio.Stdin != nil {
		go func() {
			_, _ = io.Copy(conn, stdio.Stdin)
			if cw, ok := conn.(interface{ CloseWrite() error }); ok {
				_ = cw.CloseWrite()
			}
		}()
	}

	streamErr := demuxDockerStream(br, stdio.Stdout, stdio.Stderr)
	if ctx.Err() != nil {
		return -1, ctx.Err()
	}
	if streamErr != nil {
		return -1, streamErr
	}
	return c.execExitCode(created.ID)
}

// startExec starts an exec instance and takes over the connection for the output stream
// (POST /exec/{id}/start answers with "101 Switching Protocols").
func (c *DockerClient) startExec(ctx context.Context, id string) (net.Conn, *bufio.Reader, error) {
	conn, err := c.dial(ctx)
	if err != nil {
		return nil, nil, err
	}
	req, err := http.NewRequest(http.MethodPost, c.baseURL+c.prefix+"/exec/"+url.PathEscape(id)+"/start",
		strings.NewReader(`{"Detach":false,"Tty":false}`))
	if err != nil {
		conn.Close()
		return nil, nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Connection", "Upgrade")
	req.Header.Set("Upgrade", "tcp")

	if deadline, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(deadline)
	} else {
		_ = conn.SetDeadline(time.Now().Add(30 * time.Second))
	}
	if err := req.Write(conn); err != nil {
		conn.Close()
		return nil, nil, err
	}
	br := bufio.NewReader(conn)
	resp, err := http.ReadResponse(br, req)
	if err != nil {
		conn.Close()
		return nil, nil, err
	}
	if resp.StatusCode != http.StatusSwitchingProtocols && resp.StatusCode != http.StatusOK {
		data, _ := io.ReadAll(io.LimitReader(resp.Body, 64*1024))
		resp.Body.Close()
		conn.Close()
		var apiErr struct {
			Message string `json:"message"`
		}
		if json.Unmarshal(data, &apiErr) == nil && apiErr.Message != "" {
			return nil, nil, fmt.Errorf("docker exec start: %s", apiErr.Message)
		}
		return nil, nil, fmt.Errorf("docker exec start: %s", resp.Status)
	}
	// The stream lasts as long as the command; only a context deadline ends it early.
	if _, ok := ctx.Deadline(); !ok {
		_ = conn.SetDeadline(time.Time{})
	}
	return conn, br, nil
}

// demuxDockerStream splits the multiplexed exec stream: every frame starts with an 8 byte header
// (stream type, 3 zero bytes, big endian payload length). Nil writers discard their stream.
func demuxDockerStream(r io.Reader, stdout, stderr io.Writer) error {
	if stdout == nil {
		stdout = io.Discard
	}
	if stderr == nil {
		stderr = io.Discard
	}
	var header [8]byte
	for {
		if _, err := io.ReadFull(r, header[:]); err != nil {
			if err == io.EOF {
				return nil
			}
			if err == io.ErrUnexpectedEOF {
				return fmt.Errorf("docker exec stream ended inside a frame header")
			}
			if isClosedConnErr(err) {
				return nil
			}
			return err
		}
		size := int64(binary.BigEndian.Uint32(header[4:]))
		var w io.Writer
		switch header[0] {
		case 1:
			w = stdout
		case 2:
			w = stderr
		default:
			w = io.Discard
		}
		if _, err := io.CopyN(w, r, size); err != nil {
			if isClosedConnErr(err) {
				return nil
			}
			return err
		}
	}
}

func isClosedConnErr(err error) bool {
	return err != nil && strings.Contains(err.Error(), "use of closed network connection")
}

// execExitCode waits until the exec instance has finished and returns its exit code.
func (c *DockerClient) execExitCode(id string) (int, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	for {
		var inspect struct {
			Running  bool `json:"Running"`
			ExitCode int  `json:"ExitCode"`
		}
		if err := c.do(ctx, http.MethodGet, "/exec/"+url.PathEscape(id)+"/json", nil, nil, &inspect); err != nil {
			return -1, err
		}
		if !inspect.Running {
			if inspect.ExitCode != 0 {
				return inspect.ExitCode, &ExitError{Code: inspect.ExitCode}
			}
			return 0, nil
		}
		select {
		case <-ctx.Done():
			return -1, ctx.Err()
		case <-time.After(50 * time.Millisecond):
		}
	}
}

// DockerContainerState is the subset of GET /containers/{id}/json the dashboard uses.
type DockerContainerState struct {
	Running bool   `json:"Running"`
	Status  string `json:"Status"`
}

// ContainerState returns the state of a container.
func (c *DockerClient) ContainerState(ctx context.Context, container string) (DockerContainerState, error) {
	var inspect struct {
		State DockerContainerState `json:"State"`
	}
	err := c.do(ctx, http.MethodGet, "/containers/"+url.PathEscape(container)+"/json", nil, nil, &inspect)
	return inspect.State, err
}

// StartContainer starts a stopped container.
func (c *DockerClient) StartContainer(ctx context.Context, container string) error {
	return c.do(ctx, http.MethodPost, "/containers/"+url.PathEscape(container)+"/start", nil, nil, nil)
}
