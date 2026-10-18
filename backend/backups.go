package backend

import (
	"archive/tar"
	"bufio"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/klauspost/compress/zstd"
)

// -------------------------------------------------------------
// Backup directories
// -------------------------------------------------------------

var reBackupDirSetting = regexp.MustCompile(`^\s*backupdir\s*=\s*["']?([^"']+)["']?`)

// customBackupDir reads backupdir from the LinuxGSM config (<script>.cfg -> common.cfg -> _default.cfg).
func customBackupDir(srv GameServerInstance) string {
	configDir := filepath.Join(srv.Dir, srv.configRel(srv.Script))
	for _, name := range []string{srv.Script + ".cfg", "common.cfg", "_default.cfg"} {
		data, err := os.ReadFile(filepath.Join(configDir, name))
		if err != nil {
			continue
		}
		scanner := bufio.NewScanner(bytes.NewReader(data))
		for scanner.Scan() {
			matches := reBackupDirSetting.FindStringSubmatch(scanner.Text())
			if len(matches) > 1 {
				customDir := strings.TrimSpace(matches[1])
				lgsmDir := filepath.Join(srv.Dir, "lgsm")
				replacer := strings.NewReplacer(
					"${lgsmdir}", lgsmDir, "$lgsmdir", lgsmDir,
					"${rootdir}", srv.Dir, "$rootdir", srv.Dir,
					"${username}", srv.User, "$username", srv.User,
					"${selfname}", srv.Script, "$selfname", srv.Script,
				)
				if srv.Layout == LayoutData {
					// Inside the container: the script lives in /app, the data in /data.
					replacer = strings.NewReplacer(
						"${lgsmdir}", "/app/lgsm", "$lgsmdir", "/app/lgsm",
						"${rootdir}", "/app", "$rootdir", "/app",
						"${selfname}", srv.Script, "$selfname", srv.Script,
					)
					return srv.dashboardPath(replacer.Replace(customDir))
				}
				return filepath.Clean(replacer.Replace(customDir))
			}
		}
	}
	return ""
}

// isAllowedBackupDir decides whether the dashboard (running as root) may list, read and write
// backup archives in dir. The backupdir setting is editable by gameserver users, so it must not be
// able to point at arbitrary system locations: the directory has to be inside the server
// directory or be owned by the server's Linux user.
func isAllowedBackupDir(srv GameServerInstance, dir string) bool {
	if !filepath.IsAbs(dir) {
		return false
	}
	resolved, err := resolveExisting(filepath.Clean(dir))
	if err != nil {
		return false
	}
	if root, err := filepath.EvalSymlinks(srv.Dir); err == nil && IsWithin(root, resolved) {
		return true
	}
	if IsForbiddenServerDir(filepath.ToSlash(resolved)) {
		return false
	}
	info, err := os.Stat(resolved)
	if err != nil || !info.IsDir() {
		return false
	}
	uid, err := fileOwnerUID(resolved)
	return err == nil && uid >= 0 && uid == srv.UID && srv.UID > 0
}

func (im *InstanceManager) getBackupDirsForServer(srv GameServerInstance) []string {
	dirs := []string{
		filepath.Join(srv.Dir, srv.defaultBackupRel()),
		filepath.Join(srv.Dir, "lgsm", "backup"),
		filepath.Join(srv.Dir, "lgsm", "backups"),
		filepath.Join(srv.Dir, "backup"),
		filepath.Join(srv.Dir, "backups"),
	}
	if custom := customBackupDir(srv); custom != "" {
		// Prepend to directories list so it is searched first
		dirs = append([]string{custom}, dirs...)
	}

	// Remove duplicates/forbidden locations and keep order
	seen := make(map[string]bool)
	var uniqueDirs []string
	for _, d := range dirs {
		if seen[d] || !isAllowedBackupDir(srv, d) {
			continue
		}
		seen[d] = true
		uniqueDirs = append(uniqueDirs, d)
	}
	return uniqueDirs
}

