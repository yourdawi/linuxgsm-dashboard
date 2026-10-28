package backend

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// Size limits for files that are loaded into memory.
const (
	MaxEditableFileSize = 5 << 20 // config/file editor
)

// ErrFileTooLarge is returned when a file exceeds MaxEditableFileSize.
var ErrFileTooLarge = errors.New("file is too large to be edited in the browser")

// chownToServer hands a file created by root over to the server's Linux user (never follows symlinks).
func chownToServer(path string, srv GameServerInstance) {
	if srv.UID > 0 {
		_ = os.Lchown(path, srv.UID, srv.GID)
	}
}

// writeServerFile writes data to rel (relative to the server root) as root without ever following
// symlinks out of the server directory. Missing parent directories are created and handed to the
// server user.
func writeServerFile(srv GameServerInstance, rel string, data []byte) error {
	target, err := ResolveInRoot(srv.Dir, rel)
	if err != nil {
		return err
	}
	if err := mkdirAllForServer(srv, filepath.Dir(target)); err != nil {
		return err
	}
	// Resolve again: the parent now exists, so symlinks created meanwhile are detected.
	target, err = ResolveInRoot(srv.Dir, rel)
	if err != nil {
		return err
	}
	return writeResolvedFile(srv, target, data, 0644)
}

// writeResolvedFile writes data via a temporary file + rename so a symlink planted at target is
// replaced instead of followed.
func writeResolvedFile(srv GameServerInstance, target string, data []byte, perm os.FileMode) error {
	if info, err := os.Lstat(target); err == nil {
		if info.IsDir() {
			return fmt.Errorf("target is a directory")
		}
		perm = info.Mode().Perm()
	}
	tmp, err := os.CreateTemp(filepath.Dir(target), ".lgsm-dashboard-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		os.Remove(tmpName)
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpName)
		return err
	}
	_ = os.Chmod(tmpName, perm)
	chownToServer(tmpName, srv)
	if err := os.Rename(tmpName, target); err != nil {
		os.Remove(tmpName)
		return err
	}
	return nil
}

// mkdirAllForServer creates dir (which must be inside the server directory) and chowns the newly
// created directories to the server user.
func mkdirAllForServer(srv GameServerInstance, dir string) error {
	root, err := filepath.EvalSymlinks(srv.Dir)
	if err != nil {
		return err
	}
	resolved, err := resolveExisting(dir)
	if err != nil {
		return err
	}
	if !IsWithin(root, resolved) {
		return ErrPathOutsideRoot
	}
	var missing []string
	for p := resolved; p != root && IsWithin(root, p); p = filepath.Dir(p) {
		if _, err := os.Lstat(p); err == nil {
			break
		}
		missing = append(missing, p)
	}
	if err := os.MkdirAll(resolved, 0755); err != nil {
		return err
	}
	for _, p := range missing {
		chownToServer(p, srv)
	}
	return nil
}

// readResolvedFileLimited reads an already resolved file with a size limit.
func readResolvedFileLimited(resolved string, limit int64) ([]byte, error) {
	f, err := OpenInRootForRead(resolved)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("not a regular file")
	}
	if info.Size() > limit {
		return nil, ErrFileTooLarge
	}
	return io.ReadAll(io.LimitReader(f, limit+1))
}

// -------------------------------------------------------------
// Config editor
// -------------------------------------------------------------

func configDirsRel(srv GameServerInstance) (string, string) {
	rootScript := strings.Split(srv.Script, "-")[0]
	return srv.configRel(srv.Script), srv.configRel(rootScript)
}

// configRoots returns the directories the config editor may access.
func configRoots(srv GameServerInstance) []string {
	instRel, rootRel := configDirsRel(srv)
	var roots []string
	for _, rel := range []string{instRel, rootRel, "serverfiles"} {
		if r, err := ServerSubRoot(srv.Dir, rel, srv.UID); err == nil {
			roots = append(roots, r)
		}
	}
	return roots
}

var editableConfigExt = map[string]bool{
	".cfg": true, ".ini": true, ".properties": true, ".conf": true, ".json": true,
	".yml": true, ".yaml": true, ".xml": true, ".txt": true, ".toml": true,
}

