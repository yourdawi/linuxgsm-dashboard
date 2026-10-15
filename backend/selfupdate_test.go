package backend

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type fakeGitHub struct {
	server     *httptest.Server
	tag        string
	prerelease bool
	compare    string // compare status: behind, identical, ahead, diverged
	binary     []byte
	sums       []byte
	sig        []byte
	pubKey     string
}

func newFakeGitHub(t *testing.T, tag string) *fakeGitHub {
	t.Helper()
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	f := &fakeGitHub{tag: tag, compare: "behind", binary: []byte("#!/bin/true new dashboard binary")}
	sum := sha256.Sum256(f.binary)
	f.sums = []byte(fmt.Sprintf("%s  lgsm-dashboard-linux-amd64\n%s  lgsm-dashboard-linux-arm64\n", hex.EncodeToString(sum[:]), strings.Repeat("0", 64)))
	f.sig = []byte(base64.StdEncoding.EncodeToString(ed25519.Sign(priv, f.sums)))
	f.pubKey = base64.StdEncoding.EncodeToString(pub)

	mux := http.NewServeMux()
	mux.HandleFunc("/repos/owner/repo/releases/latest", func(w http.ResponseWriter, r *http.Request) {
		base := f.server.URL + "/download/"
		json.NewEncoder(w).Encode(map[string]interface{}{
			"tag_name":   f.tag,
			"prerelease": f.prerelease,
			"html_url":   "https://example.invalid/release",
			"assets": []map[string]string{
				{"name": "lgsm-dashboard-linux-amd64", "browser_download_url": base + "bin"},
				{"name": "SHA256SUMS", "browser_download_url": base + "sums"},
				{"name": "SHA256SUMS.sig", "browser_download_url": base + "sig"},
			},
		})
	})
	mux.HandleFunc("/repos/owner/repo/compare/", func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/compare/main..."+f.tag) {
			http.NotFound(w, r)
			return
		}
		ahead := 0
		if f.compare == "ahead" || f.compare == "diverged" {
			ahead = 3
		}
		json.NewEncoder(w).Encode(map[string]interface{}{"status": f.compare, "ahead_by": ahead})
	})
	mux.HandleFunc("/download/bin", func(w http.ResponseWriter, r *http.Request) { w.Write(f.binary) })
	mux.HandleFunc("/download/sums", func(w http.ResponseWriter, r *http.Request) { w.Write(f.sums) })
	mux.HandleFunc("/download/sig", func(w http.ResponseWriter, r *http.Request) { w.Write(f.sig) })
	f.server = httptest.NewTLSServer(mux)
	t.Cleanup(f.server.Close)
	return f
}

func (f *fakeGitHub) updater(current string) *Updater {
	return &Updater{
		Repo:           "owner/repo",
		APIBase:        f.server.URL,
		PublicKey:      f.pubKey,
		CurrentVersion: current,
		Arch:           "amd64",
		Client:         f.server.Client(),
		SmokeTest:      func(path, want string) error { return nil },
	}
}

func installTarget(t *testing.T) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "lgsm-dashboard")
	if err := os.WriteFile(p, []byte("old binary"), 0755); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestUpdaterInstallsSignedRelease(t *testing.T) {
	gh := newFakeGitHub(t, "v1.2.0")
	u := gh.updater("1.1.0")
	var smokeVersion string
	u.SmokeTest = func(path, want string) error { smokeVersion = want; return nil }

	target := installTarget(t)
	if err := u.Install(context.Background(), "v1.2.0", target); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(target)
	if string(got) != string(gh.binary) {
		t.Fatal("binary not replaced")
	}
	old, _ := os.ReadFile(target + ".old")
	if string(old) != "old binary" {
		t.Fatal("previous binary not kept for rollback")
	}
	if smokeVersion != "1.2.0" {
		t.Fatalf("smoke test expected version %q", smokeVersion)
	}
}

