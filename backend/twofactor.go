package backend

// Two-factor authentication with time-based one-time passwords (TOTP, RFC 6238), compatible with
// Google Authenticator, Microsoft Authenticator, Authy, Aegis, 1Password, Bitwarden and others.
//
// Every user has an independent secret and independent recovery codes stored in the own user
// record. The second login step only receives a challenge token that is bound to the user whose
// password was verified; it never accepts a username, so codes are only ever checked against the
// 2FA data of that one account. Recovery codes are additionally hashed together with the username.

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha1"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base32"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"
)

const (
	totpPeriod        = 30 // seconds
	totpDigits        = 6
	totpSkew          = 1 // accepted time steps before/after the current one
	recoveryCodeCount = 10
	challengeLifetime = 5 * time.Minute
	challengeAttempts = 5
	setupLifetime     = 15 * time.Minute
	// TOTPIssuer is shown in the authenticator app.
	TOTPIssuer = "LinuxGSM Dashboard"
)

// 2FA errors.
var (
	ErrTwoFactorRequired = errors.New("two-factor authentication required")
	ErrInvalidCode       = errors.New("invalid two-factor code")
	ErrChallengeExpired  = errors.New("login expired, please sign in again")
	ErrTwoFactorEnabled  = errors.New("two-factor authentication is already enabled")
	ErrTwoFactorDisabled = errors.New("two-factor authentication is not enabled")
)

var base32NoPad = base32.StdEncoding.WithPadding(base32.NoPadding)

// GenerateTOTPSecret returns a random 160 bit secret in base32 (the format authenticator apps use).
func GenerateTOTPSecret() (string, error) {
	b := make([]byte, 20)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base32NoPad.EncodeToString(b), nil
}

// TOTPCode computes the code for a time step.
func TOTPCode(secret string, step int64) (string, error) {
	key, err := base32NoPad.DecodeString(strings.ToUpper(strings.TrimSpace(secret)))
	if err != nil || len(key) == 0 {
		return "", errors.New("invalid TOTP secret")
	}
	var msg [8]byte
	binary.BigEndian.PutUint64(msg[:], uint64(step))
	mac := hmac.New(sha1.New, key)
	mac.Write(msg[:])
	sum := mac.Sum(nil)
	offset := sum[len(sum)-1] & 0x0f
	value := binary.BigEndian.Uint32(sum[offset:offset+4]) & 0x7fffffff
	return fmt.Sprintf("%06d", value%1000000), nil
}

// verifyTOTP checks code against the steps around now. Steps <= lastStep were already used and are
// rejected (replay protection). It returns the matching step.
func verifyTOTP(secret, code string, now time.Time, lastStep int64) (int64, bool) {
	code = strings.ReplaceAll(strings.TrimSpace(code), " ", "")
	if len(code) != totpDigits {
		return 0, false
	}
	for _, c := range code {
		if c < '0' || c > '9' {
			return 0, false
		}
	}
	current := now.Unix() / totpPeriod
	for delta := int64(-totpSkew); delta <= totpSkew; delta++ {
		step := current + delta
		if step <= lastStep {
			continue
		}
		want, err := TOTPCode(secret, step)
		if err != nil {
			return 0, false
		}
		if subtle.ConstantTimeCompare([]byte(want), []byte(code)) == 1 {
			return step, true
		}
	}
	return 0, false
}

// TOTPURI builds the otpauth:// URI encoded in the QR code.
func TOTPURI(secret, account, host string) string {
	label := TOTPIssuer + ":" + account
	if host != "" {
		label = TOTPIssuer + ":" + account + "@" + host
	}
	q := url.Values{}
	q.Set("secret", secret)
	q.Set("issuer", TOTPIssuer)
	q.Set("algorithm", "SHA1")
	q.Set("digits", fmt.Sprint(totpDigits))
	q.Set("period", fmt.Sprint(totpPeriod))
	return "otpauth://totp/" + url.PathEscape(label) + "?" + q.Encode()
}

// -------------------------------------------------------------
// Recovery codes
// -------------------------------------------------------------

var recoveryAlphabet = []byte("abcdefghjkmnpqrstuvwxyz23456789") // no 0/o/1/l/i

func generateRecoveryCodes() ([]string, error) {
	codes := make([]string, recoveryCodeCount)
	for i := range codes {
		buf := make([]byte, 10)
		if _, err := rand.Read(buf); err != nil {
			return nil, err
		}
		for j := range buf {
			buf[j] = recoveryAlphabet[int(buf[j])%len(recoveryAlphabet)]
		}
		codes[i] = string(buf[:5]) + "-" + string(buf[5:])
	}
	return codes, nil
}

func normalizeRecoveryCode(code string) string {
	code = strings.ToLower(strings.TrimSpace(code))
	return strings.NewReplacer("-", "", " ", "").Replace(code)
}

// hashRecoveryCode binds a recovery code to its user, so a code can never match another account.
func hashRecoveryCode(username, code string) string {
	sum := sha256.Sum256([]byte("lgsm-recovery\x00" + username + "\x00" + normalizeRecoveryCode(code)))
	return hex.EncodeToString(sum[:])
}