// ArchiveFormat is the compression of a backup archive. LinuxGSM picks zstd when it is
// installed, otherwise pigz/gzip, otherwise a plain tar (fn_select_compression).
type ArchiveFormat int

const (
	ArchiveTar ArchiveFormat = iota
	ArchiveTarGz
	ArchiveTarZst
)

// BackupArchiveFormat derives the archive format from a backup file name.
func BackupArchiveFormat(name string) (ArchiveFormat, bool) {
	switch {
	case strings.HasSuffix(name, ".tar.gz"):
		return ArchiveTarGz, true
	case strings.HasSuffix(name, ".tar.zst"):
		return ArchiveTarZst, true
	case strings.HasSuffix(name, ".tar"):
		return ArchiveTar, true
	}
	return 0, false
}

// ContentType returns the media type used when the archive is downloaded.
func (f ArchiveFormat) ContentType() string {
	switch f {
	case ArchiveTarGz:
		return "application/gzip"
	case ArchiveTarZst:
		return "application/zstd"
	}
	return "application/x-tar"
}

// tarFlags returns the tar options that select the decompressor for extraction.
func (f ArchiveFormat) tarFlags() []string {
	switch f {
	case ArchiveTarGz:
		return []string{"-z"}
	case ArchiveTarZst:
		return []string{"--zstd"}
	}
	return nil
}