func TestUpdaterRejectsUnsafeReleases(t *testing.T) {
	cases := map[string]func(gh *fakeGitHub, u *Updater) string{
		"not on main (ahead)": func(gh *fakeGitHub, u *Updater) string { gh.compare = "ahead"; return "v1.2.0" },
		"not on main (diverged)": func(gh *fakeGitHub, u *Updater) string {
			gh.compare = "diverged"
			return "v1.2.0"
		},
		"bad signature": func(gh *fakeGitHub, u *Updater) string {
			_, other, _ := ed25519.GenerateKey(rand.Reader)
			gh.sig = []byte(base64.StdEncoding.EncodeToString(ed25519.Sign(other, gh.sums)))
			return "v1.2.0"
		},
		"tampered binary": func(gh *fakeGitHub, u *Updater) string { gh.binary = []byte("evil"); return "v1.2.0" },
		"not the latest":  func(gh *fakeGitHub, u *Updater) string { return "v1.1.5" },
		"prerelease": func(gh *fakeGitHub, u *Updater) string {
			gh.prerelease = true
			return "v1.2.0"
		},
		"no key embedded": func(gh *fakeGitHub, u *Updater) string { u.PublicKey = ""; return "v1.2.0" },
		"downgrade":       func(gh *fakeGitHub, u *Updater) string { u.CurrentVersion = "1.3.0"; return "v1.2.0" },
		"dev tag":         func(gh *fakeGitHub, u *Updater) string { gh.tag = "v1.2.0-dev"; return "v1.2.0-dev" },
		"failed smoke test": func(gh *fakeGitHub, u *Updater) string {
			u.SmokeTest = func(string, string) error { return fmt.Errorf("does not start") }
			return "v1.2.0"
		},
	}
	for name, setup := range cases {
		t.Run(name, func(t *testing.T) {
			gh := newFakeGitHub(t, "v1.2.0")
			u := gh.updater("1.1.0")
			tag := setup(gh, u)
			target := installTarget(t)
			if err := u.Install(context.Background(), tag, target); err == nil {
				t.Fatal("unsafe update was installed")
			}
			got, _ := os.ReadFile(target)
			if string(got) != "old binary" {
				t.Fatal("current binary was modified")
			}
			entries, _ := os.ReadDir(filepath.Dir(target))
			if len(entries) != 1 {
				t.Fatalf("temporary files left behind: %d entries", len(entries))
			}
		})
	}
}

func TestUpdaterCheck(t *testing.T) {
	gh := newFakeGitHub(t, "v1.2.0")
	st := gh.updater("1.1.9").Check(context.Background())
	if !st.Available || st.Latest != "v1.2.0" || !st.Enabled {
		t.Fatalf("unexpected status %+v", st)
	}
	if st := gh.updater("1.2.0").Check(context.Background()); st.Available {
		t.Fatal("same version reported as update")
	}
	if st := gh.updater("dev").Check(context.Background()); !st.Available {
		t.Fatal("legacy self-compiled builds should be offered the release")
	}
	if st := gh.updater("dev-main-1a2b3c4").Check(context.Background()); st.Available || !st.DevBuild {
		t.Fatalf("development builds must not get update notices: %+v", st)
	}
}

func TestUpdaterCheckIgnoresUnsignedOrDevReleases(t *testing.T) {
	gh := newFakeGitHub(t, "v1.2.0")
	gh.compare = "ahead" // e.g. a release created on the website with target "dev"
	if st := gh.updater("1.1.0").Check(context.Background()); st.Available {
		t.Fatal("release outside of main must not be announced")
	}

	gh2 := newFakeGitHub(t, "v1.2.0")
	u := gh2.updater("1.1.0")
	u.Arch = "riscv64" // no binary for this platform in the release
	if st := u.Check(context.Background()); st.Available {
		t.Fatal("release without a binary for this platform must not be announced")
	}
}

func TestVersionHelpers(t *testing.T) {
	cases := []struct {
		latest, current string
		want            bool
	}{
		{"v1.2.0", "1.1.9", true},
		{"v1.10.0", "1.9.9", true},
		{"v1.2.0", "1.2.0", false},
		{"v1.2.0", "v1.3.0", false},
		{"v1.2.0", "dev", true},
		{"garbage", "1.0.0", false},
	}
	for _, c := range cases {
		if got := IsNewerVersion(c.latest, c.current); got != c.want {
			t.Errorf("IsNewerVersion(%q, %q) = %v", c.latest, c.current, got)
		}
	}
	for _, tag := range []string{"v1.2.3", "v10.0.1"} {
		if !IsStableReleaseTag(tag) {
			t.Errorf("valid tag rejected: %s", tag)
		}
	}
	for _, tag := range []string{"1.2.3", "v1.2", "v1.2.3-beta.1", "v1.2.3+dev", "main"} {
		if IsStableReleaseTag(tag) {
			t.Errorf("tag accepted: %s", tag)
		}
	}
	sums := []byte("abc  x\n" + strings.Repeat("a", 64) + "  lgsm-dashboard-linux-amd64\n")
	if sum, err := ChecksumFor(sums, "lgsm-dashboard-linux-amd64"); err != nil || sum != strings.Repeat("a", 64) {
		t.Fatalf("ChecksumFor: %q %v", sum, err)
	}
	if _, err := ChecksumFor(sums, "x"); err == nil {
		t.Fatal("malformed checksum accepted")
	}
}