// -------------------------------------------------------------
// AuthManager integration
// -------------------------------------------------------------

type loginChallenge struct {
	username string
	expiry   time.Time
	attempts int
}

type pendingSetup struct {
	secret string
	expiry time.Time
}

// TwoFactorStatus is shown in the account settings.
type TwoFactorStatus struct {
	Enabled           bool `json:"enabled"`
	RecoveryCodesLeft int  `json:"recovery_codes_left"`
}

// TwoFactorSetup is returned when a user starts the setup.
type TwoFactorSetup struct {
	Secret string `json:"secret"`
	URI    string `json:"uri"`
}

// LoginResult is either a session (no 2FA) or a challenge for the second step.
type LoginResult struct {
	SessionID string
	Challenge string
}

// BeginLogin checks username and password. Users with 2FA get a challenge token instead of a
// session; the challenge is bound to this user and can only be completed with this user's codes.
func (am *AuthManager) BeginLogin(username, password string) (LoginResult, error) {
	if err := am.verifyCredentials(username, password); err != nil {
		return LoginResult{}, err
	}
	am.mu.RLock()
	enabled := false
	for _, u := range am.config.Users {
		if u.Username == username {
			enabled = u.TOTPEnabled
			break
		}
	}
	am.mu.RUnlock()

	if !enabled {
		sid, err := am.createSession(username)
		return LoginResult{SessionID: sid}, err
	}
	token, err := generateRandomHex(32)
	if err != nil {
		return LoginResult{}, err
	}
	am.mu.Lock()
	am.challenges[token] = loginChallenge{username: username, expiry: am.now().Add(challengeLifetime)}
	am.mu.Unlock()
	return LoginResult{Challenge: token}, nil
}

// ChallengeUser returns the user a pending challenge belongs to (for rate limiting and logging).
func (am *AuthManager) ChallengeUser(token string) (string, bool) {
	am.mu.RLock()
	defer am.mu.RUnlock()
	c, ok := am.challenges[token]
	if !ok || am.now().After(c.expiry) {
		return "", false
	}
	return c.username, true
}

// CompleteLogin finishes a login with a TOTP or recovery code of the challenged user.
// It returns the session ID and whether a recovery code was used.
func (am *AuthManager) CompleteLogin(token, code string) (string, bool, error) {
	am.mu.Lock()
	c, ok := am.challenges[token]
	if !ok || am.now().After(c.expiry) {
		delete(am.challenges, token)
		am.mu.Unlock()
		return "", false, ErrChallengeExpired
	}
	c.attempts++
	if c.attempts > challengeAttempts {
		delete(am.challenges, token)
		am.mu.Unlock()
		return "", false, ErrChallengeExpired
	}
	am.challenges[token] = c

	usedRecovery, err := am.consumeSecondFactorLocked(c.username, code)
	if err != nil {
		am.mu.Unlock()
		return "", false, err
	}
	delete(am.challenges, token)
	am.mu.Unlock()

	sid, err := am.createSession(c.username)
	return sid, usedRecovery, err
}

// consumeSecondFactorLocked verifies a TOTP or recovery code of exactly this user and records its
// use (TOTP replay protection, one-time recovery codes). am.mu must be held for writing.
func (am *AuthManager) consumeSecondFactorLocked(username, code string) (bool, error) {
	idx := -1
	for i := range am.config.Users {
		if am.config.Users[i].Username == username {
			idx = i
			break
		}
	}
	if idx < 0 || !am.config.Users[idx].TOTPEnabled {
		return false, ErrInvalidCode
	}
	u := &am.config.Users[idx]

	if step, ok := verifyTOTP(u.TOTPSecret, code, am.now(), u.TOTPLastStep); ok {
		prev := u.TOTPLastStep
		u.TOTPLastStep = step
		if err := am.saveConfigLocked(); err != nil {
			u.TOTPLastStep = prev
			return false, err
		}
		return false, nil
	}

	hash := hashRecoveryCode(username, code)
	for i, h := range u.RecoveryCodes {
		if subtle.ConstantTimeCompare([]byte(h), []byte(hash)) == 1 {
			prev := append([]string(nil), u.RecoveryCodes...)
			u.RecoveryCodes = append(u.RecoveryCodes[:i:i], u.RecoveryCodes[i+1:]...)
			if err := am.saveConfigLocked(); err != nil {
				u.RecoveryCodes = prev
				return false, err
			}
			return true, nil
		}
	}
	return false, ErrInvalidCode
}

// TwoFactorStatusFor returns the 2FA state of a user.
func (am *AuthManager) TwoFactorStatusFor(username string) TwoFactorStatus {
	am.mu.RLock()
	defer am.mu.RUnlock()
	for _, u := range am.config.Users {
		if u.Username == username {
			return TwoFactorStatus{Enabled: u.TOTPEnabled, RecoveryCodesLeft: len(u.RecoveryCodes)}
		}
	}
	return TwoFactorStatus{}
}

