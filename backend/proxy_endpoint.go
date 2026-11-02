package backend

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"
)

// Paths of the proxy on an agent.
const (
	AgentProxyPath        = "/agent/v1/proxy"
	AgentProxyPreparePath = "/agent/v1/proxy/prepare"
)

// LocalProxyEndpoint is this dashboard's own machine.
type LocalProxyEndpoint struct{ Host *ProxyHost }

func (e LocalProxyEndpoint) Prepare(ctx context.Context, asProxy bool) (ProxyPrepared, error) {
	return e.Host.Prepare(ctx, asProxy)
}
func (e LocalProxyEndpoint) Apply(ctx context.Context, plan ProxyPlan) error {
	return e.Host.Apply(ctx, plan)
}
func (e LocalProxyEndpoint) Status(ctx context.Context) (ProxyHostStatus, error) {
	return e.Host.Status(ctx), nil
}

// TunnelProxyEndpoint is a node, reached through its tunnel.
type TunnelProxyEndpoint struct{ Client *AgentTunnelClient }

func (e TunnelProxyEndpoint) call(ctx context.Context, method, path string, in, out interface{}) error {
	var body io.Reader
	if in != nil {
		data, err := json.Marshal(in)
		if err != nil {
			return err
		}
		body = bytes.NewReader(data)
	}
	req, err := http.NewRequestWithContext(ctx, method, path, body)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := e.Client.DoControl(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return err
	}
	if resp.StatusCode != http.StatusOK {
		var failure struct {
			Error string `json:"error"`
		}
		if json.Unmarshal(data, &failure) == nil && failure.Error != "" {
			return errors.New(failure.Error)
		}
		return fmt.Errorf("the node answered with status %d", resp.StatusCode)
	}
	if out == nil {
		return nil
	}
	return json.Unmarshal(data, out)
}

func (e TunnelProxyEndpoint) Prepare(ctx context.Context, asProxy bool) (ProxyPrepared, error) {
	var out ProxyPrepared
	err := e.call(ctx, http.MethodPost, AgentProxyPreparePath, map[string]bool{"as_proxy": asProxy}, &out)
	// A node answers with what it found; its key still has to look like one.
	if err == nil && !validProxyKey(out.PublicKey) {
		err = errors.New("the node sent an invalid public key")
	}
	return out, err
}
func (e TunnelProxyEndpoint) Apply(ctx context.Context, plan ProxyPlan) error {
	return e.call(ctx, http.MethodPut, AgentProxyPath, plan, nil)
}
func (e TunnelProxyEndpoint) Status(ctx context.Context) (ProxyHostStatus, error) {
	var out ProxyHostStatus
	err := e.call(ctx, http.MethodGet, AgentProxyPath, nil, &out)
	return out, err
}

// ProxyNodePorts lists the game ports of this machine as "udp/27015". A node only opens these.
func (im *InstanceManager) ProxyNodePorts() map[string]bool {
	ports := map[string]bool{}
	add := func(list []PortProbe) {
		for _, p := range list {
			ports[strings.ToLower(p.Protocol)+"/"+strconv.Itoa(p.Port)] = true
		}
	}
	for _, srv := range im.snapshot() {
		add(srv.ParsedPorts)
		add(parseServerPortsFromConfig(srv.Dir, filepath.Join(srv.Dir, srv.configLgsmRel()), srv.Script, srv.Game))
		// LinuxGSM lists a game port with one protocol, the game may use both.
		if srv.Port > 0 {
			ports["udp/"+strconv.Itoa(srv.Port)] = true
			ports["tcp/"+strconv.Itoa(srv.Port)] = true
		}
	}
	return ports
}
