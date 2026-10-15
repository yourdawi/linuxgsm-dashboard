package backend

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"errors"
	"testing"
)

type tarEntry struct {
	name     string
	typ      byte
	linkname string
	body     string
}

func buildTarGz(t *testing.T, entries []tarEntry) *bytes.Reader {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for _, e := range entries {
		typ := e.typ
		if typ == 0 {
			typ = tar.TypeReg
		}
		hdr := &tar.Header{Name: e.name, Typeflag: typ, Linkname: e.linkname, Mode: 0644, Size: int64(len(e.body))}
		if typ != tar.TypeReg {
			hdr.Size = 0
		}
		if err := tw.WriteHeader(hdr); err != nil {
			t.Fatal(err)
		}
		if typ == tar.TypeReg && e.body != "" {
			if _, err := tw.Write([]byte(e.body)); err != nil {
				t.Fatal(err)
			}
		}
	}
	tw.Close()
	gz.Close()
	return bytes.NewReader(buf.Bytes())
}

func TestValidateTarGzAcceptsNormalBackup(t *testing.T) {
	r := buildTarGz(t, []tarEntry{
		{name: "./", typ: tar.TypeDir},
		{name: "./serverfiles/", typ: tar.TypeDir},
		{name: "./serverfiles/server.cfg", body: "hostname x"},
		{name: "./serverfiles/steamclient.so", typ: tar.TypeSymlink, linkname: "linux64/steamclient.so"},
		{name: "./serverfiles/abs-link", typ: tar.TypeSymlink, linkname: "/home/vhserver/serverfiles/x"},
		{name: "./serverfiles/hard", typ: tar.TypeLink, linkname: "./serverfiles/server.cfg"},
	})
	if err := ValidateTarGz(r, "/home/vhserver", ArchiveLimits{MaxEntries: 100, MaxTotalSize: 1 << 20}); err != nil {
		t.Fatalf("normal archive rejected: %v", err)
	}
}

func TestValidateTarGzRejectsMaliciousArchives(t *testing.T) {
	limits := ArchiveLimits{MaxEntries: 100, MaxTotalSize: 1 << 20}
	cases := map[string][]tarEntry{
		"traversal":         {{name: "../../etc/cron.d/evil", body: "x"}},
		"nested traversal":  {{name: "serverfiles/../../evil", body: "x"}},
		"absolute":          {{name: "/etc/passwd", body: "x"}},
		"symlink absolute":  {{name: "link", typ: tar.TypeSymlink, linkname: "/etc"}},
		"symlink relative":  {{name: "serverfiles/link", typ: tar.TypeSymlink, linkname: "../../../etc"}},
		"hardlink escape":   {{name: "hard", typ: tar.TypeLink, linkname: "../../etc/shadow"}},
		"hardlink absolute": {{name: "hard", typ: tar.TypeLink, linkname: "/etc/shadow"}},
		"char device":       {{name: "dev", typ: tar.TypeChar}},
		"fifo":              {{name: "fifo", typ: tar.TypeFifo}},
	}
	for name, entries := range cases {
		err := ValidateTarGz(buildTarGz(t, entries), "/home/vhserver", limits)
		if !errors.Is(err, ErrUnsafeArchive) {
			t.Errorf("%s: expected ErrUnsafeArchive, got %v", name, err)
		}
	}
}

func TestValidateTarGzLimits(t *testing.T) {
	var many []tarEntry
	for i := 0; i < 20; i++ {
		many = append(many, tarEntry{name: "f" + string(rune('a'+i))})
	}
	if err := ValidateTarGz(buildTarGz(t, many), "/srv", ArchiveLimits{MaxEntries: 10}); !errors.Is(err, ErrUnsafeArchive) {
		t.Fatalf("entry limit not enforced: %v", err)
	}
	big := []tarEntry{{name: "big", body: string(make([]byte, 4096))}}
	if err := ValidateTarGz(buildTarGz(t, big), "/srv", ArchiveLimits{MaxTotalSize: 1024}); !errors.Is(err, ErrUnsafeArchive) {
		t.Fatalf("size limit not enforced: %v", err)
	}
	if err := ValidateTarGz(bytes.NewReader([]byte("not a gzip")), "/srv", DefaultArchiveLimits); !errors.Is(err, ErrUnsafeArchive) {
		t.Fatalf("garbage accepted: %v", err)
	}
}

func TestBackupFilenameValidation(t *testing.T) {
	if !IsValidBackupFilename("vhserver-2024-01-01-120000.tar.gz") {
		t.Fatal("valid backup name rejected")
	}
	for _, n := range []string{"../x.tar.gz", "a/b.tar.gz", ".hidden.tar.gz", "x.zip", "a b.tar.gz", "x'.tar.gz", "x\".tar.gz"} {
		if IsValidBackupFilename(n) {
			t.Errorf("invalid backup name accepted: %q", n)
		}
	}
}