// StartTwoFactorSetup creates a new secret for the user after re-checking the password. The secret
// only becomes active after ConfirmTwoFactorSetup with a valid code.
func (am *AuthManager) StartTwoFactorSetup(username, password, host string) (TwoFactorSetup, error) {
	if err := am.verifyCredentials(username, password); err != nil {
		return TwoFactorSetup{}, errors.New("current password incorrect")
	}
	if am.TwoFactorStatusFor(username).Enabled {
		return TwoFactorSetup{}, ErrTwoFactorEnabled
	}
	secret, err := GenerateTOTPSecret()
	if err != nil {
		return TwoFactorSetup{}, err
	}
	am.mu.Lock()
	am.setups[username] = pendingSetup{secret: secret, expiry: am.now().Add(setupLifetime)}
	am.mu.Unlock()
	return TwoFactorSetup{Secret: secret, URI: TOTPURI(secret, username, host)}, nil
}

// ConfirmTwoFactorSetup enables 2FA when the code matches the pending secret of this user. It
// returns the recovery codes (shown once). Other sessions of the user are logged out.
func (am *AuthManager) ConfirmTwoFactorSetup(username, code, keepSessionID string) ([]string, error) {
	am.mu.Lock()
	defer am.mu.Unlock()

	setup, ok := am.setups[username]
	if !ok || am.now().After(setup.expiry) {
		delete(am.setups, username)
		return nil, errors.New("setup expired, please start again")
	}
	step, valid := verifyTOTP(setup.secret, code, am.now(), 0)
	if !valid {
		return nil, ErrInvalidCode
	}
	codes, err := generateRecoveryCodes()
	if err != nil {
		return nil, err
	}
	for i := range am.config.Users {
		u := &am.config.Users[i]
		if u.Username != username {
			continue
		}
		if u.TOTPEnabled {
			return nil, ErrTwoFactorEnabled
		}
		prev := *u
		u.TOTPEnabled = true
		u.TOTPSecret = setup.secret
		u.TOTPLastStep = step
		u.RecoveryCodes = make([]string, len(codes))
		for j, c := range codes {
			u.RecoveryCodes[j] = hashRecoveryCode(username, c)
		}
		if err := am.saveConfigLocked(); err != nil {
			*u = prev
			return nil, err
		}
		delete(am.setups, username)
		am.invalidateUserSessionsLocked(username, keepSessionID)
		return codes, nil
	}
	return nil, errors.New("user not found")
}

// DisableTwoFactor turns 2FA off after checking password and a current code of the same user.
func (am *AuthManager) DisableTwoFactor(username, password, code, keepSessionID string) error {
	if err := am.verifyCredentials(username, password); err != nil {
		return errors.New("current password incorrect")
	}
	am.mu.Lock()
	defer am.mu.Unlock()
	if _, err := am.consumeSecondFactorLocked(username, code); err != nil {
		return err
	}
	return am.clearTwoFactorLocked(username, keepSessionID)
}

// RegenerateRecoveryCodes replaces all recovery codes after checking password and a current code.
func (am *AuthManager) RegenerateRecoveryCodes(username, password, code string) ([]string, error) {
	if err := am.verifyCredentials(username, password); err != nil {
		return nil, errors.New("current password incorrect")
	}
	am.mu.Lock()
	defer am.mu.Unlock()
	if _, err := am.consumeSecondFactorLocked(username, code); err != nil {
		return nil, err
	}
	codes, err := generateRecoveryCodes()
	if err != nil {
		return nil, err
	}
	for i := range am.config.Users {
		u := &am.config.Users[i]
		if u.Username != username {
			continue
		}
		prev := u.RecoveryCodes
		u.RecoveryCodes = make([]string, len(codes))
		for j, c := range codes {
			u.RecoveryCodes[j] = hashRecoveryCode(username, c)
		}
		if err := am.saveConfigLocked(); err != nil {
			u.RecoveryCodes = prev
			return nil, err
		}
		return codes, nil
	}
	return nil, errors.New("user not found")
}

// ResetTwoFactor is the administrator action for users who lost their device. All sessions of the
// user are logged out; the user still needs the own password to sign in again.
func (am *AuthManager) ResetTwoFactor(username string) error {
	am.mu.Lock()
	defer am.mu.Unlock()
	return am.clearTwoFactorLocked(username, "")
}

func (am *AuthManager) clearTwoFactorLocked(username, keepSessionID string) error {
	for i := range am.config.Users {
		u := &am.config.Users[i]
		if u.Username != username {
			continue
		}
		if !u.TOTPEnabled {
			return ErrTwoFactorDisabled
		}
		prev := *u
		u.TOTPEnabled = false
		u.TOTPSecret = ""
		u.TOTPLastStep = 0
		u.RecoveryCodes = nil
		if err := am.saveConfigLocked(); err != nil {
			*u = prev
			return err
		}
		am.invalidateUserSessionsLocked(username, keepSessionID)
		for token, c := range am.challenges {
			if c.username == username {
				delete(am.challenges, token)
			}
		}
		return nil
	}
	return errors.New("user not found")
}
