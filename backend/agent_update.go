package backend

// Updating nodes from the dashboard. The dashboard only triggers the update; the node downloads
// the release itself and verifies its signature with the key built into its own binary, so a
// compromised dashboard cannot make a node run anything but an official release. The target is
// always the dashboard's own version, which keeps dashboard and nodes compatible.

// Reasons why a node cannot be updated from the dashboard (NodeUpdate.Reason).
const (
	NodeUpdateCurrent      = "current"        // already runs the dashboard's version
	NodeUpdateOffline      = "offline"        // not connected
	NodeUpdateDashboardDev = "dashboard-dev"  // the dashboard is a build from source, no release to install
	NodeUpdateNewer        = "node-newer"     // the node runs a newer release; update the dashboard
	NodeUpdateNoSelfUpdate = "no-self-update" // the node's runtime cannot update itself (Docker)
	NodeUpdateDisabled     = "disabled"       // allow_self_update is off in the node's policy
	NodeUpdateUnknown      = "unknown"        // the node's version cannot be compared (build from source)
)

// NodeUpdate says whether a node can be updated to the dashboard's version.
type NodeUpdate struct {
	Possible bool   `json:"possible"`
	Target   string `json:"target,omitempty"` // release tag, e.g. v1.2.3
	Reason   string `json:"reason,omitempty"` // set when not possible
}

// NodeUpdateInfo decides whether a node can be updated to controllerVersion.
func NodeUpdateInfo(controllerVersion string, st NodeStatus) NodeUpdate {
	target := "v" + controllerVersion
	switch {
	case !st.Online || st.Hello == nil:
		return NodeUpdate{Reason: NodeUpdateOffline}
	case !IsStableReleaseTag(target):
		return NodeUpdate{Reason: NodeUpdateDashboardDev}
	case st.Hello.Version == controllerVersion:
		return NodeUpdate{Reason: NodeUpdateCurrent}
	}
	if _, ok := ParseVersion(st.Hello.Version); !ok && st.Hello.Version != "dev" {
		// Builds from source ("dev-main-...") are never replaced by releases automatically.
		return NodeUpdate{Reason: NodeUpdateUnknown}
	}
	if IsNewerVersion(st.Hello.Version, controllerVersion) {
		return NodeUpdate{Reason: NodeUpdateNewer}
	}
	if !st.Hello.Capabilities.SelfUpdate {
		return NodeUpdate{Reason: NodeUpdateNoSelfUpdate}
	}
	if !st.Hello.AllowSelfUpdate {
		return NodeUpdate{Reason: NodeUpdateDisabled}
	}
	return NodeUpdate{Possible: true, Target: target}
}