// IsEditableConfigFile reports whether the config editor may open a file with this name.
func IsEditableConfigFile(name string) bool {
	return editableConfigExt[strings.ToLower(filepath.Ext(name))]
}

func (im *InstanceManager) GetConfigFiles(serverID string) ([]ConfigFile, error) {
	srv, ok := im.getServer(serverID)
	if !ok {
		return nil, fmt.Errorf("server not found")
	}

	var configs []ConfigFile
	seenPaths := make(map[string]bool)

	addConfig := func(name, path, layer string) {
		clean := filepath.Clean(path)
		if seenPaths[clean] {
			return
		}
		seenPaths[clean] = true
		configs = append(configs, ConfigFile{
			Name:  name,
			Path:  clean,
			Layer: layer,
		})
	}

	instRel, rootRel := configDirsRel(srv)

	// 1. Scan primary LinuxGSM config directory for this specific instance: <dir>/lgsm/config-lgsm/<script>/
	configDir := filepath.Join(srv.Dir, instRel)
	if files, err := os.ReadDir(configDir); err == nil {
		for _, f := range files {
			if f.Type().IsRegular() && strings.HasSuffix(f.Name(), ".cfg") {
				layer := "instance"
				if f.Name() == "_default.cfg" {
					layer = "default"
				} else if f.Name() == "common.cfg" {
					layer = "common"
				} else if strings.HasPrefix(f.Name(), "secrets-") {
					layer = "secrets"
				}
				addConfig(f.Name(), filepath.Join(configDir, f.Name()), layer)
			}
		}
	}

	// 2. If this is a multi-instance (e.g. mtaserver-2), also scan the root config directory
	if rootRel != instRel {
		rootConfigDir := filepath.Join(srv.Dir, rootRel)
		if files, err := os.ReadDir(rootConfigDir); err == nil {
			for _, f := range files {
				if f.Type().IsRegular() && strings.HasSuffix(f.Name(), ".cfg") {
					if f.Name() == "_default.cfg" {
						addConfig(f.Name(), filepath.Join(rootConfigDir, f.Name()), "default")
					} else if f.Name() == "common.cfg" {
						addConfig(f.Name(), filepath.Join(rootConfigDir, f.Name()), "common")
					} else if f.Name() == fmt.Sprintf("%s.cfg", srv.Script) {
						addConfig(f.Name(), filepath.Join(rootConfigDir, f.Name()), "instance")
					} else if strings.HasPrefix(f.Name(), fmt.Sprintf("secrets-%s", srv.Script)) {
						addConfig(f.Name(), filepath.Join(rootConfigDir, f.Name()), "secrets")
					}
				}
			}
		}
	}

	// 3. Scan gameserver files directory recursively: <dir>/serverfiles/
	serverfilesDir := filepath.Join(srv.Dir, "serverfiles")
	if _, err := ServerSubRoot(srv.Dir, "serverfiles", srv.UID); err == nil {
		found := findConfigsInDir(srv.Dir, serverfilesDir, 0)
		for _, cfg := range found {
			addConfig(cfg.Name, cfg.Path, "instance")
		}
	}

	return configs, nil
}

// resolveConfigPath validates an absolute path from the config file list.
func resolveConfigPath(srv GameServerInstance, path string) (string, error) {
	if !IsEditableConfigFile(path) {
		return "", fmt.Errorf("access denied: file type not editable in config editor")
	}
	resolved, err := ResolveAbsInRoots(path, configRoots(srv)...)
	if err != nil {
		return "", fmt.Errorf("access denied: path outside allowed config directories")
	}
	if !IsEditableConfigFile(resolved) {
		return "", fmt.Errorf("access denied: file type not editable in config editor")
	}
	return resolved, nil
}

func (im *InstanceManager) GetConfigFileContent(serverID, path string) (string, error) {
	srv, ok := im.getServer(serverID)
	if !ok {
		return "", fmt.Errorf("server not found")
	}
	resolved, err := resolveConfigPath(srv, path)
	if err != nil {
		return "", err
	}
	content, err := readResolvedFileLimited(resolved, MaxEditableFileSize)
	if err != nil {
		if errors.Is(err, ErrFileTooLarge) {
			return "", err
		}
		return "", fmt.Errorf("failed to read config file")
	}
	return string(content), nil
}

