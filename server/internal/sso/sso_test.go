package sso

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"postwerk/internal/sso/ssotest"
	"postwerk/internal/store"
)

const redirectURI = "https://mail.example.org/api/auth/oidc/callback"

func setup(t *testing.T) (*Manager, *ssotest.Provider, store.OIDCSettings) {
	t.Helper()
	p := ssotest.New("postwerk", "geheim")
	t.Cleanup(p.Close)
	p.Claims["email"] = "Anna@Example.org"
	p.Claims["email_verified"] = true
	p.Claims["name"] = "Anna Beispiel"
	p.Claims["groups"] = []string{"staff", "mail-admins"}
	cfg := store.DefaultSettings("mail.example.org").OIDC
	cfg.Enabled = true
	cfg.IssuerURL = p.URL
	cfg.ClientID = "postwerk"
	cfg.ClientSecret = "geheim"
	return New(), p, cfg
}

// authorize follows the authorization URL like a browser and returns the
// state and code from the redirect back to Postwerk.
func authorize(t *testing.T, authURL string) (state, code string) {
	t.Helper()
	u, _ := url.Parse(authURL)
	q := u.Query()
	if q.Get("code_challenge_method") != "S256" || q.Get("code_challenge") == "" || q.Get("nonce") == "" || q.Get("redirect_uri") != redirectURI {
		t.Fatalf("authorization request incomplete: %s", authURL)
	}
	if !strings.Contains(q.Get("scope"), "openid") || !strings.Contains(q.Get("scope"), "email") {
		t.Fatalf("scopes = %q", q.Get("scope"))
	}
	c := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := c.Get(authURL)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	loc, err := url.Parse(resp.Header.Get("Location"))
	if err != nil || resp.StatusCode != http.StatusFound {
		t.Fatalf("authorize: %d %v", resp.StatusCode, err)
	}
	return loc.Query().Get("state"), loc.Query().Get("code")
}

func TestFlow(t *testing.T) {
	m, _, cfg := setup(t)
	cfg.AdminClaim, cfg.AdminValue = "groups", "mail-admins"
	ctx := context.Background()
	authURL, binding, err := m.Begin(ctx, cfg, redirectURI)
	if err != nil {
		t.Fatal(err)
	}
	state, code := authorize(t, authURL)
	id, err := m.Finish(ctx, cfg, state, code, binding)
	if err != nil {
		t.Fatal(err)
	}
	if id.Email != "anna@example.org" || id.Name != "Anna Beispiel" || !id.Admin || !id.AdminChecked || id.Subject != "user-1" {
		t.Fatalf("identity = %+v", id)
	}
	// The state is single use.
	if _, err := m.Finish(ctx, cfg, state, code, binding); !errors.Is(err, ErrUnknownState) {
		t.Fatalf("replay: %v", err)
	}
}

func TestStateAndBinding(t *testing.T) {
	m, _, cfg := setup(t)
	ctx := context.Background()
	authURL, binding, err := m.Begin(ctx, cfg, redirectURI)
	if err != nil {
		t.Fatal(err)
	}
	state, code := authorize(t, authURL)
	if _, err := m.Finish(ctx, cfg, "forged-state", code, binding); !errors.Is(err, ErrUnknownState) {
		t.Fatalf("unknown state: %v", err)
	}
	// Another browser (no or a different binding cookie) cannot use the state,
	// and the attempt consumes it.
	if _, err := m.Finish(ctx, cfg, state, code, "other"); !errors.Is(err, ErrBrowserMismatch) {
		t.Fatalf("binding: %v", err)
	}
	if _, err := m.Finish(ctx, cfg, state, code, binding); !errors.Is(err, ErrUnknownState) {
		t.Fatalf("state reused after failure: %v", err)
	}
}

func TestStateExpires(t *testing.T) {
	m, _, cfg := setup(t)
	ctx := context.Background()
	authURL, binding, err := m.Begin(ctx, cfg, redirectURI)
	if err != nil {
		t.Fatal(err)
	}
	state, code := authorize(t, authURL)
	m.Now = func() time.Time { return time.Now().Add(StateTTL + time.Minute) }
	if _, err := m.Finish(ctx, cfg, state, code, binding); !errors.Is(err, ErrUnknownState) {
		t.Fatalf("expired: %v", err)
	}
}

