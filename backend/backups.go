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
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// -------------------------------------------------------------
// Backup directories
// -------------------------------------------------------------

var reBackupDirSetting = regexp.MustCompile(`^\s*backupdir\s*=\s*["']?([^"']+)["']?`)

// customBackupDir reads backupdir from the LinuxGSM config (<script>.cfg -> common.cfg -> _default.cfg).
func customBackupDir(srv GameServerInstance) string {
	configDir := filepath.Join(srv.Dir, "lgsm", "config-lgsm", srv.Script)
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
				return filepath.Clean(replacer.Replace(customDir))
			}
		}
	}
	return ""
}

// isAllowedBackupDir decides whether the dashboard (running as root) may list, read and write
// .tar.gz files in dir. The backupdir setting is editable by gameserver users, so it must not be
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

var reBackupName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._+-]{0,200}\.tar\.gz$`)

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
	if !strings.HasSuffix(filename, ".tar.gz") {
		return fmt.Errorf("invalid backup file extension, only .tar.gz is allowed")
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
		targetDir = filepath.Join(srv.Dir, "lgsm", "backup")
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

// ValidateTarGz inspects a .tar.gz archive before it is extracted into destDir. It rejects
// absolute paths, traversal, device nodes, hard links/symlinks escaping the destination,
// excessive entry counts and archives whose uncompressed size exceeds the limits.
func ValidateTarGz(r io.Reader, destDir string, limits ArchiveLimits) error {
	gz, err := gzip.NewReader(r)
	if err != nil {
		return fmt.Errorf("%w: not a gzip archive", ErrUnsafeArchive)
	}
	defer gz.Close()
	tr := tar.NewReader(gz)

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
	verr := ValidateTarGz(f, srv.Dir, DefaultArchiveLimits)
	f.Close()
	if verr != nil {
		updateStatus(oldStatus)
		sse.log(Msg(lang, "Backup archive rejected: ", "Backup-Archiv abgelehnt: ") + verr.Error())
		sse.exit(1)
		return
	}

	if oldStatus == "running" {
		sse.log(Msg(lang, "Stopping game server before restore...", "Stoppe Gameserver vor der Wiederherstellung..."))
		_ = lgsmCommand(context.Background(), srv, "stop").Run()
	}

	sse.log(Msg(lang, fmt.Sprintf("Restoring backup archive %s...", fileName), fmt.Sprintf("Stelle Backup-Archiv %s wieder her...", fileName)))

	// Extract with the privileges of the server user (no shell involved).
	cmd := exec.Command("runuser", "-u", srv.User, "--", "tar", "--no-same-owner", "-xzf", filePath, "-C", srv.Dir)
	var errBuf bytes.Buffer
	cmd.Stdout = io.Discard
	cmd.Stderr = &errBuf

	if err := cmd.Run(); err != nil {
		updateStatus("stopped")
		sse.log(Msg(lang, "Error during restore: ", "Fehler bei der Wiederherstellung: ") + err.Error())
		sse.log(stripAnsi(limitString(errBuf.String(), 4000)))
		sse.exit(1)
		return
	}

	sse.log(Msg(lang, "Backup files extracted successfully.", "Backup-Dateien erfolgreich entpackt."))

	if oldStatus == "running" {
		sse.log(Msg(lang, "Restarting game server...", "Starte Gameserver neu..."))
		_ = lgsmCommand(context.Background(), srv, "start").Run()
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
	return filepath.Join("lgsm", "config-lgsm", srv.Script, "cloud_backup.json")
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
	rcloneCmd := "rclone copy -v -- " + ShellQuote(backupDir) + " " + ShellQuote(targetDest)
	cmd := exec.CommandContext(r.Context(), "runuser", "-l", srv.User, "-c", rcloneCmd)

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		sse.log("Error creating stdout pipe")
		sse.exit(1)
		return
	}
	cmd.Stderr = cmd.Stdout

	if err := cmd.Start(); err != nil {
		sse.log(Msg(lang, "Error starting rclone (is rclone installed?): ", "Fehler beim Starten von rclone (ist rclone installiert?): ") + err.Error())
		sse.exit(1)
		return
	}

	reader := bufio.NewReader(stdout)
	for {
		line, err := reader.ReadString('\n')
		if line != "" {
			sse.log(strings.TrimRight(line, "\r\n"))
		}
		if err != nil {
			break
		}
	}

	if err := cmd.Wait(); err != nil {
		sse.log(Msg(lang, "Sync finished with error: ", "Synchronisierung mit Fehler beendet: ") + err.Error())
		sse.exit(1)
		return
	}

	sse.log(Msg(lang, "Cloud backup sync completed successfully!", "Cloud-Backup-Synchronisierung erfolgreich abgeschlossen!"))
	sse.exit(0)
}
