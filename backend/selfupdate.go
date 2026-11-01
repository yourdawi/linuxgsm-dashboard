package backend

// Self-update from signed GitHub release binaries.
//
// Release assets (built by .github/workflows/release.yml):
//   lgsm-dashboard-linux-amd64, lgsm-dashboard-linux-arm64   the binaries
//   SHA256SUMS                                                "<sha256>  <file name>" per line
//   SHA256SUMS.sig                                            base64 Ed25519 signature of SHA256SUMS
//
// An update is only installed when
//   1. it is the latest published, non-prerelease release of the configured repository,
//   2. the tagged commit is contained in the main branch (GitHub compare API),
//   3. SHA256SUMS carries a valid signature of the release key embedded in the binary,
//   4. the downloaded binary matches its checksum, and
//   5. the new binary starts and reports the expected version.

import (
	"bufio"
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"
)

// ReleaseBranch is the only branch releases may come from.
const ReleaseBranch = "main"

const (
	maxBinarySize    = 200 << 20
	maxChecksumsSize = 64 << 10
)

var reStableTag = regexp.MustCompile(`^v[0-9]{1,4}\.[0-9]{1,4}\.[0-9]{1,4}$`)

// IsStableReleaseTag accepts tags like v1.2.3 (no pre-release or build suffixes).
func IsStableReleaseTag(tag string) bool {
	return reStableTag.MatchString(tag)
}

// ParseVersion parses "1.2.3" or "v1.2.3".
func ParseVersion(v string) ([3]int, bool) {
	var out [3]int
	parts := strings.Split(strings.TrimPrefix(strings.TrimSpace(v), "v"), ".")
	if len(parts) != 3 {
		return out, false
	}
	for i, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil || n < 0 {
			return out, false
		}
		out[i] = n
	}
	return out, true
}

// IsNewerVersion reports whether latest is newer than current. Unparsable current versions
// (e.g. "dev" for source builds) are treated as older than any release.
func IsNewerVersion(latest, current string) bool {
	l, ok := ParseVersion(latest)
	if !ok {
		return false
	}
	c, ok := ParseVersion(current)
	if !ok {
		return true
	}
	for i := 0; i < 3; i++ {
		if l[i] != c[i] {
			return l[i] > c[i]
		}
	}
	return false
}

// IsDevelopmentBuild reports whether the version belongs to a build from source made by the
// installer (e.g. "dev-main-1a2b3c4"). Such builds are not offered release updates. A plain "dev"
// comes from an older dashboard that updated itself by compiling main.go; it is offered the
// release so it switches to the signed binaries.
func IsDevelopmentBuild(version string) bool {
	return strings.HasPrefix(version, "dev-")
}

// maxReleaseNotes limits the text of a release kept for the update view.
const maxReleaseNotes = 16 * 1024

// ReleaseInfo describes a published release.
type ReleaseInfo struct {
	Tag       string            `json:"tag"`
	URL       string            `json:"url"`
	Published time.Time         `json:"published"`
	Notes     string            `json:"notes,omitempty"`
	Assets    map[string]string `json:"-"` // asset name -> download URL
}

// UpdateStatus is what the UI shows.
type UpdateStatus struct {
	Current   string    `json:"current_version"`
	Latest    string    `json:"latest_version"`
	Available bool      `json:"has_update"`
	URL       string    `json:"release_url,omitempty"`
	Notes     string    `json:"release_notes,omitempty"`
	Checked   time.Time `json:"checked,omitempty"`
	Error     string    `json:"error,omitempty"`
	Enabled   bool      `json:"self_update_enabled"`
	DevBuild  bool      `json:"development_build,omitempty"`
}

