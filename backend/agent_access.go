package backend

import "strings"

// Access classes decide what an agent allows in its access mode. Every route an agent serves has
// one; read-only nodes allow AccessRead and AccessControl.
const (
	AccessRead    = "read"    // viewing: lists, status, console output, logs, file and backup downloads
	AccessControl = "control" // start, stop, restart
	AccessWrite   = "write"   // everything that changes the node: edits, commands, installs, deletes
	// AccessByAction marks routes whose class depends on the LinuxGSM action in the body.
	AccessByAction = "by-action"
)

// ActionAccess returns the access class of a LinuxGSM action. Unknown actions are writes.
func ActionAccess(action string) string {
	switch action {
	case "start", "stop", "restart":
		return AccessControl
	case "details":
		return AccessRead
	}
	return AccessWrite
}

// AgentModeAllows reports whether an access mode permits an access class. Unknown classes are
// treated as writes, so a route without a class is never readable by mistake.
func AgentModeAllows(mode, class string) bool {
	switch class {
	case AccessRead, AccessControl:
		return mode == AgentModeReadOnly || mode == AgentModeReadWrite
	default:
		return mode == AgentModeReadWrite
	}
}

// AgentProtocolVersion changes when controller and agent stop understanding each other in
// ways the release version does not capture (the tunnel itself, hello).
const AgentProtocolVersion = 1

// AgentHello is the agent's answer to GET /agent/v1/hello.
type AgentHello struct {
	Version         string       `json:"version"`
	Protocol        int          `json:"protocol"`
	Hostname        string       `json:"hostname"`
	MachineID       string       `json:"machine_id"`
	Capabilities    Capabilities `json:"capabilities"`
	Mode            string       `json:"mode"`
	AllowFirewall   bool         `json:"allow_firewall"`
	AllowSelfUpdate bool         `json:"allow_self_update"`
	Servers         []string     `json:"servers"`
	// FastDLURL is the base URL under which the node serves FastDL itself (empty: it does not).
	FastDLURL string `json:"fastdl_url,omitempty"`
}

// AgentVersionsCompatible reports whether a controller and an agent can work together. The
// controller forwards its whole API to the agent, so releases must share major and minor
// version (request and response shapes only change in minor releases). Builds from source are
// only compatible with exactly the same build.
func AgentVersionsCompatible(controller, agent string) bool {
	c, okC := ParseVersion(controller)
	a, okA := ParseVersion(agent)
	if okC && okA {
		return c[0] == a[0] && c[1] == a[1]
	}
	return strings.TrimSpace(controller) != "" && controller == agent
}

// Paths and headers of the agent API.
const (
	// AgentHelloPath answers with AgentHello.
	AgentHelloPath = "/agent/v1/hello"
	// AgentCSRPath and AgentCertPath renew the node certificate (controller driven).
	AgentCSRPath  = "/agent/v1/csr"
	AgentCertPath = "/agent/v1/cert"
	// AgentActorHeader names the dashboard user a forwarded request acts for (audit log only).
	AgentActorHeader = "X-LGSM-Actor"
	// AgentActorIPHeader is the address of that user as seen by the controller (audit log only).
	AgentActorIPHeader = "X-LGSM-Actor-IP"
	// AgentActorScheduleHeader is "1" when that user may change schedules. The node acts as
	// administrator, so checks inside handlers that go beyond the route permission need it.
	AgentActorScheduleHeader = "X-LGSM-Actor-Schedule"
	// AgentActorAdminHeader is "1" when that user is an administrator.
	AgentActorAdminHeader = "X-LGSM-Actor-Admin"
)
