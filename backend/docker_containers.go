package backend

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
)

// PullImage pulls image ("repo:tag") and reports progress through logf. Repeated download and
// extraction progress lines are left out.
func (c *DockerClient) PullImage(ctx context.Context, image string, logf func(string)) error {
	repo, tag := image, "latest"
	if i := strings.LastIndex(image, ":"); i > strings.LastIndex(image, "/") {
		repo, tag = image[:i], image[i+1:]
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		c.baseURL+c.prefix+"/images/create?"+url.Values{"fromImage": {repo}, "tag": {tag}}.Encode(), nil)
	if err != nil {
		return err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		data, _ := io.ReadAll(io.LimitReader(resp.Body, 64*1024))
		return fmt.Errorf("docker pull %s: %s", image, apiMessage(data, resp.Status))
	}

	dec := json.NewDecoder(resp.Body)
	last := ""
	for {
		var msg struct {
			Status string `json:"status"`
			ID     string `json:"id"`
			Error  string `json:"error"`
		}
		if err := dec.Decode(&msg); err == io.EOF {
			return nil
		} else if err != nil {
			return fmt.Errorf("docker pull %s: %w", image, err)
		}
		if msg.Error != "" {
			return fmt.Errorf("docker pull %s: %s", image, msg.Error)
		}
		switch msg.Status {
		case "", "Downloading", "Extracting", "Waiting", "Verifying Checksum":
			continue
		}
		line := msg.Status
		if msg.ID != "" && msg.ID != tag {
			line = msg.ID + ": " + line
		}
		if line != last && logf != nil {
			logf(line + "\n")
		}
		last = line
	}
}

// apiMessage extracts the "message" of a Docker API error response.
func apiMessage(data []byte, fallback string) string {
	var apiErr struct {
		Message string `json:"message"`
	}
	if json.Unmarshal(data, &apiErr) == nil && apiErr.Message != "" {
		return apiErr.Message
	}
	return fallback
}

// DockerCreateConfig is the subset of POST /containers/create the dashboard uses.
type DockerCreateConfig struct {
	Image       string            `json:"Image"`
	Env         []string          `json:"Env,omitempty"`
	Labels      map[string]string `json:"Labels,omitempty"`
	StopTimeout int               `json:"StopTimeout,omitempty"`
	HostConfig  DockerHostConfig  `json:"HostConfig"`
}

// DockerHostConfig is the subset of HostConfig the dashboard uses.
type DockerHostConfig struct {
	Binds         []string            `json:"Binds,omitempty"`
	NetworkMode   string              `json:"NetworkMode,omitempty"`
	RestartPolicy DockerRestartPolicy `json:"RestartPolicy"`
}

// DockerRestartPolicy is the restart policy of a container.
type DockerRestartPolicy struct {
	Name string `json:"Name"`
}

// CreateContainer creates (but does not start) a container and returns its ID.
func (c *DockerClient) CreateContainer(ctx context.Context, name string, cfg DockerCreateConfig) (string, error) {
	var created struct {
		ID string `json:"Id"`
	}
	err := c.do(ctx, http.MethodPost, "/containers/create", url.Values{"name": {name}}, cfg, &created)
	return created.ID, err
}

// RemoveContainer removes a container, stopping it first when force is set.
func (c *DockerClient) RemoveContainer(ctx context.Context, container string, force bool) error {
	q := url.Values{}
	if force {
		q.Set("force", "1")
	}
	return c.do(ctx, http.MethodDelete, "/containers/"+url.PathEscape(container), q, nil, nil)
}

// DockerContainerInfo is the subset of GET /containers/{id}/json the dashboard uses.
type DockerContainerInfo struct {
	Name   string               `json:"Name"`
	State  DockerContainerState `json:"State"`
	Config struct {
		Image  string            `json:"Image"`
		Env    []string          `json:"Env"`
		Labels map[string]string `json:"Labels"`
	} `json:"Config"`
	HostConfig struct {
		NetworkMode string `json:"NetworkMode"`
	} `json:"HostConfig"`
}

// InspectContainer returns details of a container. found is false when it does not exist.
func (c *DockerClient) InspectContainer(ctx context.Context, container string) (info DockerContainerInfo, found bool, err error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+c.prefix+"/containers/"+url.PathEscape(container)+"/json", nil)
	if err != nil {
		return info, false, err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return info, false, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return info, false, nil
	}
	if resp.StatusCode >= 300 {
		data, _ := io.ReadAll(io.LimitReader(resp.Body, 64*1024))
		return info, false, fmt.Errorf("docker inspect %s: %s", container, apiMessage(data, resp.Status))
	}
	err = json.NewDecoder(io.LimitReader(resp.Body, 8<<20)).Decode(&info)
	return info, err == nil, err
}

// FollowLogs streams the output of a container line by line to onLine until ctx ends or the
// container stops. onLine returns false to stop following.
func (c *DockerClient) FollowLogs(ctx context.Context, container string, onLine func(string) bool) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	q := url.Values{"follow": {"1"}, "stdout": {"1"}, "stderr": {"1"}}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+c.prefix+"/containers/"+url.PathEscape(container)+"/logs?"+q.Encode(), nil)
	if err != nil {
		return err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		data, _ := io.ReadAll(io.LimitReader(resp.Body, 64*1024))
		return fmt.Errorf("docker logs %s: %s", container, apiMessage(data, resp.Status))
	}

	pr, pw := io.Pipe()
	go func() {
		pw.CloseWithError(demuxDockerStream(resp.Body, pw, pw))
	}()
	reader := bufio.NewReader(pr)
	for {
		line, err := reader.ReadString('\n')
		if line != "" && !onLine(line) {
			cancel()
			pr.Close()
			return nil
		}
		if err != nil {
			if err == io.EOF || ctx.Err() != nil {
				return nil
			}
			return err
		}
	}
}
