package backend

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// The config file LinuxGSM itself uses for a game (servercfgfullpath, for Don't Starve also
// clustercfgfullpath) is read from the merged LinuxGSM configs, so the editor lists it wherever
// the game keeps it: below Saved/ (ARK, Conan, Palworld), in $HOME (Project Zomboid, Euro Truck
// Simulator) or with an extension the scan does not list (.xml, .sii, .lua).

var reLGSMVar = regexp.MustCompile(`\$\{([A-Za-z_][A-Za-z0-9_]*)\}|\$([A-Za-z_][A-Za-z0-9_]*)`)

// lgsmBaseVars are the variables linuxgsm.sh sets before it loads the configs.
func lgsmBaseVars(srv GameServerInstance) map[string]string {
	if srv.Layout == LayoutData {
		// Inside the official container: script in /app, data (and the user's home) in /data.
		return map[string]string{
			"rootdir": dockerScriptDir, "lgsmdir": dockerScriptDir + "/lgsm", "serverfiles": containerDataDir + "/serverfiles",
			"HOME": containerDataDir, "selfname": srv.Script, "USER": "linuxgsm", "username": "linuxgsm",
		}
	}
	home := srv.Dir
	if users, err := readPasswd(); err == nil {
		for _, u := range users {
			if u.Name == srv.User && u.Home != "" {
				home = u.Home
			}
		}
	}
	return map[string]string{
		"rootdir": srv.Dir, "lgsmdir": filepath.Join(srv.Dir, "lgsm"), "serverfiles": filepath.Join(srv.Dir, "serverfiles"),
		"HOME": home, "selfname": srv.Script, "USER": srv.User, "username": srv.User,
	}
}

// expandLGSMValue expands ${var} and $var with the config values and the base variables, like
// bash does when LinuxGSM sources its configs. It fails for unknown variables and command
// substitutions, which the dashboard cannot evaluate.
func expandLGSMValue(value string, values, base map[string]string) (string, bool) {
	if strings.ContainsAny(value, "`") || strings.Contains(value, "$(") {
		return "", false
	}
	for depth := 0; depth < 10 && strings.Contains(value, "$"); depth++ {
		ok := true
		value = reLGSMVar.ReplaceAllStringFunc(value, func(m string) string {
			name := reLGSMVar.FindStringSubmatch(m)
			key := name[1] + name[2]
			if v, found := base[key]; found {
				return v
			}
			if v, found := values[key]; found {
				return v
			}
			ok = false
			return ""
		})
		if !ok {
			return "", false
		}
	}
	return value, !strings.Contains(value, "$")
}

// linuxGSMConfigPaths returns the game config files LinuxGSM uses for srv, as paths the dashboard
// can open.
func linuxGSMConfigPaths(srv GameServerInstance) []string {
	values := lgsmCfgValues(srv, true)
	base := lgsmBaseVars(srv)
	var out []string
	for _, key := range []string{"servercfgfullpath", "clustercfgfullpath"} {
		raw, ok := values[key]
		if !ok || raw == "" {
			continue
		}
		p, ok := expandLGSMValue(raw, values, base)
		if !ok || !filepath.IsAbs(p) {
			continue
		}
		if srv.Layout == LayoutData {
			if p = srv.dashboardPath(p); p == "" {
				continue
			}
		}
		out = append(out, filepath.Clean(p))
	}
	return out
}

