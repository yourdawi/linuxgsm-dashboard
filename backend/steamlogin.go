package backend

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// SteamLogin is the Steam account LinuxGSM installs a game with. Most games allow the anonymous
// login; the games in steamLoginGames do not.
type SteamLogin struct {
	User string
	Pass string
}

// steamLoginGames have steamuser="username" in their _default.cfg of LinuxGSM. check_steamcmd.sh
// stops with "Steam login not set" until the value is changed, so auto-install cannot succeed
// without an account.
var steamLoginGames = map[string]bool{
	"acserver": true, "ahl2server": true, "arma3server": true, "bsserver": true,
	"dayzserver": true, "jk2server": true, "jk3server": true, "kfserver": true,
	"ndserver": true, "pc2server": true, "q4server": true, "roserver": true,
	"rwrserver": true, "sbserver": true, "terrariaserver": true, "twserver": true,
}

// NeedsSteamLogin reports whether a game can only be installed with a Steam account.
func NeedsSteamLogin(gameCmd string) bool {
	return steamLoginGames[gameCmd]
}

var reSteamUser = regexp.MustCompile(`^[A-Za-z0-9_.@-]{1,64}$`)

// Valid checks the values before they are written to a config that bash loads. An empty login is
// valid: nothing is written then.
func (s SteamLogin) Valid() bool {
	if s.User == "" {
		return s.Pass == ""
	}
	if !reSteamUser.MatchString(s.User) || len(s.Pass) > 256 {
		return false
	}
	return !strings.ContainsFunc(s.Pass, func(r rune) bool { return r < 0x20 || r == 0x7f })
}

// seedSteamLogin stores the account in secrets-common.cfg before LinuxGSM runs for the first time.
// LinuxGSM loads this file after _default.cfg and only creates it when it is missing; other
// settings of an existing file stay. The file is written with mode 0600 and owned by uid (kept as
// root for uid 0, in Docker mode the entrypoint hands /data over to the game server user).
func seedSteamLogin(cfgDir string, login SteamLogin, uid, gid int) error {
	if err := mkdirAllNoSymlinks(cfgDir, 0755, uid, gid); err != nil {
		return err
	}
	target := filepath.Join(cfgDir, "secrets-common.cfg")
	content := ""
	if f, err := openNoFollow(target, os.O_RDONLY, 0); err == nil {
		data, err := io.ReadAll(io.LimitReader(f, 1<<20))
		f.Close()
		if err != nil {
			return err
		}
		content = string(data)
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	updated := applyCfgChanges(content, map[string]*string{"steamuser": &login.User, "steampass": &login.Pass})
	return writeFileNoSymlinks(target, 0600, false, uid, gid, func(f *os.File) error {
		_, err := f.WriteString(updated)
		return err
	})
}

// lgsmDownloadFailed recognises the last message of core_dl.sh after SteamCMD failed ten times.
func lgsmDownloadFailed(line string) bool {
	return strings.Contains(stripAnsi(line), "Did not complete the download")
}