// Updater checks for and installs releases.
type Updater struct {
	Repo           string // "owner/name"
	APIBase        string // default https://api.github.com
	PublicKey      string // base64 Ed25519 public key; empty disables installing updates
	CurrentVersion string
	Arch           string // default runtime.GOARCH
	Client         *http.Client

	// SmokeTest runs the downloaded binary; replaced in tests.
	SmokeTest func(path, wantVersion string) error

	mu      sync.Mutex
	status  UpdateStatus
	install sync.Mutex

	notesMu      sync.Mutex
	notes        []ReleaseNotes
	notesFetched time.Time
	notesFailed  time.Time
	notesErr     error
}

func (u *Updater) client() *http.Client {
	if u.Client != nil {
		return u.Client
	}
	return &http.Client{Timeout: 60 * time.Second}
}

func (u *Updater) apiBase() string {
	if u.APIBase != "" {
		return strings.TrimRight(u.APIBase, "/")
	}
	return "https://api.github.com"
}

func (u *Updater) arch() string {
	if u.Arch != "" {
		return u.Arch
	}
	return runtime.GOARCH
}

// AssetName is the binary asset for this platform.
func (u *Updater) AssetName() string {
	return "lgsm-dashboard-linux-" + u.arch()
}

func (u *Updater) getJSON(ctx context.Context, url string, dst interface{}) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", "linuxgsm-dashboard")
	req.Header.Set("Accept", "application/vnd.github+json")
	resp, err := u.client().Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("GitHub API returned status %d", resp.StatusCode)
	}
	return json.NewDecoder(io.LimitReader(resp.Body, 2<<20)).Decode(dst)
}

// Latest returns the latest stable release.
func (u *Updater) Latest(ctx context.Context) (ReleaseInfo, error) {
	var rel struct {
		TagName     string    `json:"tag_name"`
		Draft       bool      `json:"draft"`
		Prerelease  bool      `json:"prerelease"`
		HTMLURL     string    `json:"html_url"`
		PublishedAt time.Time `json:"published_at"`
		Body        string    `json:"body"`
		Assets      []struct {
			Name string `json:"name"`
			URL  string `json:"browser_download_url"`
		} `json:"assets"`
	}
	if err := u.getJSON(ctx, u.apiBase()+"/repos/"+u.Repo+"/releases/latest", &rel); err != nil {
		return ReleaseInfo{}, err
	}
	if rel.Draft || rel.Prerelease || !IsStableReleaseTag(rel.TagName) {
		return ReleaseInfo{}, errors.New("latest release is not a stable release")
	}
	if len(rel.Body) > maxReleaseNotes {
		rel.Body = strings.ToValidUTF8(rel.Body[:maxReleaseNotes], "")
	}
	info := ReleaseInfo{Tag: rel.TagName, URL: rel.HTMLURL, Published: rel.PublishedAt, Notes: rel.Body, Assets: map[string]string{}}
	for _, a := range rel.Assets {
		info.Assets[a.Name] = a.URL
	}
	return info, nil
}

// VerifyOnReleaseBranch checks via the compare API that the tag is contained in the release
// branch, i.e. the release was not built from a development branch.
func (u *Updater) VerifyOnReleaseBranch(ctx context.Context, tag string) error {
	var cmp struct {
		Status   string `json:"status"`
		AheadBy  int    `json:"ahead_by"`
		BehindBy int    `json:"behind_by"`
	}
	url := fmt.Sprintf("%s/repos/%s/compare/%s...%s", u.apiBase(), u.Repo, ReleaseBranch, tag)
	if err := u.getJSON(ctx, url, &cmp); err != nil {
		return fmt.Errorf("could not verify the release branch: %w", err)
	}
	// "behind": the tag is an ancestor of main; "identical": main points to the tag.
	if (cmp.Status != "behind" && cmp.Status != "identical") || cmp.AheadBy != 0 {
		return fmt.Errorf("release %s is not part of the %s branch", tag, ReleaseBranch)
	}
	return nil
}

