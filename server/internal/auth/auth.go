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
	"net/url"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/bcrypt"

	"postwerk/internal/store"
)

var (
	ErrInvalid       = errors.New("invalid credentials")
	ErrTOTPRequired  = errors.New("totp required")
	ErrBanned        = errors.New("too many failed attempts")
	ErrAccountLocked = errors.New("account disabled")
)

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

// NewAppPassword returns a human-friendly random password like abcd-efgh-ijkl-mnop.
func NewAppPassword() string {
	const alphabet = "abcdefghjkmnpqrstuvwxyz23456789"
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	var sb strings.Builder
	for i, c := range b {
		if i > 0 && i%4 == 0 {
			sb.WriteByte('-')
		}
		sb.WriteByte(alphabet[int(c)%len(alphabet)])
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

func ValidateTOTP(secret, code string) bool {
	code = strings.TrimSpace(code)
	if len(code) != 6 || secret == "" {
		return false
	}
	c := uint64(time.Now().Unix() / 30)
	for _, d := range []int64{-1, 0, 1} {
		if subtle.ConstantTimeCompare([]byte(totpAt(secret, uint64(int64(c)+d))), []byte(code)) == 1 {
			return true
		}
	}
	return false
}

// ---- brute-force protection ----

type Ban struct {
	IP       string    `json:"ip"`
	Reason   string    `json:"reason"`
	Until    time.Time `json:"until"`
	Failures int       `json:"failures"`
}

type Limits struct {
	MaxAttempts int
	Window      time.Duration
	BanFor      time.Duration
}

type Guard struct {
	mu       sync.Mutex
	failures map[string][]time.Time
	bans     map[string]*Ban
	limits   func() Limits
	OnBan    func(ip, reason string)
}

func NewGuard(limits func() Limits) *Guard {
	return &Guard{failures: map[string][]time.Time{}, bans: map[string]*Ban{}, limits: limits}
}

func (g *Guard) Banned(ip string) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	b, ok := g.bans[ip]
	if !ok {
		return false
	}
	if time.Now().After(b.Until) {
		delete(g.bans, ip)
		return false
	}
	return true
}

func (g *Guard) Fail(ip, reason string) {
	l := g.limits()
	g.mu.Lock()
	cutoff := time.Now().Add(-l.Window)
	list := g.failures[ip][:0]
	for _, t := range g.failures[ip] {
		if t.After(cutoff) {
			list = append(list, t)
		}
	}
	list = append(list, time.Now())
	g.failures[ip] = list
	banned := false
	if len(list) >= l.MaxAttempts {
		g.bans[ip] = &Ban{IP: ip, Reason: reason, Until: time.Now().Add(l.BanFor).UTC(), Failures: len(list)}
		delete(g.failures, ip)
		banned = true
	}
	g.mu.Unlock()
	if banned && g.OnBan != nil {
		g.OnBan(ip, reason)
	}
}

func (g *Guard) Success(ip string) {
	g.mu.Lock()
	delete(g.failures, ip)
	g.mu.Unlock()
}

func (g *Guard) List() []Ban {
	g.mu.Lock()
	defer g.mu.Unlock()
	out := []Ban{}
	for ip, b := range g.bans {
		if time.Now().After(b.Until) {
			delete(g.bans, ip)
			continue
		}
		out = append(out, *b)
	}
	return out
}

func (g *Guard) Unban(ip string) {
	g.mu.Lock()
	delete(g.bans, ip)
	delete(g.failures, ip)
	g.mu.Unlock()
}

// ---- credential verification ----

type Authenticator struct {
	Store *store.Store
	Guard *Guard
}

// Interactive verifies password (+TOTP) for the web login.
func (a *Authenticator) Interactive(ctx context.Context, ip, email, password, totp string) (*store.Account, error) {
	if a.Guard.Banned(ip) {
		return nil, ErrBanned
	}
	acct, err := a.Store.GetAccountByEmail(ctx, email)
	if errors.Is(err, store.ErrNotFound) {
		_ = bcrypt.CompareHashAndPassword(dummyHash, []byte(password))
	}
	if err != nil || !CheckPassword(acct.PasswordHash, password) {
		if err == nil || errors.Is(err, store.ErrNotFound) {
			a.Guard.Fail(ip, "web login")
			return nil, ErrInvalid
		}
		return nil, err
	}
	if acct.Disabled {
		return nil, ErrAccountLocked
	}
	if acct.TOTPEnabled {
		if totp == "" {
			return nil, ErrTOTPRequired
		}
		if !ValidateTOTP(acct.TOTPSecret, totp) {
			a.Guard.Fail(ip, "web login (2FA)")
			return nil, ErrInvalid
		}
	}
	a.Guard.Success(ip)
	return acct, nil
}

// Protocol verifies credentials for SMTP/POP3/IMAP. Accounts with 2FA must
// use an app password; others may use their main password or app passwords.
func (a *Authenticator) Protocol(ctx context.Context, ip, email, password, proto string) (*store.Account, error) {
	if a.Guard.Banned(ip) {
		return nil, ErrBanned
	}
	acct, err := a.Store.GetAccountByEmail(ctx, email)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			a.Guard.Fail(ip, proto+" auth")
			return nil, ErrInvalid
		}
		return nil, err
	}
	if acct.Disabled {
		return nil, ErrAccountLocked
	}
	if !acct.TOTPEnabled && CheckPassword(acct.PasswordHash, password) {
		a.Guard.Success(ip)
		return acct, nil
	}
	apps, err := a.Store.ListAppPasswords(ctx, acct.ID)
	if err != nil {
		return nil, err
	}
	normalized := NormalizeAppPassword(password)
	for _, p := range apps {
		if CheckPassword(p.Hash, normalized) {
			a.Store.TouchAppPassword(ctx, p.ID)
			a.Guard.Success(ip)
			return acct, nil
		}
	}
	a.Guard.Fail(ip, proto+" auth")
	return nil, ErrInvalid
}

// TOTPCode returns the current 6-digit code for a Base32 secret (used when
// Postwerk itself must answer a provider's 2FA prompt).
func TOTPCode(secret string) string {
	return totpAt(strings.ReplaceAll(strings.TrimSpace(secret), " ", ""), uint64(time.Now().Unix()/30))
}
