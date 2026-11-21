package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"postwerk/internal/auth"
	"postwerk/internal/config"
	"postwerk/internal/store"
)

const testPassword = "ein-sehr-gutes-passwort"

type testEnv struct {
	s    *Server
	h    http.Handler
	acct *store.Account
	dir  string
}

func newAuthEnv(t *testing.T) *testEnv {
	t.Helper()
	dir := t.TempDir()
	st, err := store.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	settings, err := st.LoadSettings(context.Background(), "mail.example.test")
	if err != nil {
		t.Fatal(err)
	}
	h, _ := auth.HashPassword(testPassword)
	acct := &store.Account{Email: "anna@example.test", Name: "Anna", PasswordHash: h, Role: "user"}
	if err := st.CreateAccount(context.Background(), acct); err != nil {
		t.Fatal(err)
	}
	guard := auth.NewGuard(func() auth.Limits { return auth.Limits{MaxAttempts: 50, Window: time.Minute, BanFor: time.Minute} })
	s := &Server{Cfg: &config.Config{DataDir: dir}, Store: st, Settings: settings, Auth: &auth.Authenticator{Store: st, Guard: guard},
		Log: slog.New(slog.NewTextHandler(io.Discard, nil)), UI: fstest.MapFS{}}
	return &testEnv{s: s, h: s.Handler(), acct: acct, dir: dir}
}

func (e *testEnv) do(t *testing.T, method, path string, body any, c *http.Cookie) *httptest.ResponseRecorder {
	t.Helper()
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	r := httptest.NewRequest(method, path, rd)
	r.RemoteAddr = "198.51.100.10:5555"
	r.Header.Set("X-Postwerk", "1")
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("User-Agent", "TestBrowser/1.0")
	if c != nil {
		r.AddCookie(c)
	}
	rec := httptest.NewRecorder()
	e.h.ServeHTTP(rec, r)
	return rec
}

func sessionFrom(t *testing.T, rec *httptest.ResponseRecorder) *http.Cookie {
	t.Helper()
	for _, c := range rec.Result().Cookies() {
		if c.Name == sessionCookie && c.Value != "" {
			return c
		}
	}
	t.Fatalf("no session cookie (status %d: %s)", rec.Code, rec.Body.String())
	return nil
}

func (e *testEnv) login(t *testing.T, totp string) *http.Cookie {
	t.Helper()
	rec := e.do(t, "POST", "/api/auth/login", map[string]string{"email": e.acct.Email, "password": testPassword, "totp": totp}, nil)
	if rec.Code != 200 {
		t.Fatalf("login: %d %s", rec.Code, rec.Body.String())
	}
	return sessionFrom(t, rec)
}

func errCode(rec *httptest.ResponseRecorder) string {
	var body struct{ Error string }
	_ = json.Unmarshal(rec.Body.Bytes(), &body)
	return body.Error
}

func TestInternalErrorIsGeneric(t *testing.T) {
	e := newAuthEnv(t)
	rec := httptest.NewRecorder()
	e.s.writeErr(rec, httptest.NewRequest("GET", "/api/x", nil), errors.New("open /data/secret.db: permission denied"))
	if rec.Code != 500 {
		t.Fatalf("status %d", rec.Code)
	}
	body := rec.Body.String()
	if strings.Contains(body, "secret.db") || !strings.Contains(body, "Referenz") {
		t.Fatalf("internal details leaked or reference missing: %s", body)
	}
}

func TestPasswordChangeEndsOtherSessionsAndAppPasswords(t *testing.T) {
	e := newAuthEnv(t)
	c1, c2 := e.login(t, ""), e.login(t, "")

	// Creating an app password needs the current password.
	if rec := e.do(t, "POST", "/api/account/app-passwords", map[string]string{"name": "Laptop"}, c1); rec.Code != 400 {
		t.Fatalf("app password without re-authentication: %d", rec.Code)
	}
	if rec := e.do(t, "POST", "/api/account/app-passwords", map[string]string{"name": "Laptop", "password": testPassword}, c1); rec.Code != 200 {
		t.Fatalf("create app password: %d %s", rec.Code, rec.Body.String())
	}

	rec := e.do(t, "POST", "/api/account/password", map[string]any{"current": testPassword, "new": "ganz-neues-passwort", "revokeAppPasswords": true}, c1)
	if rec.Code != 200 {
		t.Fatalf("change password: %d %s", rec.Code, rec.Body.String())
	}
	fresh := sessionFrom(t, rec)
	for name, c := range map[string]*http.Cookie{"other": c2, "old own": c1} {
		if r := e.do(t, "GET", "/api/auth/me", nil, c); r.Code != 401 {
			t.Errorf("%s session still valid after password change: %d", name, r.Code)
		}
	}
	if r := e.do(t, "GET", "/api/auth/me", nil, fresh); r.Code != 200 {
		t.Fatalf("reissued session invalid: %d", r.Code)
	}
	if apps, _ := e.s.Store.ListAppPasswords(context.Background(), e.acct.ID); len(apps) != 0 {
		t.Fatalf("app passwords survived: %d", len(apps))
	}
}