// isLinuxGSMConfigFile reports whether path is one of the game configs LinuxGSM uses for srv and
// returns it resolved. It must be a regular file below the server directory or the user's home,
// owned by the server user when it lies outside the server directory.
func isLinuxGSMConfigFile(srv GameServerInstance, path string) (string, bool) {
	clean := filepath.Clean(path)
	for _, p := range linuxGSMConfigPaths(srv) {
		if p != clean {
			continue
		}
		// The server directory, the target of a symlinked serverfiles and the user's home.
		roots := append([]string{srv.Dir}, configRoots(srv)...)
		if home := lgsmBaseVars(srv)["HOME"]; srv.Layout != LayoutData && home != srv.Dir && !IsForbiddenServerDir(home) {
			roots = append(roots, home)
		}
		resolved, err := ResolveAbsInRoots(clean, roots...)
		if err != nil {
			return "", false
		}
		if dir, err := filepath.EvalSymlinks(srv.Dir); err != nil || !IsWithin(dir, resolved) {
			if uid, err := fileOwnerUID(resolved); err != nil || (uid >= 0 && uid != srv.UID) {
				return "", false
			}
		}
		return resolved, true
	}
	return "", false
}

// Directories the config scan skips: game data, logs, caches and tools, not configs.
var skipConfigDirs = map[string]bool{
	"steamapps": true, "steam": true, "steamclient": true, ".steam": true, "Engine": true,
	"MonoBleedingEdge": true, "linux64": true, "node_modules": true, ".git": true,
	"lgsm": true, "log": true, "logs": true, "Logs": true, "backups": true,
	"SaveGames": true, "Crashes": true, "Content": true, "Paks": true, "Movies": true,
	"world": true, "world_nether": true, "world_the_end": true, "cache": true, "Cache": true,
}

// listedConfigExt are the extensions the scan lists. .json and .txt are only listed near the
// top of serverfiles: deeper they are mostly save data (Minecraft stats, logs).
var listedConfigExt = map[string]bool{
	".cfg": true, ".ini": true, ".properties": true, ".conf": true, ".yml": true, ".yaml": true,
	".xml": true, ".toml": true,
}

const (
	maxConfigScanDepth = 6
	maxConfigScanFiles = 400
	maxConfigScanDirs  = 5000
)

// findConfigs lists config files below dir (serverfiles).
func findConfigs(baseDir, dir string) []ConfigFile {
	var out []ConfigFile
	dirs := 0
	var walk func(path string, depth int)
	walk = func(path string, depth int) {
		if depth > maxConfigScanDepth || len(out) >= maxConfigScanFiles || dirs >= maxConfigScanDirs {
			return
		}
		dirs++
		entries, err := os.ReadDir(path)
		if err != nil {
			return
		}
		for _, e := range entries {
			name := e.Name()
			full := filepath.Join(path, name)
			if e.IsDir() {
				if !skipConfigDirs[name] {
					walk(full, depth+1)
				}
				continue
			}
			if !e.Type().IsRegular() {
				continue
			}
			ext := strings.ToLower(filepath.Ext(name))
			if !listedConfigExt[ext] && !((ext == ".json" || ext == ".txt") && depth <= 1) {
				continue
			}
			rel, err := filepath.Rel(baseDir, full)
			if err != nil {
				rel = name
			}
			out = append(out, ConfigFile{Name: filepath.ToSlash(rel), Path: full})
			if len(out) >= maxConfigScanFiles {
				return
			}
		}
	}
	walk(dir, 0)
	return out
}

// IsLinuxGSMSettingsFile reports whether path is one of LinuxGSM's own config files of the server
// (config-lgsm: _default.cfg, common.cfg, the instance and secrets files, cloud_backup.json).
// LinuxGSM sources them with bash and they hold the alert secrets, so editing them amounts to a
// shell as the server user: only administrators may.
func (im *InstanceManager) IsLinuxGSMSettingsFile(serverID, path string) bool {
	srv, ok := im.getServer(serverID)
	if !ok {
		return false
	}
	dir := filepath.Join(srv.Dir, srv.configLgsmRel())
	if IsWithin(dir, filepath.Clean(path)) {
		return true
	}
	// Also through a symlink, e.g. serverfiles/x.cfg pointing at a secrets file.
	realDir, err1 := filepath.EvalSymlinks(dir)
	realPath, err2 := filepath.EvalSymlinks(path)
	return err1 == nil && err2 == nil && IsWithin(realDir, realPath)
}
