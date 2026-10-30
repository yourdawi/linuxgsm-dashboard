package backend

import "errors"

// Roles
const (
	RoleAdmin = "admin"
	RoleUser  = "user"
)

// Permission scopes that can be assigned to non-admin users.
const (
	PermStart    = "start"    // start a server
	PermStop     = "stop"     // stop a server
	PermRestart  = "restart"  // restart a server
	PermConsole  = "console"  // read console/logs, send console & RCON commands, kick/ban players
	PermConfig   = "config"   // LinuxGSM/game config editor
	PermFiles    = "files"    // file manager (list/read/upload/edit/download in serverfiles)
	PermBackup   = "backup"   // create/list/download/upload/delete/restore backups, cloud sync
	PermSchedule = "schedule" // change cron schedules (incl. automatic backups)
	PermAlerts   = "alerts"   // read/modify alert integrations (webhooks, tokens, SMTP)

	// PermView only requires access to the server (any assigned user).
	PermView = "view"
	// PermAdmin is a pseudo permission: only administrators.
	PermAdmin = "admin"
)

// AssignablePermissions lists every permission an administrator may grant to a user.
var AssignablePermissions = []string{
	PermStart, PermStop, PermRestart, PermConsole, PermConfig, PermFiles, PermBackup, PermSchedule, PermAlerts,
}

// IsAssignablePermission validates permission names from the user management API.
func IsAssignablePermission(p string) bool {
	for _, a := range AssignablePermissions {
		if a == p {
			return true
		}
	}
	return false
}

// Authorization errors. Handlers map them to HTTP status codes.
var (
	ErrUnauthenticated = errors.New("unauthorized")
	ErrForbidden       = errors.New("forbidden")
	ErrServerNotFound  = errors.New("server not found")
)

// IsAdmin reports whether the user has the administrator role.
func (u User) IsAdmin() bool {
	return u.Role == RoleAdmin
}

// CanAccessServer reports whether the user is assigned to serverID, directly or through the node
// the server runs on (admins can access all servers). A node assignment also covers servers
// installed on the node later.
func (u User) CanAccessServer(serverID string) bool {
	if u.IsAdmin() {
		return true
	}
	for _, s := range u.Servers {
		if s == serverID {
			return true
		}
	}
	if node, _ := SplitServerID(serverID); node != "" {
		for _, n := range u.Nodes {
			if n == node {
				return true
			}
		}
	}
	return false
}

// HasPermission reports whether the user holds perm (admins hold every permission).
func (u User) HasPermission(perm string) bool {
	if u.IsAdmin() {
		return true
	}
	switch perm {
	case PermAdmin:
		return false
	case PermView:
		return true
	}
	for _, p := range u.Permissions {
		if p == perm {
			return true
		}
	}
	return false
}

// AuthorizeServerAction is the single authorization decision for every server scoped request.
// Order of checks:
//  1. authenticated user
//  2. user may access this server (checked before existence so non-admins cannot enumerate servers)
//  3. server exists
//  4. user holds the required permission (PermAdmin = administrators only)
func AuthorizeServerAction(user *User, serverID string, perm string, serverExists func(string) bool) error {
	if user == nil || user.Username == "" {
		return ErrUnauthenticated
	}
	if !IsValidServerID(serverID) {
		return ErrServerNotFound
	}
	if !user.CanAccessServer(serverID) {
		return ErrForbidden
	}
	if serverExists != nil && !serverExists(serverID) {
		return ErrServerNotFound
	}
	if perm == "" || !user.HasPermission(perm) {
		return ErrForbidden
	}
	return nil
}

// ActionPermission maps a LinuxGSM command to the permission required to run it.
// The second return value is false for unknown actions, which must be rejected.
func ActionPermission(action string) (string, bool) {
	switch action {
	case "start":
		return PermStart, true
	case "stop":
		return PermStop, true
	case "restart":
		return PermRestart, true
	case "backup":
		return PermBackup, true
	case "details": // prints server, RCON and admin passwords in clear text
		return PermConfig, true
	case "update", "validate", "update-lgsm", "force-update", "test-alert", "map-wipe", "full-wipe",
		"change-password", "check-update", "mods-install", "mods-update", "mods-remove", "fastdl",
		"map-compressor", "skeleton", "install-default-resources", "postdetails", "workshop-update":
		return PermAdmin, true
	}
	return "", false
}

// BulkActionPermission is the strict allowlist for POST /api/servers/bulk.
func BulkActionPermission(action string) (string, bool) {
	switch action {
	case "start", "stop", "restart", "update", "backup":
		return ActionPermission(action)
	}
	return "", false
}