func TestWrongCurrentPasswordCountsAsFailure(t *testing.T) {
	e := newAuthEnv(t)
	e.s.Auth.Guard = auth.NewGuard(func() auth.Limits { return auth.Limits{MaxAttempts: 2, Window: time.Minute, BanFor: time.Minute} })
	c := e.login(t, "")
	for i := 0; i < 2; i++ {
		e.do(t, "POST", "/api/account/password", map[string]any{"current": "falsch", "new": "ganz-neues-passwort"}, c)
	}
	rec := e.do(t, "POST", "/api/account/password", map[string]any{"current": testPassword, "new": "ganz-neues-passwort"}, c)
	if rec.Code != 429 {
		t.Fatalf("password guessing via a stolen session is not limited: %d", rec.Code)
	}
}

func TestSessionsOverviewAndLogout(t *testing.T) {
	e := newAuthEnv(t)
	c1, c2, c3 := e.login(t, ""), e.login(t, ""), e.login(t, "")
	rec := e.do(t, "GET", "/api/account/sessions", nil, c1)
	var list []store.Session
	_ = json.Unmarshal(rec.Body.Bytes(), &list)
	if len(list) != 3 {
		t.Fatalf("sessions = %d", len(list))
	}
	current := 0
	var other string
	for _, se := range list {
		if se.Current {
			current++
		} else if other == "" {
			other = se.ID
		}
		if se.IP != "198.51.100.10" || se.UserAgent != "TestBrowser/1.0" {
			t.Fatalf("session metadata missing: %+v", se)
		}
	}
	if current != 1 {
		t.Fatalf("current sessions = %d", current)
	}
	if r := e.do(t, "DELETE", "/api/account/sessions/"+other, nil, c1); r.Code != 200 {
		t.Fatalf("delete session: %d", r.Code)
	}
	if r := e.do(t, "POST", "/api/account/sessions/logout-others", nil, c1); r.Code != 200 {
		t.Fatalf("logout others: %d", r.Code)
	}
	for _, c := range []*http.Cookie{c2, c3} {
		if r := e.do(t, "GET", "/api/auth/me", nil, c); r.Code != 401 {
			t.Fatalf("session survived: %d", r.Code)
		}
	}
	if r := e.do(t, "GET", "/api/auth/me", nil, c1); r.Code != 200 {
		t.Fatalf("own session ended: %d", r.Code)
	}
}

func TestSessionIdleTimeout(t *testing.T) {
	e := newAuthEnv(t)
	c := e.login(t, "")
	old := time.Now().Add(-store.SessionIdleTimeout - time.Hour).Unix()
	if _, err := e.s.Store.DB.Exec(`UPDATE sessions SET last_used_at = ?, created_at = ?`, old, old); err != nil {
		t.Fatal(err)
	}
	if r := e.do(t, "GET", "/api/auth/me", nil, c); r.Code != 401 {
		t.Fatalf("idle session still valid: %d", r.Code)
	}
}

func TestTOTPLifecycleWithRecoveryCodes(t *testing.T) {
	e := newAuthEnv(t)
	c1, c2 := e.login(t, ""), e.login(t, "")
	rec := e.do(t, "POST", "/api/account/totp/setup", nil, c1)
	var setup struct{ Secret string }
	_ = json.Unmarshal(rec.Body.Bytes(), &setup)
	code := auth.TOTPCode(setup.Secret)

	if r := e.do(t, "POST", "/api/account/totp/enable", map[string]string{"code": code}, c1); r.Code != 400 {
		t.Fatalf("enable without password: %d", r.Code)
	}
	rec = e.do(t, "POST", "/api/account/totp/enable", map[string]string{"code": code, "password": testPassword}, c1)
	if rec.Code != 200 {
		t.Fatalf("enable: %d %s", rec.Code, rec.Body.String())
	}
	var enabled struct{ RecoveryCodes []string }
	_ = json.Unmarshal(rec.Body.Bytes(), &enabled)
	if len(enabled.RecoveryCodes) != auth.RecoveryCodeCount {
		t.Fatalf("recovery codes = %d", len(enabled.RecoveryCodes))
	}
	if r := e.do(t, "GET", "/api/auth/me", nil, c2); r.Code != 401 {
		t.Fatalf("other session survived enabling 2FA: %d", r.Code)
	}

	// Login with a recovery code instead of a TOTP code.
	c3 := e.login(t, enabled.RecoveryCodes[0])
	if r := e.do(t, "GET", "/api/account/recovery-codes", nil, c3); !strings.Contains(r.Body.String(), `"remaining":9`) {
		t.Fatalf("remaining codes: %s", r.Body.String())
	}

	// Disabling needs a second factor in addition to the password.
	if r := e.do(t, "POST", "/api/account/totp/disable", map[string]string{"password": testPassword}, c1); r.Code != 400 || errCode(r) != "totp_required" {
		t.Fatalf("disable without code: %d %s", r.Code, r.Body.String())
	}
	if r := e.do(t, "POST", "/api/account/totp/disable", map[string]string{"password": testPassword, "code": enabled.RecoveryCodes[0]}, c1); r.Code != 400 {
		t.Fatalf("used recovery code accepted: %d", r.Code)
	}
	if r := e.do(t, "POST", "/api/account/totp/disable", map[string]string{"password": testPassword, "code": enabled.RecoveryCodes[1]}, c1); r.Code != 200 {
		t.Fatalf("disable: %d %s", r.Code, r.Body.String())
	}
	if r := e.do(t, "GET", "/api/auth/me", nil, c3); r.Code != 401 {
		t.Fatalf("other session survived disabling 2FA: %d", r.Code)
	}
}

