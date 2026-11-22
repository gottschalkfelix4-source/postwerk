// Package auth implements password hashing, TOTP, session tokens, app
// passwords, credential verification for all protocols and brute-force bans.
package auth

import (
	"context"
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
	"log/slog"
	"net/url"
	"strings"
	"time"

	"golang.org/x/crypto/bcrypt"

	"postwerk/internal/ldapdir"
	"postwerk/internal/store"
)

var (
	ErrInvalid       = errors.New("invalid credentials")
	ErrTOTPRequired  = errors.New("totp required")
	ErrBanned        = errors.New("too many failed attempts")
	ErrAccountLocked = errors.New("account disabled")
	// ErrTOTPReused: the code is valid but its time step was already used.
	ErrTOTPReused = errors.New("totp code already used")
	// ErrInvalidCode: the password was right but the 2FA code was not.
	ErrInvalidCode = errors.New("invalid second factor")
)

// MaxPasswordBytes is the longest password bcrypt can verify completely.
const MaxPasswordBytes = 72

func HashPassword(pw string) (string, error) {
	b, err := bcrypt.GenerateFromPassword([]byte(pw), 12)
	return string(b), err
}

func CheckPassword(hash, pw string) bool {
	return bcrypt.CompareHashAndPassword([]byte(hash), []byte(pw)) == nil
}

