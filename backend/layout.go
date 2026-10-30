package backend

import (
	"io"
	"path/filepath"
	"regexp"
	"strings"
)

// Layouts of the LinuxGSM files below GameServerInstance.Dir.
const (
	// LayoutHost is a LinuxGSM root directory: the script, lgsm/config-lgsm, lgsm/data,
	// lgsm/backup, log and serverfiles.
	LayoutHost = ""
	// LayoutData is the /data volume of the official LinuxGSM container images: config-lgsm,
	// data, log, serverfiles and (as configured by the dashboard) backup. The script itself and
	// lgsm/ stay inside the container under /app.
	LayoutData = "data"
)

// containerDataDir is where the official images mount the server data.
const containerDataDir = "/data"

// configLgsmRel is the directory holding the per-instance LinuxGSM config directories.
func (srv GameServerInstance) configLgsmRel() string {
	if srv.Layout == LayoutData {
		return "config-lgsm"
	}
	return filepath.Join("lgsm", "config-lgsm")
}

// configRel returns config-lgsm/<script>. LinuxGSM itself only uses the directory of the game
// server (see lgsmConfigRel), also for instances like vhserver-2.
func (srv GameServerInstance) configRel(script string) string {
	return filepath.Join(srv.configLgsmRel(), script)
}

// gameServerName is the LinuxGSM game server name of the script: vhserver for vhserver-2.
func gameServerName(script string) string {
	return strings.Split(script, "-")[0]
}

var reGameServerName = regexp.MustCompile(`(?m)^gameservername="([a-z0-9]+)"`)

// gameServerNameOf returns the gameservername the LinuxGSM script in dir declares; a renamed
// script (rust-eu, mc_survival) keeps the name of its game there. Without a readable script the
// name is derived from the script name.
func gameServerNameOf(dir, script string) string {
	if resolved, err := ResolveInRoot(dir, script); err == nil {
		if f, err := OpenInRootForRead(resolved); err == nil {
			buf := make([]byte, 8192)
			n, _ := io.ReadFull(f, buf)
			f.Close()
			if m := reGameServerName.FindSubmatch(buf[:n]); m != nil && IsValidScriptName(string(m[1])) {
				return string(m[1])
			}
		}
	}
	return gameServerName(script)
}

// lgsmConfigRel is the directory LinuxGSM loads the configs of this instance from
// (configdirserver = config-lgsm/<gameservername>): _default.cfg, common.cfg,
// secrets-common.cfg, <script>.cfg and secrets-<script>.cfg.
func (srv GameServerInstance) lgsmConfigRel() string {
	if srv.Layout == LayoutData {
		return srv.configRel(gameServerName(srv.Script)) // the script lives in the image
	}
	return srv.configRel(gameServerNameOf(srv.Dir, srv.Script))
}

// lgsmDataRel is the LinuxGSM data directory (serverlist, uid files).
func (srv GameServerInstance) lgsmDataRel() string {
	if srv.Layout == LayoutData {
		return "data"
	}
	return filepath.Join("lgsm", "data")
}

// modsRel is the LinuxGSM mods directory with the list of installed mods. LinuxGSM always uses
// lgsm/mods below its root; the dashboard mounts <Dir>/mods there in containers.
func (srv GameServerInstance) modsRel() string {
	if srv.Layout == LayoutData {
		return "mods"
	}
	return filepath.Join("lgsm", "mods")
}

// defaultBackupRel is where backups are stored when LinuxGSM has no backupdir setting the
// dashboard can see.
func (srv GameServerInstance) defaultBackupRel() string {
	if srv.Layout == LayoutData {
		return "backup"
	}
	return filepath.Join("lgsm", "backup")
}

// dashboardPath translates a path LinuxGSM uses inside the server (as found in its configs) to
// the path the dashboard sees. For containers /data maps to Dir; paths elsewhere in the
// container are not visible to the dashboard and yield "".
func (srv GameServerInstance) dashboardPath(p string) string {
	if srv.Layout != LayoutData {
		return p
	}
	p = cleanSlashPath(p)
	if p == containerDataDir {
		return srv.Dir
	}
	if strings.HasPrefix(p, containerDataDir+"/") {
		return filepath.Join(srv.Dir, strings.TrimPrefix(p, containerDataDir+"/"))
	}
	return ""
}

// containerPath translates a path below Dir to the path inside the server container. Other
// values are returned unchanged.
func (srv GameServerInstance) containerPath(p string) string {
	if srv.Layout != LayoutData || srv.Dir == "" {
		return p
	}
	dir := filepath.ToSlash(srv.Dir)
	s := filepath.ToSlash(p)
	if s == dir {
		return containerDataDir
	}
	if strings.HasPrefix(s, dir+"/") {
		return containerDataDir + "/" + strings.TrimPrefix(s, dir+"/")
	}
	return p
}
