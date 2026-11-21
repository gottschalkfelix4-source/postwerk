package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"postwerk/internal/auth"
	"postwerk/internal/ldapdir/ldaptest"
	"postwerk/internal/sso/ssotest"
	"postwerk/internal/store"
	"postwerk/internal/tlsutil"
)

const ssoPublicURL = "https://mail.example.test"

func newSSOEnv(t *testing.T) (*testEnv, *ssotest.Provider) {
	t.Helper()
	e := newAuthEnv(t)
	p := ssotest.New("postwerk", "geheim")
	t.Cleanup(p.Close)
	p.Claims["email"] = e.acct.Email
	p.Claims["email_verified"] = true
	p.Claims["name"] = "Anna SSO"
	cur := e.s.Settings.Get()
	cur.Web.PublicURL = ssoPublicURL
	cur.OIDC.Enabled = true
	cur.OIDC.IssuerURL = p.URL
	cur.OIDC.ClientID = "postwerk"
	cur.OIDC.ClientSecret = "geheim"
	if err := e.s.Settings.Save(context.Background(), cur); err != nil {
		t.Fatal(err)
	}
	e.s.Auth.Settings = e.s.Settings
	e.s.TLS = &tlsutil.Dynamic{DataDir: e.dir, Settings: func() store.TLSSettings { return e.s.Settings.Get().TLS },
		Hostname: func() string { return "mail.example.test" }}
	return e, p
}

func (e *testEnv) updateOIDC(t *testing.T, f func(*store.OIDCSettings)) {
	t.Helper()
	cur := e.s.Settings.Get()
	f(&cur.OIDC)
	if err := e.s.Settings.Save(context.Background(), cur); err != nil {
		t.Fatal(err)
	}
}

// ssoStart begins a login and returns the callback request path (as the
// identity provider redirects back) and the browser binding cookie.
func (e *testEnv) ssoStart(t *testing.T) (callback string, binding *http.Cookie) {
	t.Helper()
	rec := e.do(t, "GET", "/api/auth/oidc/start", nil, nil)
	if rec.Code != http.StatusFound {
		t.Fatalf("start: %d %s", rec.Code, rec.Body.String())
	}
	for _, c := range rec.Result().Cookies() {
		if c.Name == oidcBindingCookie && c.Value != "" {
			binding = c
			if c.SameSite != http.SameSiteLaxMode || !c.HttpOnly {
				t.Fatalf("binding cookie flags: %+v", c)
			}
		}
	}
	if binding == nil {
		t.Fatal("no binding cookie")
	}
	authURL := rec.Header().Get("Location")
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := client.Get(authURL)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	loc, err := url.Parse(resp.Header.Get("Location"))
	if err != nil || !strings.HasPrefix(loc.String(), ssoPublicURL+oidcCallbackPath) {
		t.Fatalf("provider redirect: %d %q", resp.StatusCode, resp.Header.Get("Location"))
	}
	return loc.RequestURI(), binding
}

func ssoErrorOf(rec *httptest.ResponseRecorder) string {
	loc, _ := url.Parse(rec.Header().Get("Location"))
	if loc == nil || loc.Path != "/login" {
		return ""
	}
	return loc.Query().Get("sso_error")
}

func (e *testEnv) ssoLogin(t *testing.T) *http.Cookie {
	t.Helper()
	cb, binding := e.ssoStart(t)
	rec := e.do(t, "GET", cb, nil, binding)
	if rec.Code != http.StatusFound || rec.Header().Get("Location") != "/" {
		t.Fatalf("callback: %d %q", rec.Code, rec.Header().Get("Location"))
	}
	return sessionFrom(t, rec)
}

func TestSSOStatusPublic(t *testing.T) {
	e, _ := newSSOEnv(t)
	e.updateOIDC(t, func(o *store.OIDCSettings) { o.ButtonLabel = "Mit Authentik anmelden" })
	rec := e.do(t, "GET", "/api/auth/sso", nil, nil)
	var body map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &body)
	if rec.Code != 200 || body["enabled"] != true || body["label"] != "Mit Authentik anmelden" || len(body) != 3 {
		t.Fatalf("status: %d %v", rec.Code, body)
	}
}