func TestPKCEVerifierRequired(t *testing.T) {
	m, p, cfg := setup(t)
	ctx := context.Background()
	authURL, binding, err := m.Begin(ctx, cfg, redirectURI)
	if err != nil {
		t.Fatal(err)
	}
	state, code := authorize(t, authURL)
	// Swap the stored verifier: the provider must refuse the code.
	m.mu.Lock()
	for _, pd := range m.pending {
		pd.verifier = "wrong-verifier-wrong-verifier-wrong-verifier"
	}
	m.mu.Unlock()
	if _, err := m.Finish(ctx, cfg, state, code, binding); err == nil || !strings.Contains(err.Error(), "token exchange") {
		t.Fatalf("pkce: %v", err)
	}
	if p.TokenRequests == 0 {
		t.Fatal("token endpoint not called")
	}
}

func TestWrongAudience(t *testing.T) {
	m, p, cfg := setup(t)
	p.Audience = "some-other-client"
	ctx := context.Background()
	authURL, binding, _ := m.Begin(ctx, cfg, redirectURI)
	state, code := authorize(t, authURL)
	if _, err := m.Finish(ctx, cfg, state, code, binding); err == nil || !strings.Contains(err.Error(), "id token") {
		t.Fatalf("audience: %v", err)
	}
}

func TestWrongNonce(t *testing.T) {
	m, p, cfg := setup(t)
	n := "attacker-nonce"
	p.Nonce = &n
	ctx := context.Background()
	authURL, binding, _ := m.Begin(ctx, cfg, redirectURI)
	state, code := authorize(t, authURL)
	if _, err := m.Finish(ctx, cfg, state, code, binding); !errors.Is(err, ErrNonce) {
		t.Fatalf("nonce: %v", err)
	}
}

func TestEmailChecks(t *testing.T) {
	m, p, cfg := setup(t)
	ctx := context.Background()
	run := func() error {
		authURL, binding, err := m.Begin(ctx, cfg, redirectURI)
		if err != nil {
			t.Fatal(err)
		}
		state, code := authorize(t, authURL)
		_, err = m.Finish(ctx, cfg, state, code, binding)
		return err
	}
	p.Claims["email_verified"] = false
	if err := run(); !errors.Is(err, ErrEmailUnverified) {
		t.Fatalf("unverified: %v", err)
	}
	p.Claims["email_verified"] = "true"
	if err := run(); err != nil {
		t.Fatalf("string true: %v", err)
	}
	delete(p.Claims, "email_verified")
	cfg.AllowUnverifiedEmail = true
	if err := run(); err != nil {
		t.Fatalf("allowed unverified: %v", err)
	}
	delete(p.Claims, "email")
	if err := run(); !errors.Is(err, ErrEmailMissing) {
		t.Fatalf("missing email: %v", err)
	}
}

func TestConfigChangedDuringLogin(t *testing.T) {
	m, _, cfg := setup(t)
	ctx := context.Background()
	authURL, binding, _ := m.Begin(ctx, cfg, redirectURI)
	state, code := authorize(t, authURL)
	cfg.ClientID = "another"
	if _, err := m.Finish(ctx, cfg, state, code, binding); !errors.Is(err, ErrConfigChanged) {
		t.Fatalf("err = %v", err)
	}
}

func TestEndSessionAndDiscover(t *testing.T) {
	m, p, cfg := setup(t)
	u, err := m.EndSessionURL(context.Background(), cfg, "https://mail.example.org/login")
	if err != nil || !strings.HasPrefix(u, p.URL+"/logout?") || !strings.Contains(u, "client_id=postwerk") ||
		!strings.Contains(u, "post_logout_redirect_uri=https%3A%2F%2Fmail.example.org%2Flogin") {
		t.Fatalf("end session url = %q, %v", u, err)
	}
	d, err := m.Discover(context.Background(), p.URL)
	if err != nil || d.TokenEndpoint != p.URL+"/token" || len(d.PKCEMethods) != 1 {
		t.Fatalf("discovery = %+v, %v", d, err)
	}
}

func TestClaimHas(t *testing.T) {
	cases := []struct {
		v    any
		want string
		ok   bool
	}{
		{[]any{"a", "mail-admins"}, "mail-admins", true},
		{[]any{"a"}, "mail-admins", false},
		{"mail-admins", "mail-admins", true},
		{"staff mail-admins", "mail-admins", true},
		{true, "", true},
		{false, "", false},
		{nil, "x", false},
	}
	for i, c := range cases {
		if got := claimHas(c.v, c.want); got != c.ok {
			t.Errorf("case %d: %v", i, got)
		}
	}
}