var reBackupName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._+-]{0,200}\.tar(\.gz|\.zst)?$`)

// IsValidBackupFilename validates backup file names from requests.
func IsValidBackupFilename(name string) bool {
	return reBackupName.MatchString(name) && !strings.Contains(name, "..")
}

func (im *InstanceManager) getBackupFilePath(srv GameServerInstance, filename string) (string, error) {
	if !IsValidBackupFilename(filename) {
		return "", fmt.Errorf("invalid backup filename")
	}
	for _, dir := range im.getBackupDirsForServer(srv) {
		p := filepath.Join(dir, filename)
		info, err := os.Lstat(p)
		if err != nil {
			continue
		}
		// Never follow symlinks: a link named *.tar.gz could point at any root-only file.
		if !info.Mode().IsRegular() {
			return "", fmt.Errorf("backup file not found")
		}
		return p, nil
	}
	return "", fmt.Errorf("backup file not found")
}

func (im *InstanceManager) ListBackups(serverID string) ([]BackupFile, error) {
	srv, ok := im.getServer(serverID)
	if !ok {
		return nil, fmt.Errorf("server not found")
	}

	backups := []BackupFile{}
	for _, dir := range im.getBackupDirsForServer(srv) {
		entries, err := os.ReadDir(dir)
		if err != nil {
			continue
		}
		for _, entry := range entries {
			if !entry.Type().IsRegular() || !IsValidBackupFilename(entry.Name()) {
				continue
			}
			info, err := entry.Info()
			if err != nil {
				continue
			}
			backups = append(backups, BackupFile{
				Name: entry.Name(),
				Size: info.Size(),
				Date: info.ModTime(),
				Path: filepath.Join(dir, entry.Name()),
			})
		}
	}

	sort.Slice(backups, func(i, j int) bool {
		return backups[i].Date.After(backups[j].Date)
	})

	return backups, nil
}

func (im *InstanceManager) DeleteBackup(serverID, fileName string) error {
	srv, ok := im.getServer(serverID)
	if !ok {
		return fmt.Errorf("server not found")
	}
	filePath, err := im.getBackupFilePath(srv, fileName)
	if err != nil {
		return err
	}
	return os.Remove(filePath)
}

func (im *InstanceManager) GetBackupPath(serverID, fileName string) (string, error) {
	srv, ok := im.getServer(serverID)
	if !ok {
		return "", fmt.Errorf("server not found")
	}
	return im.getBackupFilePath(srv, fileName)
}

// UploadBackup streams a backup archive into the first existing backup directory. The caller
// limits the request body size.
func (im *InstanceManager) UploadBackup(serverID string, filename string, fileReader io.Reader) error {
	srv, ok := im.getServer(serverID)
	if !ok {
		return fmt.Errorf("server not found")
	}
	if _, ok := BackupArchiveFormat(filename); !ok {
		return fmt.Errorf("invalid backup file extension, only .tar.gz, .tar.zst and .tar are allowed")
	}
	if !IsValidBackupFilename(filename) {
		return fmt.Errorf("invalid backup filename")
	}

	dirs := im.getBackupDirsForServer(srv)
	var targetDir string
	for _, dir := range dirs {
		if info, err := os.Stat(dir); err == nil && info.IsDir() {
			targetDir = dir
			break
		}
	}
	if targetDir == "" {
		targetDir = filepath.Join(srv.Dir, srv.defaultBackupRel())
		if err := mkdirAllForServer(srv, targetDir); err != nil {
			return fmt.Errorf("failed to create backup directory")
		}
	}

	resolvedDir, err := filepath.EvalSymlinks(targetDir)
	if err != nil {
		return fmt.Errorf("backup directory not found")
	}
	target, err := ResolveInRoot(resolvedDir, filename)
	if err != nil {
		return fmt.Errorf("invalid backup filename")
	}
	return streamToResolvedFile(srv, target, fileReader)
}

// -------------------------------------------------------------
// Archive validation & restore
// -------------------------------------------------------------

// ArchiveLimits bounds what a backup archive may contain.
type ArchiveLimits struct {
	MaxEntries   int
	MaxTotalSize int64
}

// DefaultArchiveLimits can be adjusted via command line flags.
var DefaultArchiveLimits = ArchiveLimits{
	MaxEntries:   2_000_000,
	MaxTotalSize: 1 << 40, // 1 TiB uncompressed
}

// ErrUnsafeArchive is returned for archives that must not be extracted.
var ErrUnsafeArchive = errors.New("unsafe archive")

func archivePathEscapes(name string) bool {
	if name == "" || strings.ContainsRune(name, 0) {
		return true
	}
	if strings.HasPrefix(name, "/") || strings.HasPrefix(name, "\\") || filepath.VolumeName(name) != "" {
		return true
	}
	for _, part := range strings.Split(strings.ReplaceAll(name, "\\", "/"), "/") {
		if part == ".." {
			return true
		}
	}
	return false
}

// ValidateTarGz inspects a .tar.gz archive before it is extracted into destDir.
func ValidateTarGz(r io.Reader, destDir string, limits ArchiveLimits) error {
	return ValidateArchive(r, ArchiveTarGz, destDir, limits)
}

// ValidateArchive inspects a backup archive before it is extracted into destDir. It rejects
// absolute paths, traversal, device nodes, hard links/symlinks escaping the destination,
// excessive entry counts and archives whose uncompressed size exceeds the limits.
func ValidateArchive(r io.Reader, format ArchiveFormat, destDir string, limits ArchiveLimits) error {
	return validateArchive(r, format, destDir, limits, nil)
}

// validateArchive is ValidateArchive with a callback that receives the name of every accepted
// entry.
func validateArchive(r io.Reader, format ArchiveFormat, destDir string, limits ArchiveLimits, onEntry func(name string)) error {
	var src io.Reader
	switch format {
	case ArchiveTarGz:
		gz, err := gzip.NewReader(r)
		if err != nil {
			return fmt.Errorf("%w: not a gzip archive", ErrUnsafeArchive)
		}
		defer gz.Close()
		src = gz
	case ArchiveTarZst:
		// One decoder goroutine and a bounded window keep a crafted frame from exhausting memory.
		zr, err := zstd.NewReader(r, zstd.WithDecoderConcurrency(1), zstd.WithDecoderLowmem(true), zstd.WithDecoderMaxWindow(1<<30))
		if err != nil {
			return fmt.Errorf("%w: not a zstd archive", ErrUnsafeArchive)
		}
		defer zr.Close()
		src = zr
	case ArchiveTar:
		src = r
	default:
		return fmt.Errorf("%w: unknown archive format", ErrUnsafeArchive)
	}
	tr := tar.NewReader(src)

	var entries int
	var total int64
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return fmt.Errorf("%w: corrupt archive", ErrUnsafeArchive)
		}
		entries++
		if limits.MaxEntries > 0 && entries > limits.MaxEntries {
			return fmt.Errorf("%w: too many entries", ErrUnsafeArchive)
		}
		if archivePathEscapes(hdr.Name) {
			return fmt.Errorf("%w: illegal path %q", ErrUnsafeArchive, hdr.Name)
		}
		switch hdr.Typeflag {
		case tar.TypeReg, 0 /* legacy TypeRegA */, tar.TypeDir, tar.TypeXGlobalHeader, tar.TypeXHeader, tar.TypeGNUSparse:
		case tar.TypeLink:
			if archivePathEscapes(hdr.Linkname) {
				return fmt.Errorf("%w: illegal hard link %q", ErrUnsafeArchive, hdr.Name)
			}
		case tar.TypeSymlink:
			target := hdr.Linkname
			if strings.HasPrefix(target, "/") {
				if !IsWithin(filepath.Clean(destDir), filepath.Clean(target)) {
					return fmt.Errorf("%w: symlink %q points outside the server directory", ErrUnsafeArchive, hdr.Name)
				}
			} else {
				joined := path.Clean(path.Join(path.Dir(strings.TrimPrefix(hdr.Name, "./")), target))
				if joined == ".." || strings.HasPrefix(joined, "../") {
					return fmt.Errorf("%w: symlink %q points outside the server directory", ErrUnsafeArchive, hdr.Name)
				}
			}
		default:
			return fmt.Errorf("%w: unsupported entry type for %q", ErrUnsafeArchive, hdr.Name)
		}
		if hdr.Size < 0 {
			return fmt.Errorf("%w: invalid size", ErrUnsafeArchive)
		}
		if onEntry != nil {
			onEntry(hdr.Name)
		}
		total += hdr.Size
		if limits.MaxTotalSize > 0 && total > limits.MaxTotalSize {
			return fmt.Errorf("%w: uncompressed size exceeds limit", ErrUnsafeArchive)
		}
		// Read the content so the declared sizes are verified against the stream.
		if _, err := io.Copy(io.Discard, tr); err != nil {
			return fmt.Errorf("%w: corrupt archive", ErrUnsafeArchive)
		}
	}
	if free := diskFreeBytes(destDir); free >= 0 && total > free {
		return fmt.Errorf("%w: not enough free disk space", ErrUnsafeArchive)
	}
	return nil
}

func (im *InstanceManager) RestoreBackup(w http.ResponseWriter, r *http.Request, serverID, fileName, lang string) {
	im.mu.Lock()
	srvPtr := im.instances[serverID]
	if srvPtr == nil {
		im.mu.Unlock()
		http.Error(w, "Server not found", http.StatusNotFound)
		return
	}
	srv := *srvPtr
	oldStatus := srvPtr.Status
	im.mu.Unlock()

	format, ok := BackupArchiveFormat(fileName)
	if !ok {
		http.Error(w, "Unsupported backup format", http.StatusBadRequest)
		return
	}
	filePath, err := im.getBackupFilePath(srv, fileName)
	if err != nil {
		http.Error(w, "Backup file not found", http.StatusNotFound)
		return
	}

	sse, ok := newSSEWriter(w)
	if !ok {
		http.Error(w, "Streaming unsupported", http.StatusInternalServerError)
		return
	}

	updateStatus := func(newStatus string) {
		im.mu.Lock()
		defer im.mu.Unlock()
		if s, ok := im.instances[serverID]; ok {
			s.Status = newStatus
		}
	}
	updateStatus("updating")
	defer func() {
		im.ScanInstances()
	}()

	// The handler streams synchronously: writing to the ResponseWriter after the handler
	// returned (as a background goroutine did before) is not allowed.
	sse.log(Msg(lang, "Validating backup archive...", "Prüfe Backup-Archiv..."))
	f, err := OpenInRootForRead(filePath)
	if err != nil {
		updateStatus(oldStatus)
		sse.log(Msg(lang, "Error opening backup archive.", "Fehler beim Öffnen des Backup-Archivs."))
		sse.exit(1)
		return
	}
	var members containerRestoreMembers
	var onEntry func(string)
	if srv.Layout == LayoutData {
		onEntry = members.add
	}
	verr := validateArchive(f, format, srv.Dir, DefaultArchiveLimits, onEntry)
	if verr == nil && srv.Layout == LayoutData && len(members.list) == 0 {
		verr = fmt.Errorf("the archive contains no server data (serverfiles, log, lgsm/config-lgsm, lgsm/data)")
	}
	f.Close()
	if verr != nil {
		updateStatus(oldStatus)
		sse.log(Msg(lang, "Backup archive rejected: ", "Backup-Archiv abgelehnt: ") + verr.Error())
		sse.exit(1)
		return
	}
	// tar --zstd runs the zstd binary where the server lives; check for it before the server
	// is stopped.
	if format == ArchiveTarZst {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		err := im.rt.RunAsUser(ctx, srv, ExecIO{}, false, "zstd", "--version")
		cancel()
		if err != nil {
			updateStatus(oldStatus)
			sse.log(Msg(lang, "zstd is not installed, .tar.zst backups cannot be restored.", "zstd ist nicht installiert, .tar.zst-Backups können nicht wiederhergestellt werden."))
			sse.exit(1)
			return
		}
	}

	if oldStatus == "running" {
		sse.log(Msg(lang, "Stopping game server before restore...", "Stoppe Gameserver vor der Wiederherstellung..."))
		_ = im.runLGSM(context.Background(), srv, "stop")
	}

	sse.log(Msg(lang, fmt.Sprintf("Restoring backup archive %s...", fileName), fmt.Sprintf("Stelle Backup-Archiv %s wieder her...", fileName)))

	// Extract with the privileges of the server user (no shell involved on a host).
	var args []string
	if srv.Layout == LayoutData {
		args = members.restoreCommand(format, filePath)
	} else {
		args = append([]string{"tar", "--no-same-owner"}, format.tarFlags()...)
		args = append(args, "-xf", filePath, "-C", srv.Dir)
	}
	var errBuf bytes.Buffer
	if err := im.rt.RunAsUser(context.Background(), srv, ExecIO{Stderr: &errBuf}, false, args...); err != nil {
		updateStatus("stopped")
		sse.log(Msg(lang, "Error during restore: ", "Fehler bei der Wiederherstellung: ") + err.Error())
		sse.log(stripAnsi(limitString(errBuf.String(), 4000)))
		sse.exit(1)
		return
	}

	sse.log(Msg(lang, "Backup files extracted successfully.", "Backup-Dateien erfolgreich entpackt."))

	if oldStatus == "running" {
		sse.log(Msg(lang, "Restarting game server...", "Starte Gameserver neu..."))
		_ = im.runLGSM(context.Background(), srv, "start")
		updateStatus("running")
	} else {
		updateStatus("stopped")
	}

	sse.log(Msg(lang, "Restore process completed successfully.", "Wiederherstellung erfolgreich abgeschlossen."))
	sse.exit(0)
}

func limitString(s string, max int) string {
	if len(s) > max {
		return s[:max] + "..."
	}
	return s
}

// containerRestoreRoots are the parts of a backup made inside a container that hold server
// data. LinuxGSM archives its root directory (/app in the official images) with symlinks
// resolved, so the archive also contains the script, modules and node_modules of the image;
// those are never restored.
var containerRestoreRoots = []string{"serverfiles", "log", "lgsm/config-lgsm", "lgsm/data", "lgsm/mods"}

// containerRestoreMembers collects the archive names of containerRestoreRoots, including the
// "./" prefix the archive uses, so tar can be told exactly what to extract.
type containerRestoreMembers struct {
	seen map[string]bool
	list []string
}

func (m *containerRestoreMembers) add(name string) {
	rel, prefix := name, ""
	for strings.HasPrefix(rel, "./") {
		rel, prefix = rel[2:], prefix+"./"
	}
	for _, root := range containerRestoreRoots {
		if rel != root && !strings.HasPrefix(rel, root+"/") {
			continue
		}
		member := prefix + root
		if m.seen == nil {
			m.seen = make(map[string]bool)
		}
		if !m.seen[member] {
			m.seen[member] = true
			m.list = append(m.list, member)
		}
		return
	}
}

// containerRestoreScript restores a container backup. GNU tar does not extract through the
// absolute symlinks of the container (/app/serverfiles -> /data/serverfiles, log/server ->
// game log directory), so the data directories are first extracted into a staging directory
// below /data and then copied to their place in the /data layout. log/server is skipped: it is
// a copy of the game's log directory inside serverfiles.
//
// Arguments: staging directory, archive, tar decompression flag ("" for none), members.
const containerRestoreScript = `set -eu
tmp=$1 archive=$2 flag=$3
shift 3
trap 'rm -rf "$tmp"' EXIT
rm -rf "$tmp"
mkdir -p "$tmp"
if [ -n "$flag" ]; then
  tar --no-same-owner "$flag" -xf "$archive" -C "$tmp" -- "$@"