func TestSSOLoginExistingAccount(t *testing.T) {
	e, _ := newSSOEnv(t)
	// SSO logins skip Postwerk's TOTP (the identity provider does MFA).
	tr := true
	secret := auth.NewTOTPSecret()
	_ = e.s.Store.UpdateAccount(context.Background(), e.acct.ID, store.AccountPatch{TOTPEnabled: &tr, TOTPSecret: &secret})

	c := e.ssoLogin(t)
	rec := e.do(t, "GET", "/api/auth/me", nil, c)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"source":"local"`) {
		t.Fatalf("me: %d %s", rec.Code, rec.Body.String())
	}
	method, _, err := e.s.Store.SessionMeta(context.Background(), auth.HashToken(c.Value))
	if err != nil || method != store.SessionOIDC {
		t.Fatalf("session method = %q, %v", method, err)
	}
}

func TestSSOStateReplayAndBinding(t *testing.T) {
	e, _ := newSSOEnv(t)
	cb, binding := e.ssoStart(t)
	// Callback without the binding cookie (another browser / login CSRF).
	if rec := e.do(t, "GET", cb, nil, nil); ssoErrorOf(rec) != "state" {
		t.Fatalf("without binding: %d %q", rec.Code, rec.Header().Get("Location"))
	}
	// The state was consumed by the failed attempt.
	if rec := e.do(t, "GET", cb, nil, binding); ssoErrorOf(rec) != "state" {
		t.Fatalf("replay: %q", rec.Header().Get("Location"))
	}

	cb, binding = e.ssoStart(t)
	if rec := e.do(t, "GET", cb, nil, binding); rec.Header().Get("Location") != "/" {
		t.Fatalf("login: %q", rec.Header().Get("Location"))
	}
	if rec := e.do(t, "GET", cb, nil, binding); ssoErrorOf(rec) != "state" {
		t.Fatalf("replayed callback: %q", rec.Header().Get("Location"))
	}
}

func TestSSOProviderError(t *testing.T) {
	e, _ := newSSOEnv(t)
	if rec := e.do(t, "GET", oidcCallbackPath+"?error=access_denied&state=x", nil, nil); ssoErrorOf(rec) != "denied" {
		t.Fatalf("denied: %q", rec.Header().Get("Location"))
	}
}

func TestSSOUnverifiedEmail(t *testing.T) {
	e, p := newSSOEnv(t)
	p.Claims["email_verified"] = false
	cb, binding := e.ssoStart(t)
	if rec := e.do(t, "GET", cb, nil, binding); ssoErrorOf(rec) != "email_unverified" {
		t.Fatalf("unverified: %q", rec.Header().Get("Location"))
	}
}

func TestSSOWrongAudience(t *testing.T) {
	e, p := newSSOEnv(t)
	p.Audience = "other-client"
	cb, binding := e.ssoStart(t)
	if rec := e.do(t, "GET", cb, nil, binding); ssoErrorOf(rec) != "failed" || sessionCookieSet(rec) {
		t.Fatalf("wrong audience: %q", rec.Header().Get("Location"))
	}
}

func sessionCookieSet(rec *httptest.ResponseRecorder) bool {
	for _, c := range rec.Result().Cookies() {
		if c.Name == sessionCookie && c.Value != "" {
			return true
		}
	}
	return false
}

func TestSSOAutoProvision(t *testing.T) {
	e, p := newSSOEnv(t)
	ctx := context.Background()
	p.Claims["email"] = "neu@sso.example"
	p.Claims["groups"] = []string{"mail-admins"}
	cb, binding := e.ssoStart(t)
	if rec := e.do(t, "GET", cb, nil, binding); ssoErrorOf(rec) != "no_account" {
		t.Fatalf("no auto-provisioning: %q", rec.Header().Get("Location"))
	}
	e.updateOIDC(t, func(o *store.OIDCSettings) {
		o.AutoProvision, o.AdminClaim, o.AdminValue = true, "groups", "mail-admins"
	})
	// The domain must exist in Postwerk.
	cb, binding = e.ssoStart(t)
	if rec := e.do(t, "GET", cb, nil, binding); ssoErrorOf(rec) != "no_account" {
		t.Fatalf("unknown domain: %q", rec.Header().Get("Location"))
	}
	if err := e.s.Store.CreateDomain(ctx, "sso.example", "pw1", "key"); err != nil {
		t.Fatal(err)
	}
	c := e.ssoLogin(t)
	acct, err := e.s.Store.GetAccountByEmail(ctx, "neu@sso.example")
	if err != nil || acct.Source != store.SourceOIDC || acct.Role != "admin" || acct.Name != "Anna SSO" || acct.PasswordHash != auth.NoPassword {
		t.Fatalf("provisioned = %+v, %v", acct, err)
	}
	// The admin claim is re-evaluated at every login of SSO accounts.
	p.Claims["groups"] = []string{"staff"}
	e.ssoLogin(t)
	acct, _ = e.s.Store.GetAccountByEmail(ctx, "neu@sso.example")
	if acct.Role != "user" {
		t.Fatalf("role after group removal = %s", acct.Role)
	}

	// SSO accounts: no password change, no TOTP, app passwords after a fresh SSO login.
	if rec := e.do(t, "POST", "/api/account/password", map[string]any{"current": "", "new": "irgendein-passwort"}, c); errCode(rec) != "external_password" {
		t.Fatalf("password change: %d %s", rec.Code, rec.Body.String())
	}
	if rec := e.do(t, "POST", "/api/account/totp/setup", nil, c); rec.Code != 400 {
		t.Fatalf("totp setup: %d", rec.Code)
	}
	if rec := e.do(t, "POST", "/api/account/app-passwords", map[string]string{"name": "Handy"}, c); rec.Code != 200 {
		t.Fatalf("app password: %d %s", rec.Code, rec.Body.String())
	}
	// An old session is not enough.
	_, _ = e.s.Store.DB.Exec(`UPDATE sessions SET created_at = created_at - 3600 WHERE token_hash = ?`, auth.HashToken(c.Value))
	if rec := e.do(t, "POST", "/api/account/app-passwords", map[string]string{"name": "Laptop"}, c); errCode(rec) != "sso_reauth_required" {
		t.Fatalf("stale session: %d %s", rec.Code, rec.Body.String())
	}
	// No password login for SSO accounts.
	if rec := e.do(t, "POST", "/api/auth/login", map[string]string{"email": "neu@sso.example", "password": auth.NoPassword}, nil); rec.Code != 401 {
		t.Fatalf("password login: %d", rec.Code)
	}
}

func TestSSODisabledAccount(t *testing.T) {
	e, _ := newSSOEnv(t)
	tr := true
	_ = e.s.Store.UpdateAccount(context.Background(), e.acct.ID, store.AccountPatch{Disabled: &tr})
	cb, binding := e.ssoStart(t)
	if rec := e.do(t, "GET", cb, nil, binding); ssoErrorOf(rec) != "disabled" {
		t.Fatalf("disabled: %q", rec.Header().Get("Location"))
	}
}

func TestSSOLogoutEndsProviderSession(t *testing.T) {
	e, p := newSSOEnv(t)
	c := e.ssoLogin(t)
	rec := e.do(t, "POST", "/api/auth/logout", nil, c)
	if strings.Contains(rec.Body.String(), "endSessionUrl") {
		t.Fatalf("rp logout without setting: %s", rec.Body.String())
	}
	e.updateOIDC(t, func(o *store.OIDCSettings) { o.RPLogout = true })
	c = e.ssoLogin(t)
	rec = e.do(t, "POST", "/api/auth/logout", nil, c)
	var body struct{ EndSessionURL string }
	_ = json.Unmarshal(rec.Body.Bytes(), &body)
	if !strings.HasPrefix(body.EndSessionURL, p.URL+"/logout?") || !strings.Contains(body.EndSessionURL, url.QueryEscape(ssoPublicURL+"/login")) {
		t.Fatalf("logout: %s", rec.Body.String())
	}
	// Password sessions never get the provider logout.
	pc := e.login(t, "")
	if rec := e.do(t, "POST", "/api/auth/logout", nil, pc); strings.Contains(rec.Body.String(), "endSessionUrl") {
		t.Fatalf("password session: %s", rec.Body.String())
	}
}

func makeAdmin(t *testing.T, e *testEnv) {
	t.Helper()
	role := "admin"
	if err := e.s.Store.UpdateAccount(context.Background(), e.acct.ID, store.AccountPatch{Role: &role}); err != nil {
		t.Fatal(err)
	}
}

func TestOnlySSONeedsLocalAdmin(t *testing.T) {
	e, _ := newSSOEnv(t)
	makeAdmin(t, e)
	src := store.SourceOIDC
	_ = e.s.Store.UpdateAccount(context.Background(), e.acct.ID, store.AccountPatch{Source: &src})
	c := e.ssoLogin(t)
	rec := e.do(t, "PUT", "/api/admin/settings", map[string]any{"oidc": map[string]any{"onlySso": true}}, c)
	if rec.Code != 400 || !strings.Contains(rec.Body.String(), "lokales Administratorkonto") {
		t.Fatalf("only sso without local admin: %d %s", rec.Code, rec.Body.String())
	}
	h, _ := auth.HashPassword(testPassword)
	_ = e.s.Store.CreateAccount(context.Background(), &store.Account{Email: "root@example.test", PasswordHash: h, Role: "admin"})
	rec = e.do(t, "PUT", "/api/admin/settings", map[string]any{"oidc": map[string]any{"onlySso": true}}, c)
	if rec.Code != 200 {
		t.Fatalf("only sso: %d %s", rec.Code, rec.Body.String())
	}
	// Secrets are masked and tied to the issuer.
	if strings.Contains(rec.Body.String(), "geheim") {
		t.Fatal("client secret not masked")
	}
	rec = e.do(t, "PUT", "/api/admin/settings", map[string]any{"oidc": map[string]any{"issuerUrl": "https://evil.example", "clientSecret": mask}}, c)
	if rec.Code != 400 {
		t.Fatalf("secret kept for new issuer: %d %s", rec.Code, rec.Body.String())
	}
	// Password login is now limited to local admins.
	if rec := e.do(t, "POST", "/api/auth/login", map[string]string{"email": "root@example.test", "password": testPassword}, nil); rec.Code != 200 {
		t.Fatalf("break-glass login: %d %s", rec.Code, rec.Body.String())
	}
}

func TestOnlySSOBlocksUserPasswordLogin(t *testing.T) {
	e, _ := newSSOEnv(t)
	e.updateOIDC(t, func(o *store.OIDCSettings) { o.OnlySSO = true })
	rec := e.do(t, "POST", "/api/auth/login", map[string]string{"email": e.acct.Email, "password": testPassword}, nil)
	if rec.Code != 403 || errCode(rec) != "sso_only" {
		t.Fatalf("password login: %d %s", rec.Code, rec.Body.String())
	}
	e.ssoLogin(t)
}

func TestAdminLDAPEndpoints(t *testing.T) {
	e, _ := newSSOEnv(t)
	ctx := context.Background()
	makeAdmin(t, e)
	srv, err := ldaptest.Start(
		ldaptest.Entry{DN: "cn=svc,dc=example,dc=test", Password: "svc"},
		ldaptest.Entry{DN: "uid=max,dc=example,dc=test", Password: "max-pw", Attrs: map[string][]string{
			"objectClass": {"person"}, "cn": {"Max"}, "mail": {"max@example.test"}, "userPassword": {"x"}}},
		ldaptest.Entry{DN: "uid=fremd,dc=example,dc=test", Password: "f", Attrs: map[string][]string{
			"objectClass": {"person"}, "cn": {"Fremd"}, "mail": {"fremd@unbekannt.test"}}},
	)
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Close()
	if err := e.s.Store.CreateDomain(ctx, "example.test", "pw1", "key"); err != nil {
		t.Fatal(err)
	}
	src := store.SourceLDAP
	ghost := &store.Account{Email: "weg@example.test", PasswordHash: auth.NoPassword, Role: "user", Source: src}
	_ = e.s.Store.CreateAccount(ctx, ghost)
	c := e.login(t, "")

	ldap := map[string]any{"enabled": true, "url": srv.URL(), "bindDn": "cn=svc,dc=example,dc=test", "bindPassword": "svc", "baseDn": "dc=example,dc=test"}
	rec := e.do(t, "PUT", "/api/admin/settings", map[string]any{"ldap": ldap}, c)
	if rec.Code != 200 || strings.Contains(rec.Body.String(), `"bindPassword":"svc"`) {
		t.Fatalf("save ldap: %d %s", rec.Code, rec.Body.String())
	}

	// Test with an unsaved draft: the masked password is reused for the same URL.
	rec = e.do(t, "POST", "/api/admin/ldap/test", map[string]any{"email": "max@example.test",
		"config": map[string]any{"url": srv.URL(), "bindPassword": mask, "nameAttribute": "cn"}}, c)
	var test struct {
		OK    bool
		Error string
		Entry struct {
			DN         string
			Name       string
			Attributes map[string][]string
		}
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &test)
	if !test.OK || test.Entry.Name != "Max" || test.Entry.Attributes["userPassword"] != nil {
		t.Fatalf("ldap test: %s", rec.Body.String())
	}
	rec = e.do(t, "POST", "/api/admin/ldap/test", map[string]any{"config": map[string]any{"url": "ldap://127.0.0.1:1", "bindPassword": mask}}, c)
	if rec.Code != 400 {
		t.Fatalf("masked password sent to another server: %d %s", rec.Code, rec.Body.String())
	}

	rec = e.do(t, "POST", "/api/admin/ldap/sync", map[string]any{"apply": false}, c)
	var sync struct {
		Users   []ldapSyncUser
		Orphans []store.AccountRef
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &sync)
	if len(sync.Users) != 2 || sync.Users[0].Status != "no_domain" || sync.Users[1].Status != "missing" || len(sync.Orphans) != 1 {
		t.Fatalf("sync preview: %s", rec.Body.String())
	}
	rec = e.do(t, "POST", "/api/admin/ldap/sync", map[string]any{"apply": true}, c)
	if !strings.Contains(rec.Body.String(), `"created":1`) || !strings.Contains(rec.Body.String(), `"disabled":1`) {
		t.Fatalf("sync apply: %s", rec.Body.String())
	}
	max, err := e.s.Store.GetAccountByEmail(ctx, "max@example.test")
	if err != nil || max.Source != store.SourceLDAP {
		t.Fatalf("created = %+v, %v", max, err)
	}
	if g, _ := e.s.Store.GetAccount(ctx, ghost.ID); !g.Disabled {
		t.Fatal("orphan not disabled")
	}

	// LDAP accounts cannot get a local password from the admin either.
	rec = e.do(t, "PATCH", "/api/admin/accounts/"+itoa(int(max.ID)), map[string]any{"password": "neues-passwort-123"}, c)
	if rec.Code != 400 {
		t.Fatalf("admin password for ldap account: %d %s", rec.Code, rec.Body.String())
	}
	// The directory password works for web login.
	if rec := e.do(t, "POST", "/api/auth/login", map[string]string{"email": "max@example.test", "password": "max-pw"}, nil); rec.Code != 200 {
		t.Fatalf("ldap login: %d %s", rec.Code, rec.Body.String())
	}
}
