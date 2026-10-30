package backend

import (
	"errors"
	"regexp"
	"strings"
)

// Most LinuxGSM commands run without asking anything. The few that do read their answers from
// stdin, and they do not all ask yes/no: mods-install and mods-remove want a mod name, the TS3
// password change wants the new password. Each command therefore gets exactly the input it needs
// and nothing else; an unexpected prompt then ends with LinuxGSM's "no input available" instead
// of being answered blindly.

// ActionInput carries the values a command asks for.
type ActionInput struct {
	Mod      string // mods-install, mods-remove: mod command name, e.g. metamodsource2
	Password string // change-password (TS3 ServerAdmin)
}

var (
	reModName = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,63}$`)
	// LinuxGSM puts the TS3 password into a shell line (serveradmin_password="..."), so only
	// characters without a meaning in double-quoted shell strings are allowed.
	reTS3Password = regexp.MustCompile(`^[A-Za-z0-9._+=@#%^*!?:,~-]{8,64}$`)
)

// IsValidModName validates a LinuxGSM mod command name.
func IsValidModName(name string) bool { return reModName.MatchString(name) }

// ActionStdin returns the answers for the prompts of a command (see command_*.sh in LinuxGSM).
func ActionStdin(action string, in ActionInput) (string, error) {
	switch action {
	case "fastdl":
		// "Overwrite existing directory?", "Force clients to download files?", "Continue?"
		return "y\ny\ny\n", nil
	case "install-default-resources":
		// "Do you want to install MTA default resources?"
		return "y\n", nil
	case "mods-install", "mods-remove":
		if !IsValidModName(in.Mod) {
			return "", errors.New("choose a mod")
		}
		// The mod name, then "Continue?" (asked when the mod is already installed, or before removing).
		return in.Mod + "\ny\n", nil
	case "change-password":
		if !reTS3Password.MatchString(in.Password) {
			return "", errors.New("the password needs 8 to 64 characters: letters, digits and . _ + = @ # % ^ * ! ? : , ~ -")
		}
		// "Continue?", then the new password.
		return "y\n" + in.Password + "\n", nil
	}
	return "", nil
}

// ModInfo is a mod LinuxGSM offers for a game.
type ModInfo struct {
	Command     string `json:"command"`
	Description string `json:"description"`
}

// ParseAvailableMods reads the "Available addons/mods" list that mods-install prints before it
// asks for a name:
//
//	Available addons/mods
//	=================================
//	Metamod:Source 2.0 - Plugins Framework (Dev Build) - https://www.metamodsource.net
//	 * metamodsource2
func ParseAvailableMods(output string) []ModInfo {
	var mods []ModInfo
	inList := false
	prev := ""
	for _, raw := range strings.Split(stripAnsi(output), "\n") {
		line := strings.TrimSpace(strings.TrimRight(raw, "\r"))
		if strings.HasPrefix(line, "Available addons/mods") {
			inList = true
			prev = ""
			continue
		}
		if !inList || line == "" || strings.Trim(line, "=") == "" {
			continue
		}
		if strings.HasPrefix(line, "Enter an addon/mod") {
			break
		}
		if name, ok := strings.CutPrefix(line, "* "); ok {
			name = strings.TrimSpace(name)
			if IsValidModName(name) {
				mods = append(mods, ModInfo{Command: name, Description: sanitizeDisplayText(prev, 300)})
			}
			prev = ""
			continue
		}
		prev = line
	}
	return mods
}
