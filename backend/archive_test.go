package backend

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"errors"
	"io"
	"testing"

	"github.com/klauspost/compress/zstd"
)

type tarEntry struct {
	name     string
	typ      byte
	linkname string
	body     string
}

func buildTarGz(t *testing.T, entries []tarEntry) *bytes.Reader {
	t.Helper()
	return buildArchive(t, ArchiveTarGz, entries)
}

func buildArchive(t *testing.T, format ArchiveFormat, entries []tarEntry) *bytes.Reader {
	t.Helper()
	var buf bytes.Buffer
	var comp io.WriteCloser
	switch format {
	case ArchiveTarGz:
		comp = gzip.NewWriter(&buf)
	case ArchiveTarZst:
		zw, err := zstd.NewWriter(&buf)
		if err != nil {
			t.Fatal(err)
		}
		comp = zw
	default:
		comp = nopWriteCloser{&buf}
	}
	tw := tar.NewWriter(comp)
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
	comp.Close()
	return bytes.NewReader(buf.Bytes())
}

type nopWriteCloser struct{ io.Writer }

func (nopWriteCloser) Close() error { return nil }

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

func TestValidateArchiveZstdAndTar(t *testing.T) {
	limits := ArchiveLimits{MaxEntries: 100, MaxTotalSize: 1 << 20}
	normal := []tarEntry{
		{name: "./", typ: tar.TypeDir},
		{name: "./serverfiles/", typ: tar.TypeDir},
		{name: "./serverfiles/server.cfg", body: "hostname x"},
		{name: "./serverfiles/steamclient.so", typ: tar.TypeSymlink, linkname: "linux64/steamclient.so"},
		{name: "./serverfiles/hard", typ: tar.TypeLink, linkname: "./serverfiles/server.cfg"},
	}
	malicious := map[string][]tarEntry{
		"traversal":        {{name: "../../etc/cron.d/evil", body: "x"}},
		"nested traversal": {{name: "./serverfiles/../../../root/.ssh/authorized_keys", body: "x"}},
		"absolute":         {{name: "/etc/passwd", body: "x"}},
		"symlink relative": {{name: "serverfiles/link", typ: tar.TypeSymlink, linkname: "../../../etc"}},
		"hardlink escape":  {{name: "hard", typ: tar.TypeLink, linkname: "../../etc/shadow"}},
		"block device":     {{name: "dev", typ: tar.TypeBlock}},
	}
	for _, format := range []ArchiveFormat{ArchiveTarZst, ArchiveTar} {
		if err := ValidateArchive(buildArchive(t, format, normal), format, "/home/vhserver", limits); err != nil {
			t.Fatalf("format %d: normal archive rejected: %v", format, err)
		}
		for name, entries := range malicious {
			err := ValidateArchive(buildArchive(t, format, entries), format, "/home/vhserver", limits)
			if !errors.Is(err, ErrUnsafeArchive) {
				t.Errorf("format %d, %s: expected ErrUnsafeArchive, got %v", format, name, err)
			}
		}
		big := []tarEntry{{name: "big", body: string(make([]byte, 4096))}}
		if err := ValidateArchive(buildArchive(t, format, big), format, "/srv", ArchiveLimits{MaxTotalSize: 1024}); !errors.Is(err, ErrUnsafeArchive) {
			t.Errorf("format %d: size limit not enforced: %v", format, err)
		}
	}
	// The format comes from the file name; content of another format is rejected.
	gz := buildArchive(t, ArchiveTarGz, normal)
	if err := ValidateArchive(gz, ArchiveTarZst, "/srv", limits); !errors.Is(err, ErrUnsafeArchive) {
		t.Errorf("gzip data accepted as zstd: %v", err)
	}
	if err := ValidateArchive(bytes.NewReader([]byte("not a zstd frame")), ArchiveTarZst, "/srv", limits); !errors.Is(err, ErrUnsafeArchive) {
		t.Errorf("garbage accepted as zstd: %v", err)
	}
}

func TestBackupArchiveFormat(t *testing.T) {
	cases := map[string]ArchiveFormat{"a.tar.gz": ArchiveTarGz, "a.tar.zst": ArchiveTarZst, "a.tar": ArchiveTar}
	for name, want := range cases {
		if got, ok := BackupArchiveFormat(name); !ok || got != want {
			t.Errorf("%s: got %d/%v, want %d", name, got, ok, want)
		}
	}
	for _, name := range []string{"a.zip", "a.tar.xz", "a.zst", "a.gz"} {
		if _, ok := BackupArchiveFormat(name); ok {
			t.Errorf("%s: unsupported format accepted", name)
		}
	}
}

func TestBackupFilenameValidation(t *testing.T) {
	for _, n := range []string{"vhserver-2024-01-01-120000.tar.gz", "vhserver-2024-01-01-120000.tar.zst", "vhserver-2024-01-01-120000.tar"} {
		if !IsValidBackupFilename(n) {
			t.Fatalf("valid backup name rejected: %q", n)
		}
	}
	for _, n := range []string{"../x.tar.gz", "a/b.tar.gz", ".hidden.tar.gz", "x.zip", "a b.tar.gz", "x'.tar.gz", "x\".tar.gz", "../x.tar.zst", "x.tar.zst.sh", "x.zst", "x.tar.xz"} {
		if IsValidBackupFilename(n) {
			t.Errorf("invalid backup name accepted: %q", n)
		}
	}
}