else
  tar --no-same-owner -xf "$archive" -C "$tmp" -- "$@"
fi
copy() {
  [ -d "$tmp/$1" ] || return 0
  mkdir -p "/data/$2"
  cp -a "$tmp/$1/." "/data/$2/"
}
copy serverfiles serverfiles
copy lgsm/config-lgsm config-lgsm
copy lgsm/data data
copy lgsm/mods mods
rm -rf /data/mods/tmp
if [ -d "$tmp/log" ]; then
  for d in "$tmp"/log/*; do
    n=${d##*/}
    [ "$n" = server ] && continue
    if [ -d "$d" ]; then
      copy "log/$n" "log/$n"
    elif [ -e "$d" ]; then
      mkdir -p /data/log
      cp -a "$d" /data/log/
    fi
  done
fi
`

// restoreCommand returns the command that restores archive (a path the dashboard sees) into
// the /data volume of a container.
func (m *containerRestoreMembers) restoreCommand(format ArchiveFormat, archive string) []string {
	flag := ""
	if f := format.tarFlags(); len(f) > 0 {
		flag = f[0]
	}
	staging := containerDataDir + "/.lgsm-dashboard-restore"
	args := []string{"sh", "-c", containerRestoreScript, "restore", staging, archive, flag}
	return append(args, m.list...)
}

// -------------------------------------------------------------
// Cloud Backup (rclone) Integration
// -------------------------------------------------------------

type CloudBackupSettings struct {
	Enabled  bool   `json:"enabled"`
	Remote   string `json:"remote"`
	Path     string `json:"path"`
	AutoSync bool   `json:"auto_sync"`
}

var (
	reRcloneRemote = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9_. -]{0,63}$`)
	reRclonePath   = regexp.MustCompile(`^[A-Za-z0-9_./@+-]{0,255}$`)
)