// checkInstallable verifies that a release carries signed binaries for this platform and was
// built from the release branch. It does not download anything.
func (u *Updater) checkInstallable(ctx context.Context, rel ReleaseInfo) error {
	for _, name := range []string{u.AssetName(), "SHA256SUMS", "SHA256SUMS.sig"} {
		if rel.Assets[name] == "" {
			return fmt.Errorf("release %s has no signed binary for linux/%s (yet)", rel.Tag, u.arch())
		}
	}
	return u.VerifyOnReleaseBranch(ctx, rel.Tag)
}

// Check refreshes the cached update status.
func (u *Updater) Check(ctx context.Context) UpdateStatus {
	st := UpdateStatus{Current: u.CurrentVersion, Checked: time.Now().UTC(), Enabled: u.PublicKey != "", DevBuild: IsDevelopmentBuild(u.CurrentVersion)}
	rel, err := u.Latest(ctx)
	if err != nil {
		st.Error = err.Error()
	} else if err := u.checkInstallable(ctx, rel); err != nil {
		// A release without signed binaries or from another branch is never offered.
		st.Error = err.Error()
	} else {
		st.Latest = rel.Tag
		st.URL = rel.URL
		st.Notes = rel.Notes
		st.Available = IsNewerVersion(rel.Tag, u.CurrentVersion) && !st.DevBuild
	}
	u.mu.Lock()
	if st.Error != "" && st.Latest == "" && u.status.Latest != "" {
		// Keep the last known release when GitHub is temporarily unreachable.
		st.Latest, st.URL, st.Notes, st.Available = u.status.Latest, u.status.URL, u.status.Notes, u.status.Available
	}
	u.status = st
	u.mu.Unlock()
	return st
}

// Status returns the cached status of the last check.
func (u *Updater) Status() UpdateStatus {
	u.mu.Lock()
	defer u.mu.Unlock()
	st := u.status
	if st.Current == "" {
		st.Current = u.CurrentVersion
		st.Enabled = u.PublicKey != ""
		st.DevBuild = IsDevelopmentBuild(u.CurrentVersion)
	}
	return st
}

// StartBackgroundCheck checks for updates shortly after start and then every interval.
func (u *Updater) StartBackgroundCheck(interval time.Duration) {
	go func() {
		defer logPanic("update check")
		time.Sleep(30 * time.Second)
		for {
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			st := u.Check(ctx)
			cancel()
			if st.Available {
				fmt.Printf("[UPDATE] Version %s is available (running %s)\n", st.Latest, st.Current)
			}
			time.Sleep(interval)
		}
	}()
}

func (u *Updater) download(ctx context.Context, url string, limit int64, w io.Writer) error {
	if !strings.HasPrefix(url, "https://") {
		return errors.New("refusing non-https download")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", "linuxgsm-dashboard")
	resp, err := u.client().Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("download returned status %d", resp.StatusCode)
	}
	n, err := io.Copy(w, io.LimitReader(resp.Body, limit+1))
	if err != nil {
		return err
	}
	if n > limit {
		return errors.New("download exceeds size limit")
	}
	return nil
}

// VerifyChecksumsSignature verifies SHA256SUMS against the embedded release key.
func VerifyChecksumsSignature(publicKeyB64 string, sums, sigB64 []byte) error {
	pub, err := base64.StdEncoding.DecodeString(strings.TrimSpace(publicKeyB64))
	if err != nil || len(pub) != ed25519.PublicKeySize {
		return errors.New("invalid embedded release public key")
	}
	sig, err := base64.StdEncoding.DecodeString(strings.TrimSpace(string(sigB64)))
	if err != nil || len(sig) != ed25519.SignatureSize {
		return errors.New("invalid release signature")
	}
	if !ed25519.Verify(ed25519.PublicKey(pub), sums, sig) {
		return errors.New("release signature does not match the release key")
	}
	return nil
}

