package backend

import (
	"path/filepath"
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

// configRel returns the LinuxGSM config directory of an instance (or of another script).
func (srv GameServerInstance) configRel(script string) string {
	return filepath.Join(srv.configLgsmRel(), script)
}

// lgsmDataRel is the LinuxGSM data directory (serverlist, uid files).
func (srv GameServerInstance) lgsmDataRel() string {
	if srv.Layout == LayoutData {
		return "data"
	}
	return filepath.Join("lgsm", "data")
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