// ValidateCloudBackupSettings rejects remote names/paths that could inject rclone options.
func ValidateCloudBackupSettings(s CloudBackupSettings) error {
	if s.Remote != "" && (!reRcloneRemote.MatchString(s.Remote) || strings.HasSuffix(s.Remote, " ")) {
		return fmt.Errorf("invalid rclone remote name")
	}
	if !reRclonePath.MatchString(s.Path) || strings.HasPrefix(s.Path, "-") || strings.Contains(s.Path, "..") {
		return fmt.Errorf("invalid remote path")
	}
	if s.Enabled && s.Remote == "" {
		return fmt.Errorf("remote is required")
	}
	return nil
}

func cloudConfigRel(srv GameServerInstance) string {
	return filepath.Join(srv.configRel(srv.Script), "cloud_backup.json")
}

func (im *InstanceManager) GetCloudBackupSettings(serverID string) (CloudBackupSettings, error) {
	srv, ok := im.getServer(serverID)
	if !ok {
		return CloudBackupSettings{}, fmt.Errorf("server not found")
	}

	defaults := CloudBackupSettings{Enabled: false, Remote: "", Path: "backups", AutoSync: false}
	resolved, err := ResolveInRoot(srv.Dir, cloudConfigRel(srv))
	if err != nil {
		return defaults, nil
	}
	data, err := readResolvedFileLimited(resolved, 64*1024)
	if err != nil {
		return defaults, nil
	}

	var settings CloudBackupSettings
	if err := json.Unmarshal(data, &settings); err != nil || ValidateCloudBackupSettings(settings) != nil {
		return defaults, nil
	}
	return settings, nil
}

