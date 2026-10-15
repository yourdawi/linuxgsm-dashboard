package backend

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"testing"
)

func TestHashAndVerifyPassword(t *testing.T) {
	h, err := HashPassword("correct horse battery staple")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(h, "$argon2id$v=19$") {
		t.Fatalf("unexpected hash format %q", h)
	}
	if !VerifyPassword("correct horse battery staple", h) {
		t.Fatal("valid password rejected")
	}
	if VerifyPassword("wrong", h) {
		t.Fatal("invalid password accepted")
	}
	h2, _ := HashPassword("correct horse battery staple")
	if h == h2 {
		t.Fatal("hashes must use a unique salt")
	}
}

func TestVerifyLegacyAndMalformedHashes(t *testing.T) {
	sum := sha256.Sum256([]byte("legacy-pass"))
	legacy := hex.EncodeToString(sum[:])
	if !IsLegacyPasswordHash(legacy) {
		t.Fatal("legacy hash not detected")
	}
	if !VerifyPassword("legacy-pass", legacy) || VerifyPassword("other", legacy) {
		t.Fatal("legacy verification broken")
	}
	for _, bad := range []string{"", "plain", "$argon2id$v=19$m=999999999,t=1,p=1$AAAA$AAAA", "$argon2i$v=19$m=1024,t=1,p=1$AAAA$AAAA", "$argon2id$v=19$m=1024,t=1,p=1$!!$AAAA"} {
		if VerifyPassword("x", bad) {
			t.Fatalf("malformed hash accepted: %q", bad)
		}
	}
}

func TestShellQuote(t *testing.T) {
	cases := map[string]string{
		"simple":        "'simple'",
		"it's":          `'it'\''s'`,
		"$(rm -rf /)":   "'$(rm -rf /)'",
		"a b; c && d":   "'a b; c && d'",
		"/mnt/ssd/game": "'/mnt/ssd/game'",
	}
	for in, want := range cases {
		if got := ShellQuote(in); got != want {
			t.Errorf("ShellQuote(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestIsSafeCfgValue(t *testing.T) {
	ok := []string{"", "https://discord.com/api/webhooks/1/abc", "user@example.com", "587", "on"}
	bad := []string{`a"b`, "a`id`", "$(id)", "a\nb", "a\\b", "x\rY", "a\x00"}
	for _, v := range ok {
		if !IsSafeCfgValue(v) {
			t.Errorf("expected safe: %q", v)
		}
	}
	for _, v := range bad {
		if IsSafeCfgValue(v) {
			t.Errorf("expected unsafe: %q", v)
		}
	}
}

func TestIdentifierValidation(t *testing.T) {
	if !IsValidServerID("vhserver") || !IsValidServerID("mcserver-2") || !IsValidServerID("vhserver@bob") {
		t.Fatal("valid server IDs rejected")
	}
	for _, id := range []string{"", "../etc", "a/b", "a b", "x'y", "<script>", ".hidden", "a..b"} {
		if IsValidServerID(id) {
			t.Errorf("invalid server ID accepted: %q", id)
		}
	}
	if !IsValidLinuxUsername("vhserver") || IsValidLinuxUsername("-root") || IsValidLinuxUsername("a b") || IsValidLinuxUsername("Root") {
		t.Fatal("linux username validation broken")
	}
	if IsValidScriptName("x';id;'") || IsValidScriptName("../x") || !IsValidScriptName("vhserver") {
		t.Fatal("script name validation broken")
	}
}

func TestIsSafeServerDir(t *testing.T) {
	ok := []string{"/home/vhserver", "/mnt/ssd/gameservers/ark", "/srv/games/rust-1", "/data/lgsm/mc"}
	bad := []string{"", "relative/dir", "/", "/home", "/mnt", "/etc", "/etc/lgsm", "/root/x", "/usr/local/games",
		"/var/lib/x", "/proc/1", "/tmp/x", "/mnt/with space", "/mnt/a;b", "/mnt/$x", "/mnt/../etc", "/mnt/./x", "/mnt//x", "/mnt/x/", "/mnt/%h"}
	for _, d := range ok {
		if !IsSafeServerDir(d) {
			t.Errorf("expected safe dir: %q", d)
		}
	}
	for _, d := range bad {
		if IsSafeServerDir(d) {
			t.Errorf("expected unsafe dir: %q", d)
		}
	}
}

func TestValidateConsoleCommand(t *testing.T) {
	if err := ValidateConsoleCommand("say hello"); err != nil {
		t.Fatal(err)
	}
	for _, c := range []string{"", "   ", "a\nquit", "a\rb", strings.Repeat("a", 2000), "x\x1b[2J"} {
		if ValidateConsoleCommand(c) == nil {
			t.Errorf("command accepted: %q", c)
		}
	}
}
