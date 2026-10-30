package backend

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// The dashboard deletes a Linux user together with its home directory only when it created that
// user itself. A heuristic on the home directory alone cannot tell a game server user from a
// fresh personal account (a cloud VM's "ubuntu" user holds only dotfiles too), so created users
// are recorded in created_users.json in the config directory.

// hostConfigDir is the dashboard's config directory; set by NewInstanceManager.
var hostConfigDir string

var createdUsersMu sync.Mutex

type createdUser struct {
	Name string `json:"name"`
	UID  int    `json:"uid"`
}

func createdUsersPath() string {
	if hostConfigDir == "" {
		return ""
	}
	return filepath.Join(hostConfigDir, "created_users.json")
}

func loadCreatedUsers() []createdUser {
	path := createdUsersPath()
	if path == "" {
		return nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var users []createdUser
	_ = json.Unmarshal(data, &users)
	return users
}

// recordCreatedUser remembers that the dashboard created the Linux user name.
func recordCreatedUser(name string) {
	path := createdUsersPath()
	if path == "" {
		return
	}
	uid := -1
	if users, err := readPasswd(); err == nil {
		for _, u := range users {
			if u.Name == name {
				uid = u.UID
			}
		}
	}
	createdUsersMu.Lock()
	defer createdUsersMu.Unlock()
	list := loadCreatedUsers()
	out := list[:0]
	for _, u := range list {
		if u.Name != name {
			out = append(out, u)
		}
	}
	out = append(out, createdUser{Name: name, UID: uid})
	if data, err := json.MarshalIndent(out, "", "  "); err == nil {
		_ = WriteFileAtomic(path, data, 0600)
	}
}

// forgetCreatedUser drops name after the user was deleted.
func forgetCreatedUser(name string) {
	path := createdUsersPath()
	if path == "" {
		return
	}
	createdUsersMu.Lock()
	defer createdUsersMu.Unlock()
	list := loadCreatedUsers()
	out := list[:0]
	for _, u := range list {
		if u.Name != name {
			out = append(out, u)
		}
	}
	if data, err := json.MarshalIndent(out, "", "  "); err == nil {
		_ = WriteFileAtomic(path, data, 0600)
	}
}

func wasCreatedByDashboard(name string, uid int) bool {
	createdUsersMu.Lock()
	defer createdUsersMu.Unlock()
	for _, u := range loadCreatedUsers() {
		if u.Name == name && (u.UID < 0 || u.UID == uid) {
			return true
		}
	}
	return false
}

// groupPath can be overridden in tests.
var groupPath = "/etc/group"

// adminGroups give a user administrative rights on common distributions.
var adminGroups = map[string]bool{"sudo": true, "wheel": true, "admin": true, "adm": true}

// isAdminAccount reports whether a user looks like a person's login rather than a game server
// user: a member of an administrative group, or a user with SSH keys.
func isAdminAccount(name, home string) bool {
	if hasAuthorizedKeys(home) {
		return true
	}
	f, err := os.Open(groupPath)
	if err != nil {
		return true // unknown: be careful
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		parts := strings.Split(sc.Text(), ":")
		if len(parts) < 4 || !adminGroups[parts[0]] {
			continue
		}
		for _, m := range strings.Split(parts[3], ",") {
			if m == name {
				return true
			}
		}
	}
	return false
}

func hasAuthorizedKeys(home string) bool {
	for _, f := range []string{"authorized_keys", "authorized_keys2"} {
		if info, err := os.Lstat(filepath.Join(home, ".ssh", f)); err == nil && info.Size() > 0 {
			return true
		}
	}
	return false
}

// mayDeleteLinuxUser decides whether Delete may remove the Linux user of srv with its home: the
// dashboard created it, or (users from versions that did not record this) it is a dedicated
// server user that is no administrator account.
func mayDeleteLinuxUser(srv GameServerInstance) bool {
	if !isDedicatedServerUser(srv) {
		return false
	}
	if wasCreatedByDashboard(srv.User, srv.UID) {
		return true
	}
	return !isAdminAccount(srv.User, srv.Dir)
}
