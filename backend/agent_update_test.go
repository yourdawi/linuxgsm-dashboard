package backend

import "testing"

func TestNodeUpdateInfo(t *testing.T) {
	hello := func(version string, selfUpdate, allow bool) *AgentHello {
		return &AgentHello{Version: version, Capabilities: Capabilities{SelfUpdate: selfUpdate}, AllowSelfUpdate: allow}
	}
	online := func(h *AgentHello) NodeStatus { return NodeStatus{Online: true, Hello: h} }
	cases := []struct {
		name       string
		controller string
		st         NodeStatus
		want       NodeUpdate
	}{
		{"older release", "1.4.2", online(hello("1.3.9", true, true)), NodeUpdate{Possible: true, Target: "v1.4.2"}},
		{"old self-compiled dashboard", "1.4.2", online(hello("dev", true, true)), NodeUpdate{Possible: true, Target: "v1.4.2"}},
		{"same version", "1.4.2", online(hello("1.4.2", true, true)), NodeUpdate{Reason: NodeUpdateCurrent}},
		{"offline", "1.4.2", NodeStatus{Hello: hello("1.3.0", true, true)}, NodeUpdate{Reason: NodeUpdateOffline}},
		{"no hello", "1.4.2", NodeStatus{Online: true}, NodeUpdate{Reason: NodeUpdateOffline}},
		{"dashboard from source", "dev-main-1a2b3c4", online(hello("1.3.0", true, true)), NodeUpdate{Reason: NodeUpdateDashboardDev}},
		{"node newer", "1.4.2", online(hello("1.5.0", true, true)), NodeUpdate{Reason: NodeUpdateNewer}},
		{"node from source", "1.4.2", online(hello("dev-main-9z9z9z9", true, true)), NodeUpdate{Reason: NodeUpdateUnknown}},
		{"docker node", "1.4.2", online(hello("1.3.0", false, true)), NodeUpdate{Reason: NodeUpdateNoSelfUpdate}},
		{"disabled on node", "1.4.2", online(hello("1.3.0", true, false)), NodeUpdate{Reason: NodeUpdateDisabled}},
	}
	for _, c := range cases {
		if got := NodeUpdateInfo(c.controller, c.st); got != c.want {
			t.Errorf("%s: got %+v, want %+v", c.name, got, c.want)
		}
	}
}