func RandomToken(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

func HashToken(tok string) string {
	s := sha256.Sum256([]byte(tok))
	return hex.EncodeToString(s[:])
}

const friendlyAlphabet = "abcdefghjkmnpqrstuvwxyz23456789"

// randomString returns n characters from friendlyAlphabet without modulo bias.
func randomString(n int) string {
	const limit = 256 - 256%len(friendlyAlphabet)
	out := make([]byte, 0, n)
	buf := make([]byte, 2*n)
	for len(out) < n {
		_, _ = rand.Read(buf)
		for _, c := range buf {
			if int(c) < limit && len(out) < n {
				out = append(out, friendlyAlphabet[int(c)%len(friendlyAlphabet)])
			}
		}
	}
	return string(out)
}

// NewAppPassword returns a human-friendly random password like abcd-efgh-ijkl-mnop.
func NewAppPassword() string {
	raw := randomString(16)
	var sb strings.Builder
	for i := 0; i < len(raw); i++ {
		if i > 0 && i%4 == 0 {
			sb.WriteByte('-')
		}
		sb.WriteByte(raw[i])
	}
	return sb.String()
}

// NormalizeAppPassword makes app passwords tolerant to case, spaces and dashes.
func NormalizeAppPassword(p string) string {
	return strings.ToLower(strings.NewReplacer(" ", "", "-", "").Replace(strings.TrimSpace(p)))
}

// dummyHash keeps the timing of unknown-user logins close to real ones.
var dummyHash, _ = bcrypt.GenerateFromPassword([]byte("postwerk-dummy"), 12)

// ---- TOTP (RFC 6238, SHA-1, 30 s, 6 digits) ----

func NewTOTPSecret() string {
	b := make([]byte, 20)
	_, _ = rand.Read(b)
	return base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(b)
}

func TOTPURL(issuer, account, secret string) string {
	return fmt.Sprintf("otpauth://totp/%s:%s?secret=%s&issuer=%s&algorithm=SHA1&digits=6&period=30",
		url.PathEscape(issuer), url.PathEscape(account), secret, url.QueryEscape(issuer))
}

func totpAt(secret string, counter uint64) string {
	key, err := base32.StdEncoding.WithPadding(base32.NoPadding).DecodeString(strings.ToUpper(secret))
	if err != nil {
		return ""
	}
	var msg [8]byte
	binary.BigEndian.PutUint64(msg[:], counter)
	h := hmac.New(sha1.New, key)
	h.Write(msg[:])
	sum := h.Sum(nil)
	off := sum[len(sum)-1] & 0x0f
	code := (binary.BigEndian.Uint32(sum[off:]) & 0x7fffffff) % 1000000
	return fmt.Sprintf("%06d", code)
}

// totpNow is replaceable in tests.
var totpNow = time.Now

// MatchTOTP checks code against the current time step and one step of clock
// drift in each direction and returns the matching time step.
func MatchTOTP(secret, code string) (int64, bool) {
	code = strings.TrimSpace(code)
	if len(code) != 6 || secret == "" {
		return 0, false
	}
	c := totpNow().Unix() / 30
	for _, d := range []int64{-1, 0, 1} {
		if subtle.ConstantTimeCompare([]byte(totpAt(secret, uint64(c+d))), []byte(code)) == 1 {
			return c + d, true
		}
	}
	return 0, false
}

// ValidateTOTP checks a code without replay protection (see
// Authenticator.VerifyTOTP for logins).
func ValidateTOTP(secret, code string) bool {
	_, ok := MatchTOTP(secret, code)
	return ok
}

// ---- recovery codes ----

// RecoveryCodeCount is the number of recovery codes generated at once.
const RecoveryCodeCount = 10

// NewRecoveryCodes returns fresh one-time recovery codes (like
// abcd-efgh-jkmn, about 59 bits each) and their hashes for storage.
func NewRecoveryCodes() (codes, hashes []string) {
	for i := 0; i < RecoveryCodeCount; i++ {
		raw := randomString(12)
		code := raw[0:4] + "-" + raw[4:8] + "-" + raw[8:12]
		codes = append(codes, code)
		hashes = append(hashes, HashRecoveryCode(code))
	}
	return codes, hashes
}

// HashRecoveryCode hashes a recovery code, ignoring case, spaces and dashes.
func HashRecoveryCode(code string) string {
	return HashToken("recovery:" + NormalizeAppPassword(code))
}

func isTOTPCode(code string) bool {
	if len(code) != 6 {
		return false
	}
	for _, c := range code {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}

// ---- credential verification ----

type Authenticator struct {
	Store *store.Store
	Guard *Guard
	// Settings (optional) enable LDAP accounts and the "only SSO" rule.
	Settings *store.SettingsCache
	// LDAP replaces the directory client (tests); nil uses ldapdir.New().
	LDAP ldapdir.Directory
	Log  *slog.Logger
}

// VerifyTOTP checks a TOTP code for acct and marks its time step as used, so
// a code can never be used twice. It returns ErrTOTPReused for a replayed code.
func (a *Authenticator) VerifyTOTP(ctx context.Context, acct *store.Account, code string) (bool, error) {
	step, ok := MatchTOTP(acct.TOTPSecret, code)
	if !ok {
		return false, nil
	}
	fresh, err := a.Store.UseTOTPStep(ctx, acct.ID, step)
	if err != nil {
		return false, err
	}
	if !fresh {
		return false, ErrTOTPReused
	}
	return true, nil
}

// VerifySecondFactor accepts either a 6-digit TOTP code or an unused
// recovery code (which is consumed).
func (a *Authenticator) VerifySecondFactor(ctx context.Context, acct *store.Account, code string) (bool, error) {
	code = strings.TrimSpace(code)
	if code == "" {
		return false, nil
	}
	if isTOTPCode(code) {
		return a.VerifyTOTP(ctx, acct, code)
	}
	return a.Store.UseRecoveryCode(ctx, acct.ID, HashRecoveryCode(code))
}

// Interactive verifies password (+TOTP or recovery code) for the web login.
func (a *Authenticator) Interactive(ctx context.Context, ip, email, password, totp string) (*store.Account, error) {
	email = store.Normalize(email)
	if a.Guard.Banned(ip) || a.Guard.AccountLocked(ip, email) {
		return nil, ErrBanned
	}
	acct, err := a.Store.GetAccountByEmail(ctx, email)
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		return nil, err
	}
	if err != nil {
		acct = nil
	}
	acct, ok, err := a.primary(ctx, email, password, acct)
	if err != nil {
		return nil, err
	}
	if !ok {
		a.Guard.FailAccount(ip, email, "web login")
		return nil, ErrInvalid
	}
	if acct.Disabled {
		return nil, ErrAccountLocked
	}
	if !a.PasswordLoginAllowed(acct) {
		return nil, ErrPasswordLoginDisabled
	}
	if acct.TOTPEnabled {
		if strings.TrimSpace(totp) == "" {
			return nil, ErrTOTPRequired
		}
		ok, err := a.VerifySecondFactor(ctx, acct, totp)
		if err != nil && !errors.Is(err, ErrTOTPReused) {
			return nil, err
		}
		if !ok {
			a.Guard.FailAccount(ip, email, "web login (2FA)")
			return nil, ErrInvalid
		}
	}
	a.Guard.Success(ip, acct.Email)
	return acct, nil
}

// Reauth confirms the identity of a signed-in user before a sensitive change:
// the current password and, if needCode is set and 2FA is active, a TOTP or
// recovery code. Failures count towards the brute-force limits.
func (a *Authenticator) Reauth(ctx context.Context, ip string, acct *store.Account, password, code string, needCode bool) error {
	if a.Guard.Banned(ip) || a.Guard.AccountLocked(ip, acct.Email) {
		return ErrBanned
	}
	_, ok, err := a.primary(ctx, acct.Email, password, acct)
	if err != nil {
		return err
	}
	if !ok {
		a.Guard.FailAccount(ip, acct.Email, "password confirmation")
		return ErrInvalid
	}
	if needCode && acct.TOTPEnabled {
		if strings.TrimSpace(code) == "" {
			return ErrTOTPRequired
		}
		ok, err := a.VerifySecondFactor(ctx, acct, code)
		if err != nil {
			return err
		}
		if !ok {
			a.Guard.FailAccount(ip, acct.Email, "2FA confirmation")
			return ErrInvalidCode
		}
	}
	a.Guard.Success(ip, acct.Email)
	return nil
}

// Protocol verifies credentials for SMTP/POP3/IMAP. Accounts with 2FA must
// use an app password; others may use their main password or app passwords.
// Unknown users cost the same bcrypt work as known ones, and the disabled
// state is only revealed after a correct password.
func (a *Authenticator) Protocol(ctx context.Context, ip, email, password, proto string) (*store.Account, error) {
	email = store.Normalize(email)
	if a.Guard.Banned(ip) || a.Guard.AccountLocked(ip, email) {
		return nil, ErrBanned
	}
	acct, err := a.Store.GetAccountByEmail(ctx, email)
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		return nil, err
	}
	if err != nil {
		// Unknown address: only a directory user can be provisioned here.
		acct, ok, err := a.primary(ctx, email, password, nil)
		if err != nil {
			return nil, err
		}
		if !ok {
			a.Guard.FailAccount(ip, email, proto+" auth")
			return nil, ErrInvalid
		}
		if acct.Disabled {
			return nil, ErrAccountLocked
		}
		a.Guard.Success(ip, acct.Email)
		return acct, nil
	}
	ok := false
	// For LDAP accounts, app passwords are tried before the directory bind.
	appFirst := acct.Source == store.SourceLDAP && looksLikeAppPassword(password)
	if appFirst {
		if ok, err = a.appPassword(ctx, acct, password); err != nil {
			return nil, err
		}
	}
	var dirErr error
	if !ok {
		if acct.TOTPEnabled {
			// The main password is not accepted, but costs the same time.
			_ = bcrypt.CompareHashAndPassword(dummyHash, []byte(password))
		} else {
			_, ok, dirErr = a.primary(ctx, email, password, acct)
			if dirErr != nil && !errors.Is(dirErr, ErrDirectoryUnavailable) {
				return nil, dirErr
			}
		}
	}
	if !ok && !appFirst {
		if ok, err = a.appPassword(ctx, acct, password); err != nil {
			return nil, err
		}
	}
	if !ok && dirErr != nil {
		return nil, dirErr
	}
	if !ok {
		a.Guard.FailAccount(ip, email, proto+" auth")
		return nil, ErrInvalid
	}
	if acct.Disabled {
		return nil, ErrAccountLocked
	}
	a.Guard.Success(ip, acct.Email)
	return acct, nil
}

// TOTPCode returns the current 6-digit code for a Base32 secret (used when
// Postwerk itself must answer a provider's 2FA prompt).
func TOTPCode(secret string) string {
	return totpAt(strings.ReplaceAll(strings.TrimSpace(secret), " ", ""), uint64(time.Now().Unix()/30))
}
