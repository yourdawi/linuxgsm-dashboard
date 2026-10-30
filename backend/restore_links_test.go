package backend

import (
	"archive/tar"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestHostLogSkips(t *testing.T) {
	h := hostLogSkips{enabled: true}
	for _, hdr := range []*tar.Header{
		{Name: "././log/server/a.log", Typeflag: tar.TypeReg},
		{Name: "././serverfiles/logs/a.log", Typeflag: tar.TypeLink, Linkname: "././log/server/a.log"},
		{Name: "././log/server/b.log", Typeflag: tar.TypeLink, Linkname: "././serverfiles/logs/b.log"},
		{Name: "././serverfiles/x", Typeflag: tar.TypeLink, Linkname: "././serverfiles/y"},
		{Name: "././log/serverx/c", Typeflag: tar.TypeLink, Linkname: "././serverfiles/c"},
		{Name: "././log/server/bad\nname", Typeflag: tar.TypeLink, Linkname: "././serverfiles/d"},
	} {
		h.add(hdr)
	}
	want := []string{"././log/server/a.log", "././serverfiles/logs/a.log", "././log/server/b.log"}
	if strings.Join(h.names, "|") != strings.Join(want, "|") {
		t.Fatalf("got %q want %q", h.names, want)
	}
	off := hostLogSkips{}
	off.add(&tar.Header{Name: "././serverfiles/logs/a.log", Typeflag: tar.TypeLink, Linkname: "././log/server/a.log"})
	if len(off.names) != 0 {
		t.Fatal("links skipped although log/server is no symlink")
	}
}

func TestHostRestoreCommands(t *testing.T) {
	dir := t.TempDir()
	disk := t.TempDir()
	for _, d := range []string{"lgsm/config-lgsm", "log"} {
		if err := os.MkdirAll(filepath.Join(dir, d), 0755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink(disk, filepath.Join(dir, "serverfiles")); err != nil {
		t.Fatal(err)
	}
	members := []string{"././serverfiles", "././lgsm/config-lgsm", "././log"}
	cmds, err := hostRestoreCommands(dir, ArchiveTarGz, "/b/a.tar.gz", members, []string{"././log/server"})
	if err != nil {
		t.Fatal(err)
	}
	if len(cmds) != 2 {
		t.Fatalf("got %d commands", len(cmds))
	}
	direct := strings.Join(cmds[0].args, " ")
	if !strings.Contains(direct, "-C "+dir+" -- ././lgsm/config-lgsm ././log") || strings.Contains(direct, "serverfiles") {
		t.Errorf("direct command: %s", direct)
	}
	linked := strings.Join(cmds[1].args, " ")
	if !strings.Contains(linked, `--transform s,^\./\./serverfiles/,, -xf /b/a.tar.gz -C `+disk+" -- ././serverfiles") {
		t.Errorf("symlinked command: %s", linked)
	}
	for _, c := range cmds {
		if c.stdin != "././log/server\n" || !strings.Contains(strings.Join(c.args, " "), "--anchored --no-wildcards -X -") {
			t.Errorf("excludes missing: %q %v", c.stdin, c.args)
		}
	}
	if err := os.Symlink(filepath.Join(dir, "missing"), filepath.Join(dir, "lgsm", "data")); err != nil {
		t.Fatal(err)
	}
	if _, err := hostRestoreCommands(dir, ArchiveTarGz, "/b/a.tar.gz", []string{"././lgsm/data"}, nil); err == nil {
		t.Error("dangling symlink accepted")
	}
}