func TestMustChangePassword(t *testing.T) {
	e := newAuthEnv(t)
	on := true
	_ = e.s.Store.UpdateAccount(context.Background(), e.acct.ID, store.AccountPatch{MustChangePassword: &on})
	file := filepath.Join(e.dir, AdminPasswordFile)
	_ = os.WriteFile(file, []byte("x\n"), 0o600)
	c := e.login(t, "")
	if r := e.do(t, "GET", "/api/auth/me", nil, c); !strings.Contains(r.Body.String(), `"mustChangePassword":true`) {
		t.Fatalf("me: %s", r.Body.String())
	}
	if r := e.do(t, "GET", "/api/mail/mailboxes", nil, c); r.Code != 403 || errCode(r) != "password_change_required" {
		t.Fatalf("API usable before the password change: %d %s", r.Code, r.Body.String())
	}
	rec := e.do(t, "POST", "/api/account/password", map[string]any{"current": testPassword, "new": "ganz-neues-passwort"}, c)
	if rec.Code != 200 {
		t.Fatalf("change: %d %s", rec.Code, rec.Body.String())
	}
	c = sessionFrom(t, rec)
	if r := e.do(t, "GET", "/api/mail/mailboxes", nil, c); r.Code != 200 {
		t.Fatalf("API still blocked: %d %s", r.Code, r.Body.String())
	}
	if _, err := os.Stat(file); !os.IsNotExist(err) {
		t.Fatal("generated password file not removed")
	}
}

func TestBodyLimitsAndPasswordLength(t *testing.T) {
	e := newAuthEnv(t)
	big := map[string]string{"email": e.acct.Email, "password": strings.Repeat("x", 100<<10)}
	if r := e.do(t, "POST", "/api/auth/login", big, nil); r.Code != 413 {
		t.Fatalf("oversized login body: %d", r.Code)
	}
	if err := validPassword(strings.Repeat("ä", 40)); err == nil {
		t.Fatal("passwords over 72 bytes must be rejected")
	}
}

func TestUnbanIPv6Network(t *testing.T) {
	e := newAuthEnv(t)
	admin := &store.Account{Email: "admin@example.test", PasswordHash: e.acct.PasswordHash, Role: "admin"}
	if err := e.s.Store.CreateAccount(context.Background(), admin); err != nil {
		t.Fatal(err)
	}
	rec := e.do(t, "POST", "/api/auth/login", map[string]string{"email": admin.Email, "password": testPassword}, nil)
	c := sessionFrom(t, rec)
	for i := 0; i < 50; i++ {
		e.s.Auth.Guard.Fail("2001:db8:5:6::1", "test")
	}
	if !e.s.Auth.Guard.Banned("2001:db8:5:6::99") {
		t.Fatal("not banned")
	}
	// The ban key contains a slash; the UI sends it URL-encoded.
	if r := e.do(t, "DELETE", "/api/admin/security/bans/2001:db8:5:6::%2F64", nil, c); r.Code != 200 {
		t.Fatalf("unban: %d %s", r.Code, r.Body.String())
	}
	if e.s.Auth.Guard.Banned("2001:db8:5:6::1") {
		t.Fatal("still banned")
	}
}

func TestMetricsHiddenWithoutToken(t *testing.T) {
	e := newAuthEnv(t)
	if r := e.do(t, "GET", "/metrics", nil, nil); r.Code != 404 {
		t.Fatalf("metrics without token: %d", r.Code)
	}
	e.s.Cfg.MetricsToken = "geheim"
	h := e.s.Handler()
	req := httptest.NewRequest("GET", "/metrics", nil)
	req.Header.Set("Authorization", "Bearer falsch")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != 401 {
		t.Fatalf("wrong token: %d", rec.Code)
	}
	req.Header.Set("Authorization", "Bearer geheim")
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Fatalf("right token: %d", rec.Code)
	}
}