func (im *InstanceManager) SaveCloudBackupSettings(serverID string, settings CloudBackupSettings) error {
	srv, ok := im.getServer(serverID)
	if !ok {
		return fmt.Errorf("server not found")
	}
	if err := ValidateCloudBackupSettings(settings); err != nil {
		return err
	}
	data, err := json.MarshalIndent(settings, "", "  ")
	if err != nil {
		return err
	}
	return writeServerFile(srv, cloudConfigRel(srv), data)
}

func (im *InstanceManager) SyncCloudBackup(w http.ResponseWriter, r *http.Request, serverID, lang string) {
	srv, ok := im.getServer(serverID)
	if !ok {
		http.Error(w, "Server not found", http.StatusNotFound)
		return
	}

	settings, err := im.GetCloudBackupSettings(serverID)
	if err != nil || !settings.Enabled || settings.Remote == "" {
		http.Error(w, "Cloud backup not configured or disabled", http.StatusBadRequest)
		return
	}

	backupDir := ""
	for _, d := range im.getBackupDirsForServer(srv) {
		if info, err := os.Stat(d); err == nil && info.IsDir() {
			backupDir = d
			break
		}
	}
	if backupDir == "" {
		http.Error(w, "No backup directory found", http.StatusBadRequest)
		return
	}

	sse, ok := newSSEWriter(w)
	if !ok {
		http.Error(w, "Streaming unsupported", http.StatusInternalServerError)
		return
	}

	sse.log(Msg(lang, fmt.Sprintf("Starting cloud backup sync to %s:%s...", settings.Remote, settings.Path), fmt.Sprintf("Starte Cloud-Backup-Synchronisierung nach %s:%s...", settings.Remote, settings.Path)))

	// Login shell so the user's own rclone configuration (~/.config/rclone) is used.
	targetDest := fmt.Sprintf("%s:%s", settings.Remote, settings.Path)
	err = streamLines(func(out io.Writer) error {
		return im.rt.RunAsUser(r.Context(), srv, ExecIO{Stdout: out, Stderr: out}, true, "rclone", "copy", "-v", "--", backupDir, targetDest)
	}, func(line string) {
		sse.log(strings.TrimRight(line, "\r\n"))
	}, nil)
	if isStartError(err) {
		sse.log(Msg(lang, "Error starting rclone (is rclone installed?): ", "Fehler beim Starten von rclone (ist rclone installiert?): ") + err.Error())
		sse.exit(1)
		return
	}
	if err != nil {
		sse.log(Msg(lang, "Sync finished with error: ", "Synchronisierung mit Fehler beendet: ") + err.Error())
		sse.exit(1)
		return
	}

	sse.log(Msg(lang, "Cloud backup sync completed successfully!", "Cloud-Backup-Synchronisierung erfolgreich abgeschlossen!"))
	sse.exit(0)
}
