package backend

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

// DockerClient talks to the Docker Engine API over the Unix socket (or TCP, for example a
// docker-socket-proxy). Only the few endpoints the dashboard needs are implemented, so no
// Docker SDK is required.
type DockerClient struct {
	http    *http.Client
	baseURL string // scheme and host, e.g. http://docker
	prefix  string // API version prefix, e.g. /v1.47
}

// DockerVersion is the subset of GET /version the dashboard uses.
type DockerVersion struct {
	Version    string `json:"Version"`
	APIVersion string `json:"ApiVersion"`
	Os         string `json:"Os"`
	Arch       string `json:"Arch"`
}

// DockerContainer is the subset of GET /containers/json the dashboard uses.
type DockerContainer struct {
	ID     string            `json:"Id"`
	Names  []string          `json:"Names"`
	Image  string            `json:"Image"`
	State  string            `json:"State"`
	Status string            `json:"Status"`
	Labels map[string]string `json:"Labels"`
}

// Name returns the container name without the leading slash.
func (c DockerContainer) Name() string {
	if len(c.Names) == 0 {
		return ""
	}
	return strings.TrimPrefix(c.Names[0], "/")
}

const defaultDockerHost = "unix:///var/run/docker.sock"

// NewDockerClient creates a client for host ("unix:///path" or "tcp://host:port"). An empty
// host uses DOCKER_HOST and falls back to the default socket. The API version is negotiated
// with the engine, because current engines reject old API versions.
func NewDockerClient(ctx context.Context, host string) (*DockerClient, error) {
	if host == "" {
		host = os.Getenv("DOCKER_HOST")
	}
	if host == "" {
		host = defaultDockerHost
	}
	u, err := url.Parse(host)
	if err != nil {
		return nil, fmt.Errorf("invalid DOCKER_HOST %q: %w", host, err)
	}

	transport := &http.Transport{MaxIdleConns: 4, IdleConnTimeout: 90 * time.Second}
	c := &DockerClient{http: &http.Client{Transport: transport}}
	switch u.Scheme {
	case "unix":
		socket := u.Path
		dialer := &net.Dialer{Timeout: 5 * time.Second}
		transport.DialContext = func(ctx context.Context, _, _ string) (net.Conn, error) {
			return dialer.DialContext(ctx, "unix", socket)
		}
		c.baseURL = "http://docker"
	case "tcp", "http":
		if u.Host == "" {
			return nil, fmt.Errorf("invalid DOCKER_HOST %q: missing host", host)
		}
		c.baseURL = "http://" + u.Host
	default:
		return nil, fmt.Errorf("unsupported DOCKER_HOST %q: use unix:// or tcp://", host)
	}

	v, err := c.Version(ctx)
	if err != nil {
		return nil, fmt.Errorf("Docker engine not reachable at %s: %w", host, err)
	}
	if v.APIVersion == "" {
		return nil, fmt.Errorf("Docker engine at %s reported no API version", host)
	}
	c.prefix = "/v" + v.APIVersion
	return c, nil
}

// do sends a request and decodes a JSON response into out (if out is not nil).
func (c *DockerClient) do(ctx context.Context, method, path string, query url.Values, body, out interface{}) error {
	var reqBody io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return err
		}
		reqBody = bytes.NewReader(data)
	}
	target := c.baseURL + c.prefix + path
	if len(query) > 0 {
		target += "?" + query.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, method, target, reqBody)
	if err != nil {
		return err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		var apiErr struct {
			Message string `json:"message"`
		}
		data, _ := io.ReadAll(io.LimitReader(resp.Body, 64*1024))
		if json.Unmarshal(data, &apiErr) == nil && apiErr.Message != "" {
			return fmt.Errorf("docker API %s %s: %s", method, path, apiErr.Message)
		}
		return fmt.Errorf("docker API %s %s: %s", method, path, resp.Status)
	}
	if out == nil {
		_, _ = io.Copy(io.Discard, resp.Body)
		return nil
	}
	return json.NewDecoder(io.LimitReader(resp.Body, 32<<20)).Decode(out)
}

// Version returns the engine version. It uses the unversioned endpoint, so it also works
// before the API version is known.
func (c *DockerClient) Version(ctx context.Context) (DockerVersion, error) {
	var v DockerVersion
	err := c.do(ctx, http.MethodGet, "/version", nil, nil, &v)
	return v, err
}

// ListContainers returns all containers (running or not) that carry the given label
// ("key" or "key=value").
func (c *DockerClient) ListContainers(ctx context.Context, label string) ([]DockerContainer, error) {
	filters, _ := json.Marshal(map[string][]string{"label": {label}})
	q := url.Values{"all": {"1"}, "filters": {string(filters)}}
	var list []DockerContainer
	err := c.do(ctx, http.MethodGet, "/containers/json", q, nil, &list)
	return list, err
}

// -------------------------------------------------------------
// Detection
// -------------------------------------------------------------

// containerMarkers can be overridden in tests.
var containerMarkers = struct {
	files  []string
	cgroup string
}{
	files:  []string{"/.dockerenv", "/run/.containerenv"},
	cgroup: "/proc/1/cgroup",
}

// InContainer reports whether the dashboard itself runs inside a container (Docker, Podman
// or containerd based).
func InContainer() bool {
	for _, f := range containerMarkers.files {
		if _, err := os.Stat(f); err == nil {
			return true
		}
	}
	data, err := os.ReadFile(containerMarkers.cgroup)
	if err != nil {
		return false
	}
	s := string(data)
	for _, marker := range []string{"docker", "containerd", "kubepods", "libpod"} {
		if strings.Contains(s, marker) {
			return true
		}
	}
	return false
}