// ChecksumFor returns the hex SHA-256 listed for name in a SHA256SUMS file.
func ChecksumFor(sums []byte, name string) (string, error) {
	scanner := bufio.NewScanner(bytes.NewReader(sums))
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) == 2 && strings.TrimPrefix(fields[1], "*") == name {
			if _, err := hex.DecodeString(fields[0]); err != nil || len(fields[0]) != 64 {
				return "", errors.New("malformed checksum")
			}
			return strings.ToLower(fields[0]), nil
		}
	}
	return "", fmt.Errorf("no checksum for %s", name)
}

func defaultSmokeTest(path, wantVersion string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, path, "-version").Output()
	if err != nil {
		return fmt.Errorf("new binary does not start: %v", err)
	}
	if got := strings.TrimSpace(string(out)); got != wantVersion {
		return fmt.Errorf("new binary reports version %q, expected %q", got, wantVersion)
	}
	return nil
}

// Install downloads, verifies and installs the given release in place of execPath.
// The previous binary is kept as execPath+".old".
func (u *Updater) Install(ctx context.Context, tag, execPath string) error {
	u.install.Lock()
	defer u.install.Unlock()

	if u.PublicKey == "" {
		return errors.New("self-update is disabled: this build has no release signing key")
	}
	if !IsStableReleaseTag(tag) {
		return errors.New("invalid release tag")
	}
	rel, err := u.Latest(ctx)
	if err != nil {
		return fmt.Errorf("could not query the latest release: %w", err)
	}
	if rel.Tag != tag {
		return errors.New("only the latest release can be installed")
	}
	if !IsNewerVersion(rel.Tag, u.CurrentVersion) {
		return errors.New("the installed version is already up to date")
	}
	if err := u.VerifyOnReleaseBranch(ctx, tag); err != nil {
		return err
	}

	asset := u.AssetName()
	binURL, sumsURL, sigURL := rel.Assets[asset], rel.Assets["SHA256SUMS"], rel.Assets["SHA256SUMS.sig"]
	if binURL == "" || sumsURL == "" || sigURL == "" {
		return fmt.Errorf("release %s has no signed binary for linux/%s (yet)", tag, u.arch())
	}

	var sums, sig bytes.Buffer
	if err := u.download(ctx, sumsURL, maxChecksumsSize, &sums); err != nil {
		return fmt.Errorf("checksums: %w", err)
	}
	if err := u.download(ctx, sigURL, 4096, &sig); err != nil {
		return fmt.Errorf("signature: %w", err)
	}
	if err := VerifyChecksumsSignature(u.PublicKey, sums.Bytes(), sig.Bytes()); err != nil {
		return err
	}
	want, err := ChecksumFor(sums.Bytes(), asset)
	if err != nil {
		return err
	}

	execPath, err = filepath.EvalSymlinks(execPath)
	if err != nil {
		return err
	}
	dir := filepath.Dir(execPath)
	tmp, err := os.CreateTemp(dir, ".lgsm-dashboard-update-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	keep := false
	defer func() {
		if !keep {
			os.Remove(tmpName)
		}
	}()

	hasher := sha256.New()
	if err := u.download(ctx, binURL, maxBinarySize, io.MultiWriter(tmp, hasher)); err != nil {
		tmp.Close()
		return fmt.Errorf("binary: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if got := hex.EncodeToString(hasher.Sum(nil)); got != want {
		return errors.New("downloaded binary does not match the signed checksum")
	}
	if err := os.Chmod(tmpName, 0755); err != nil {
		return err
	}

	smoke := u.SmokeTest
	if smoke == nil {
		smoke = defaultSmokeTest
	}
	if err := smoke(tmpName, strings.TrimPrefix(tag, "v")); err != nil {
		return err
	}

	oldPath := execPath + ".old"
	_ = os.Remove(oldPath)
	if err := os.Rename(execPath, oldPath); err != nil {
		return fmt.Errorf("failed to back up the current binary: %w", err)
	}
	if err := os.Rename(tmpName, execPath); err != nil {
		_ = os.Rename(oldPath, execPath)
		return fmt.Errorf("failed to install the new binary: %w", err)
	}
	keep = true
	return nil
}
