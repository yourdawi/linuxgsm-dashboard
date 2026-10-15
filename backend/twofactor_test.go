package backend

import (
	"encoding/base32"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// RFC 6238 appendix B test vectors (SHA-1, 8 digits; the last 6 digits are the 6 digit code).
func TestTOTPRFC6238Vectors(t *testing.T) {
	secret := base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString([]byte("12345678901234567890"))
	vectors := map[int64]string{
		59:          "94287082",
		1111111109:  "07081804",
		1111111111:  "14050471",
		1234567890:  "89005924",
		2000000000:  "69279037",
		20000000000: "65353130",
	}
	for unix, eight := range vectors {
		got, err := TOTPCode(secret, unix/30)
		if err != nil {
			t.Fatal(err)
		}
		if got != eight[2:] {
			t.Errorf("T=%d: got %s, want %s", unix, got, eight[2:])
		}
	}
}

// fakeClock lets tests move time forward.
type fakeClock struct{ t time.Time }

func (c *fakeClock) now() time.Time { return c.t }

func newTwoFactorEnv(t *testing.T) (*AuthManager, *fakeClock) {
	t.Helper()
	am := newTestAuth(t)
	clock := &fakeClock{t: time.Unix(1_800_000_000, 0)}
	am.now = clock.now
	for _, u := range []string{"alice", "bob"} {
		if err := am.CreateUser(u, u+"-password", RoleUser, nil, nil); err != nil {
			t.Fatal(err)
		}
	}
	return am, clock
}

// enable2FA runs the complete setup for a user and returns its secret and recovery codes.
func enable2FA(t *testing.T, am *AuthManager, user string) (string, []string) {
	t.Helper()
	setup, err := am.StartTwoFactorSetup(user, user+"-password", "example.org")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(setup.URI, "otpauth://totp/") || !strings.Contains(setup.URI, "secret="+setup.Secret) {
		t.Fatalf("unexpected URI %q", setup.URI)
	}
	code, _ := TOTPCode(setup.Secret, am.now().Unix()/30)
	codes, err := am.ConfirmTwoFactorSetup(user, code, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(codes) != recoveryCodeCount {
		t.Fatalf("got %d recovery codes", len(codes))
	}
	return setup.Secret, codes
}

func currentCode(t *testing.T, am *AuthManager, secret string) string {
	t.Helper()
	code, err := TOTPCode(secret, am.now().Unix()/30)
	if err != nil {
		t.Fatal(err)
	}
	return code
}

func sessionUser(t *testing.T, am *AuthManager, sid string) string {
	t.Helper()
	s, ok := am.GetSession(sid)
	if !ok {
		t.Fatal("session not valid")
	}
	return s.Username
}

func TestTwoFactorLoginFlow(t *testing.T) {
	am, clock := newTwoFactorEnv(t)
	secret, _ := enable2FA(t, am, "alice")

	// Password alone no longer creates a session.
	if _, err := am.Login("alice", "alice-password"); !errors.Is(err, ErrTwoFactorRequired) {
		t.Fatalf("expected ErrTwoFactorRequired, got %v", err)
	}
	res, err := am.BeginLogin("alice", "alice-password")
	if err != nil || res.SessionID != "" || res.Challenge == "" {
		t.Fatalf("expected a challenge, got %+v %v", res, err)
	}
	if _, _, err := am.CompleteLogin(res.Challenge, "000000"); !errors.Is(err, ErrInvalidCode) {
		t.Fatalf("wrong code accepted: %v", err)
	}

	clock.t = clock.t.Add(60 * time.Second) // next code
	sid, usedRecovery, err := am.CompleteLogin(res.Challenge, currentCode(t, am, secret))
	if err != nil || usedRecovery || sessionUser(t, am, sid) != "alice" {
		t.Fatalf("login failed: %v", err)
	}
	// The challenge is single use.
	if _, _, err := am.CompleteLogin(res.Challenge, currentCode(t, am, secret)); !errors.Is(err, ErrChallengeExpired) {
		t.Fatal("challenge reused")
	}
	// The same code cannot be replayed for another login.
	res2, _ := am.BeginLogin("alice", "alice-password")
	if _, _, err := am.CompleteLogin(res2.Challenge, currentCode(t, am, secret)); !errors.Is(err, ErrInvalidCode) {
		t.Fatal("TOTP code replay accepted")
	}

	// Users without 2FA are not affected.
	if sid, err := am.Login("bob", "bob-password"); err != nil || sessionUser(t, am, sid) != "bob" {
		t.Fatalf("bob login: %v", err)
	}
}

func TestTwoFactorIsIndependentPerUser(t *testing.T) {
	am, clock := newTwoFactorEnv(t)
	aliceSecret, aliceCodes := enable2FA(t, am, "alice")
	clock.t = clock.t.Add(30 * time.Second)
	bobSecret, bobCodes := enable2FA(t, am, "bob")

	if aliceSecret == bobSecret {
		t.Fatal("users share a secret")
	}
	if hashRecoveryCode("alice", "abcde-fghjk") == hashRecoveryCode("bob", "abcde-fghjk") {
		t.Fatal("recovery code hashes are not bound to the user")
	}

	clock.t = clock.t.Add(60 * time.Second)

	// Bob's login cannot be completed with anything that belongs to Alice.
	bobLogin, _ := am.BeginLogin("bob", "bob-password")
	for _, code := range []string{aliceCodes[0], currentCode(t, am, aliceSecret)} {
		if _, _, err := am.CompleteLogin(bobLogin.Challenge, code); err == nil {
			t.Fatalf("bob's login accepted alice's code %q", code)
		}
	}
	// ... but with Bob's own recovery code, and the session belongs to Bob.
	sid, usedRecovery, err := am.CompleteLogin(bobLogin.Challenge, bobCodes[0])
	if err != nil || !usedRecovery || sessionUser(t, am, sid) != "bob" {
		t.Fatalf("bob recovery login: %v", err)
	}

	// Alice's codes are unaffected by Bob's login and still log in only Alice.
	aliceLogin, _ := am.BeginLogin("alice", "alice-password")
	if _, _, err := am.CompleteLogin(aliceLogin.Challenge, bobCodes[1]); err == nil {
		t.Fatal("alice's login accepted bob's recovery code")
	}
	sid, _, err = am.CompleteLogin(aliceLogin.Challenge, aliceCodes[0])
	if err != nil || sessionUser(t, am, sid) != "alice" {
		t.Fatalf("alice recovery login: %v", err)
	}

	// A challenge always stays bound to its user.
	if u, _ := am.ChallengeUser(aliceLogin.Challenge); u != "" {
		t.Fatal("completed challenge still valid")
	}
	if am.TwoFactorStatusFor("alice").RecoveryCodesLeft != 9 || am.TwoFactorStatusFor("bob").RecoveryCodesLeft != 9 {
		t.Fatal("recovery codes of one user were consumed by the other")
	}
}

func TestRecoveryCodesAreSingleUse(t *testing.T) {
	am, _ := newTwoFactorEnv(t)
	_, codes := enable2FA(t, am, "alice")

	login := func(code string) error {
		res, _ := am.BeginLogin("alice", "alice-password")
		_, _, err := am.CompleteLogin(res.Challenge, code)
		return err
	}
	if err := login(strings.ToUpper(strings.ReplaceAll(codes[3], "-", " "))); err != nil {
		t.Fatalf("formatted recovery code rejected: %v", err)
	}
	if err := login(codes[3]); err == nil {
		t.Fatal("recovery code used twice")
	}
	if left := am.TwoFactorStatusFor("alice").RecoveryCodesLeft; left != recoveryCodeCount-1 {
		t.Fatalf("expected %d codes left, got %d", recoveryCodeCount-1, left)
	}
}

func TestChallengeLimits(t *testing.T) {
	am, clock := newTwoFactorEnv(t)
	secret, _ := enable2FA(t, am, "alice")

	res, _ := am.BeginLogin("alice", "alice-password")
	for i := 0; i < challengeAttempts; i++ {
		_, _, _ = am.CompleteLogin(res.Challenge, "000000")
	}
	clock.t = clock.t.Add(30 * time.Second)
	if _, _, err := am.CompleteLogin(res.Challenge, currentCode(t, am, secret)); !errors.Is(err, ErrChallengeExpired) {
		t.Fatal("challenge not invalidated after too many attempts")
	}

	res, _ = am.BeginLogin("alice", "alice-password")
	clock.t = clock.t.Add(challengeLifetime + time.Second)
	if _, _, err := am.CompleteLogin(res.Challenge, currentCode(t, am, secret)); !errors.Is(err, ErrChallengeExpired) {
		t.Fatal("expired challenge accepted")
	}
	if _, _, err := am.CompleteLogin("unknown", "123456"); !errors.Is(err, ErrChallengeExpired) {
		t.Fatal("unknown challenge accepted")
	}
	// A wrong password never yields a challenge.
	if _, err := am.BeginLogin("alice", "wrong"); err == nil {
		t.Fatal("wrong password accepted")
	}
}

func TestSetupRequiresPasswordAndValidCode(t *testing.T) {
	am, _ := newTwoFactorEnv(t)
	if _, err := am.StartTwoFactorSetup("alice", "wrong", ""); err == nil {
		t.Fatal("setup started without password")
	}
	setup, _ := am.StartTwoFactorSetup("alice", "alice-password", "")
	if _, err := am.ConfirmTwoFactorSetup("alice", "000000", ""); err == nil {
		t.Fatal("setup confirmed with wrong code")
	}
	if am.TwoFactorStatusFor("alice").Enabled {
		t.Fatal("2FA enabled before confirmation")
	}
	// Bob cannot confirm Alice's pending setup.
	code, _ := TOTPCode(setup.Secret, am.now().Unix()/30)
	if _, err := am.ConfirmTwoFactorSetup("bob", code, ""); err == nil {
		t.Fatal("bob confirmed alice's setup")
	}
	if _, err := am.ConfirmTwoFactorSetup("alice", code, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := am.StartTwoFactorSetup("alice", "alice-password", ""); !errors.Is(err, ErrTwoFactorEnabled) {
		t.Fatal("second setup allowed while enabled")
	}
}

func TestDisableResetAndSessions(t *testing.T) {
	am, clock := newTwoFactorEnv(t)
	secret, codes := enable2FA(t, am, "alice")

	res, _ := am.BeginLogin("alice", "alice-password")
	clock.t = clock.t.Add(30 * time.Second)
	sid, _, _ := am.CompleteLogin(res.Challenge, currentCode(t, am, secret))

	if err := am.DisableTwoFactor("alice", "wrong", codes[0], sid); err == nil {
		t.Fatal("disabled without password")
	}
	if err := am.DisableTwoFactor("alice", "alice-password", "000000", sid); err == nil {
		t.Fatal("disabled without valid code")
	}
	// Bob's password does not help to disable Alice's 2FA.
	if err := am.DisableTwoFactor("alice", "bob-password", codes[0], sid); err == nil {
		t.Fatal("disabled with another user's password")
	}
	if err := am.DisableTwoFactor("alice", "alice-password", codes[0], sid); err != nil {
		t.Fatal(err)
	}
	if am.TwoFactorStatusFor("alice").Enabled || !am.IsValidSession(sid) {
		t.Fatal("disable failed or removed the current session")
	}

	// Administrator reset logs the user out everywhere.
	enable2FA(t, am, "alice")
	other, _ := am.createSession("alice")
	if err := am.ResetTwoFactor("alice"); err != nil {
		t.Fatal(err)
	}
	if am.IsValidSession(other) || am.TwoFactorStatusFor("alice").Enabled {
		t.Fatal("reset did not clear 2FA or sessions")
	}
	if _, err := am.Login("alice", "alice-password"); err != nil {
		t.Fatalf("login after reset: %v", err)
	}
}

func TestTwoFactorSecretsNeverExposedOrLeakedBetweenUsers(t *testing.T) {
	am, _ := newTwoFactorEnv(t)
	enable2FA(t, am, "alice")
	for _, u := range am.GetUsers() {
		s := u.Sanitized()
		if s.TOTPSecret != "" || s.RecoveryCodes != nil || s.PasswordHash != "" {
			t.Fatal("sanitized user still contains secrets")
		}
		if u.Username == "bob" && (u.TOTPSecret != "" || len(u.RecoveryCodes) != 0) {
			t.Fatal("bob received alice's 2FA data")
		}
	}
	// Persisted per user, survives a restart.
	data, _ := os.ReadFile(filepath.Join(filepath.Dir(am.configPath), "config.json"))
	var cfg Config
	_ = json.Unmarshal(data, &cfg)
	for _, u := range cfg.Users {
		if u.Username == "alice" && (!u.TOTPEnabled || u.TOTPSecret == "" || len(u.RecoveryCodes) != recoveryCodeCount) {
			t.Fatal("2FA not persisted for alice")
		}
		for _, h := range u.RecoveryCodes {
			if len(h) != 64 {
				t.Fatal("recovery codes not stored as hashes")
			}
		}
	}
}