// ConfigSaveResult tells the editor where a save ended up. RedirectedTo is set when changes to
// _default.cfg were written to the instance config instead.
type ConfigSaveResult struct {
	RedirectedTo string   `json:"redirected_to,omitempty"`
	Keys         []string `json:"keys,omitempty"`
}

func (im *InstanceManager) SaveConfigFileContent(serverID, path, content string) (ConfigSaveResult, error) {
	srv, ok := im.getServer(serverID)
	if !ok {
		return ConfigSaveResult{}, fmt.Errorf("server not found")
	}
	resolved, err := resolveConfigPath(srv, path)
	if err != nil {
		return ConfigSaveResult{}, err
	}
	// Only existing config files can be edited; this endpoint never creates new files.
	info, err := os.Lstat(resolved)
	if err != nil || !info.Mode().IsRegular() {
		return ConfigSaveResult{}, fmt.Errorf("config file not found")
	}
	if filepath.Base(resolved) == "_default.cfg" {
		return im.saveDefaultCfgOverrides(srv, resolved, filepath.Clean(path), content)
	}
	if err := writeResolvedFile(srv, resolved, []byte(content), info.Mode().Perm()); err != nil {
		return ConfigSaveResult{}, fmt.Errorf("failed to save config file")
	}
	go im.RefreshServerDetailsForce(serverID)
	return ConfigSaveResult{}, nil
}

// saveDefaultCfgOverrides writes the values changed in _default.cfg to <script>.cfg next to it.
// linuxgsm.sh restores _default.cfg from config-default on every run, so edits there are lost.
// listedPath is the path as the config file list shows it, so the editor can select the target.
func (im *InstanceManager) saveDefaultCfgOverrides(srv GameServerInstance, defaultPath, listedPath, content string) (ConfigSaveResult, error) {
	current, err := readResolvedFileLimited(defaultPath, MaxEditableFileSize)
	if err != nil {
		return ConfigSaveResult{}, fmt.Errorf("failed to read config file")
	}
	oldValues, _ := cfgRawValues(string(current))
	newValues, order := cfgRawValues(content)
	changed := make(map[string]string)
	var keys []string
	for _, key := range order {
		if old, ok := oldValues[key]; !ok || old != newValues[key] {
			changed[key] = newValues[key]
			keys = append(keys, key)
		}
	}
	instanceName := srv.Script + ".cfg"
	result := ConfigSaveResult{RedirectedTo: filepath.Join(filepath.Dir(listedPath), instanceName), Keys: keys}
	if len(keys) == 0 {
		return result, nil
	}
	target, err := resolveConfigPath(srv, filepath.Join(filepath.Dir(defaultPath), instanceName))
	if err != nil {
		return ConfigSaveResult{}, err
	}
	instContent := ""
	if data, err := readResolvedFileLimited(target, MaxEditableFileSize); err == nil {
		instContent = string(data)
	} else if _, statErr := os.Lstat(target); !os.IsNotExist(statErr) {
		return ConfigSaveResult{}, fmt.Errorf("failed to read %s", instanceName)
	}
	if err := writeResolvedFile(srv, target, []byte(setCfgRawValues(instContent, changed, keys)), 0644); err != nil {
		return ConfigSaveResult{}, fmt.Errorf("failed to save %s", instanceName)
	}
	go im.RefreshServerDetailsForce(srv.ID)
	return result, nil
}

var reCfgAssignment = regexp.MustCompile(`^\s*([A-Za-z_][A-Za-z0-9_]*)=(.*)$`)

// cfgRawValues returns the right-hand side (quotes included) of every key=value line, the last
// assignment of a key winning like in bash, and the keys in the order they first appear.
func cfgRawValues(content string) (map[string]string, []string) {
	values := make(map[string]string)
	var order []string
	for _, line := range strings.Split(content, "\n") {
		m := reCfgAssignment.FindStringSubmatch(strings.TrimRight(line, "\r"))
		if m == nil {
			continue
		}
		if _, seen := values[m[1]]; !seen {
			order = append(order, m[1])
		}
		values[m[1]] = strings.TrimSpace(m[2])
	}
	return values, order
}

// setCfgRawValues is setCfgValues for values taken verbatim from a config line.
func setCfgRawValues(content string, values map[string]string, order []string) string {
	lines := strings.Split(content, "\n")
	updated := make(map[string]bool)
	for i, line := range lines {
		m := reCfgAssignment.FindStringSubmatch(strings.TrimRight(line, "\r"))
		if m == nil {
			continue
		}
		if val, ok := values[m[1]]; ok {
			lines[i] = m[1] + "=" + val
			updated[m[1]] = true
		}
	}
	if len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	for _, key := range order {
		if !updated[key] {
			lines = append(lines, key+"="+values[key])
		}
	}
	return strings.Join(lines, "\n") + "\n"
}

// -------------------------------------------------------------
// File manager (serverfiles)
// -------------------------------------------------------------

func serverfilesRoot(srv GameServerInstance) (string, error) {
	return ServerSubRoot(srv.Dir, "serverfiles", srv.UID)
}

func (im *InstanceManager) GetServerFiles(serverID string, subPath string) ([]FileNode, error) {
	srv, ok := im.getServer(serverID)
	if !ok {
		return nil, fmt.Errorf("server not found")
	}
	baseDir, err := serverfilesRoot(srv)
	if err != nil {
		return []FileNode{}, nil
	}
	targetDir, err := ResolveInRoot(baseDir, subPath)
	if err != nil {
		return nil, fmt.Errorf("invalid path")
	}

	entries, err := os.ReadDir(targetDir)
	if err != nil {
		return []FileNode{}, nil
	}

	nodes := []FileNode{}
	for _, entry := range entries {
		info, err := entry.Info()
		if err != nil {
			continue
		}

		relPath, _ := filepath.Rel(baseDir, filepath.Join(targetDir, entry.Name()))
		relPath = filepath.ToSlash(relPath)

		nodes = append(nodes, FileNode{
			Name:    entry.Name(),
			Path:    relPath,
			IsDir:   entry.IsDir(),
			Size:    info.Size(),
			ModTime: info.ModTime().Format("2006-01-02 15:04:05"),
		})
	}

	return nodes, nil
}

// OpenServerFile opens a regular file inside serverfiles for streaming (download).
func (im *InstanceManager) OpenServerFile(serverID, relPath string) (*os.File, os.FileInfo, error) {
	srv, ok := im.getServer(serverID)
	if !ok {
		return nil, nil, fmt.Errorf("server not found")
	}
	baseDir, err := serverfilesRoot(srv)
	if err != nil {
		return nil, nil, fmt.Errorf("file not found")
	}
	target, err := ResolveInRoot(baseDir, relPath)
	if err != nil {
		return nil, nil, fmt.Errorf("invalid path")
	}
	f, err := OpenInRootForRead(target)
	if err != nil {
		return nil, nil, fmt.Errorf("file not found")
	}
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() {
		f.Close()
		return nil, nil, fmt.Errorf("file not found")
	}
	return f, info, nil
}

// GetServerFileContent returns a (small) file for the in-browser editor.
func (im *InstanceManager) GetServerFileContent(serverID string, relPath string) (string, error) {
	f, info, err := im.OpenServerFile(serverID, relPath)
	if err != nil {
		return "", err
	}
	defer f.Close()
	if info.Size() > MaxEditableFileSize {
		return "", ErrFileTooLarge
	}
	content, err := io.ReadAll(io.LimitReader(f, MaxEditableFileSize))
	if err != nil {
		return "", fmt.Errorf("failed to read file")
	}
	return string(content), nil
}

func (im *InstanceManager) SaveServerFileContent(serverID string, relPath string, content string) error {
	srv, ok := im.getServer(serverID)
	if !ok {
		return fmt.Errorf("server not found")
	}
	baseDir, err := serverfilesRoot(srv)
	if err != nil {
		return fmt.Errorf("serverfiles directory not found")
	}
	target, err := ResolveInRoot(baseDir, relPath)
	if err != nil || target == baseDir {
		return fmt.Errorf("invalid path")
	}
	if info, err := os.Stat(filepath.Dir(target)); err != nil || !info.IsDir() {
		return fmt.Errorf("parent directory not found")
	}
	if err := writeResolvedFile(srv, target, []byte(content), 0644); err != nil {
		return fmt.Errorf("failed to save file")
	}
	return nil
}

// IsValidUploadFilename rejects names that could change directories or hide files.
func IsValidUploadFilename(name string) bool {
	if name == "" || len(name) > 255 || name == "." || name == ".." {
		return false
	}
	return !strings.ContainsAny(name, "/\\\x00") && !strings.ContainsAny(name, "\r\n")
}

// UploadServerFile streams an upload into serverfiles/subPath. The caller limits the body size.
func (im *InstanceManager) UploadServerFile(serverID string, subPath string, filename string, data io.Reader) error {
	srv, ok := im.getServer(serverID)
	if !ok {
		return fmt.Errorf("server not found")
	}
	filename = filepath.Base(strings.ReplaceAll(filename, "\\", "/"))
	if !IsValidUploadFilename(filename) {
		return fmt.Errorf("invalid filename")
	}
	baseDir, err := serverfilesRoot(srv)
	if err != nil {
		return fmt.Errorf("serverfiles directory not found")
	}
	targetDir, err := ResolveInRoot(baseDir, subPath)
	if err != nil {
		return fmt.Errorf("invalid path")
	}
	if info, err := os.Stat(targetDir); err != nil || !info.IsDir() {
		return fmt.Errorf("target directory not found")
	}
	target, err := ResolveInRoot(baseDir, filepath.Join(subPath, filename))
	if err != nil {
		return fmt.Errorf("invalid path")
	}
	return streamToResolvedFile(srv, target, data)
}

// streamToResolvedFile copies r into a temp file next to target and renames it into place.
func streamToResolvedFile(srv GameServerInstance, target string, r io.Reader) error {
	if info, err := os.Lstat(target); err == nil && info.IsDir() {
		return fmt.Errorf("target is a directory")
	}
	tmp, err := os.CreateTemp(filepath.Dir(target), ".lgsm-upload-*")
	if err != nil {
		return fmt.Errorf("failed to create file")
	}
	tmpName := tmp.Name()
	if _, err := io.Copy(tmp, r); err != nil {
		tmp.Close()
		os.Remove(tmpName)
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpName)
		return err
	}
	_ = os.Chmod(tmpName, 0644)
	chownToServer(tmpName, srv)
	if err := os.Rename(tmpName, target); err != nil {
		os.Remove(tmpName)
		return fmt.Errorf("failed to store file")
	}
	return nil
}

// -------------------------------------------------------------
// FastDL & mods
// -------------------------------------------------------------

// ResolveFastDLFile maps a public FastDL request to <dir>/public_html/fastdl/<rel>. Traversal,
// sibling-prefix tricks and symlinks escaping the FastDL root are rejected.
func (im *InstanceManager) ResolveFastDLFile(serverID, rel string) (string, error) {
	srv, ok := im.getServer(serverID)
	if !ok {
		return "", ErrServerNotFound
	}
	root, err := ServerSubRoot(srv.Dir, filepath.Join("public_html", "fastdl"), srv.UID)
	if err != nil {
		return "", os.ErrNotExist
	}
	target, err := ResolveInRoot(root, rel)
	if err != nil {
		return "", err
	}
	info, err := os.Stat(target)
	if err != nil || !info.Mode().IsRegular() {
		return "", os.ErrNotExist
	}
	return target, nil
}

// GetInstalledMods reads lgsm/mods/installed-mods.txt of a server.
func (im *InstanceManager) GetInstalledMods(serverID string) ([]string, error) {
	srv, ok := im.getServer(serverID)
	if !ok {
		return nil, ErrServerNotFound
	}
	installedMods := []string{}
	resolved, err := ResolveInRoot(srv.Dir, filepath.Join(srv.modsRel(), "installed-mods.txt"))
	if err != nil {
		return installedMods, nil
	}
	data, err := readResolvedFileLimited(resolved, 1<<20)
	if err != nil {
		return installedMods, nil
	}
	scanner := bufio.NewScanner(strings.NewReader(string(data)))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line != "" && !strings.HasPrefix(line, "#") {
			installedMods = append(installedMods, line)
		}
	}
	return installedMods, nil
}

// -------------------------------------------------------------
// Server tags
// -------------------------------------------------------------

const serverTagsFileName = "server_tags.json"

func (im *InstanceManager) tagsPath() string {
	return filepath.Join(im.configDir, serverTagsFileName)
}

func (im *InstanceManager) loadServerTags() map[string]string {
	tags := make(map[string]string)
	data, err := os.ReadFile(im.tagsPath())
	if err != nil {
		// Older versions stored the tags relative to the working directory.
		data, err = os.ReadFile(serverTagsFileName)
		if err != nil {
			return tags
		}
	}
	_ = json.Unmarshal(data, &tags)
	return tags
}

func (im *InstanceManager) saveServerTags(tags map[string]string) error {
	data, err := json.MarshalIndent(tags, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(im.configDir, 0700); err != nil {
		return err
	}
	return WriteFileAtomic(im.tagsPath(), data, 0600)
}

func (im *InstanceManager) SetServerTag(serverID string, tag string) error {
	tag = sanitizeDisplayText(tag, 64)

	im.mu.Lock()
	defer im.mu.Unlock()

	srv, ok := im.instances[serverID]
	if !ok {
		return fmt.Errorf("server not found")
	}

	srv.Tag = tag
	tags := im.loadServerTags()
	if srv.Tag == "" {
		delete(tags, serverID)
	} else {
		tags[serverID] = srv.Tag
	}
	return im.saveServerTags(tags)
}

// -------------------------------------------------------------
// Custom server directories
// -------------------------------------------------------------

const customPathsFileName = "server_paths.json"

// MaxCustomServerPaths bounds the number of additional directories.
const MaxCustomServerPaths = 50

func (im *InstanceManager) customPathsFile() string {
	return filepath.Join(im.configDir, customPathsFileName)
}

func (im *InstanceManager) loadCustomPaths() []string {
	var cfg struct {
		Paths []string `json:"paths"`
	}
	data, err := os.ReadFile(im.customPathsFile())
	if err != nil {
		return []string{}
	}
	if err := json.Unmarshal(data, &cfg); err != nil {
		fmt.Printf("[WARNING] Ignoring invalid %s: %v\n", customPathsFileName, err)
		return []string{}
	}
	valid := []string{}
	for _, p := range cfg.Paths {
		if IsSafeServerDir(p) {
			valid = append(valid, p)
		}
	}
	return valid
}

// GetCustomServerPaths returns the additional server directories configured by an administrator.
func (im *InstanceManager) GetCustomServerPaths() []string {
	im.pathsMu.RLock()
	defer im.pathsMu.RUnlock()
	out := make([]string, len(im.customPaths))
	copy(out, im.customPaths)
	return out
}

// ValidateCustomServerPath checks a directory before it is added to the discovery list.
func ValidateCustomServerPath(p string) (string, error) {
	p = strings.TrimSpace(p)
	if len(p) > 1 {
		p = strings.TrimRight(p, "/")
	}
	if !IsSafeServerDir(p) {
		return "", fmt.Errorf("invalid directory %q: use an absolute path (letters, digits, / . _ - + @) outside of system directories", p)
	}
	info, err := os.Stat(p)
	if err != nil || !info.IsDir() {
		return "", fmt.Errorf("directory %q does not exist", p)
	}
	if resolved, err := filepath.EvalSymlinks(p); err == nil && IsForbiddenServerDir(filepath.ToSlash(resolved)) {
		return "", fmt.Errorf("directory %q points to a system directory", p)
	}
	return p, nil
}

// SetCustomServerPaths validates, persists and applies the list of additional server directories.
func (im *InstanceManager) SetCustomServerPaths(paths []string) ([]string, error) {
	if len(paths) > MaxCustomServerPaths {
		return nil, fmt.Errorf("too many directories (max %d)", MaxCustomServerPaths)
	}
	seen := make(map[string]bool)
	clean := []string{}
	for _, p := range paths {
		v, err := ValidateCustomServerPath(p)
		if err != nil {
			return nil, err
		}
		if !seen[v] {
			seen[v] = true
			clean = append(clean, v)
		}
	}
	sort.Strings(clean)

	data, err := json.MarshalIndent(map[string][]string{"paths": clean}, "", "  ")
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(im.configDir, 0700); err != nil {
		return nil, err
	}
	if err := WriteFileAtomic(im.customPathsFile(), data, 0600); err != nil {
		return nil, err
	}

	im.pathsMu.Lock()
	im.customPaths = clean
	im.pathsMu.Unlock()

	im.ScanInstances()
	return clean, nil
}
