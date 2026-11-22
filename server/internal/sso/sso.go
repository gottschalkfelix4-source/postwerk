// Package sso implements OpenID Connect single sign-on for the web interface:
// authorization code flow with PKCE (S256), state and nonce kept server-side
// (short-lived, single use) and bound to the browser that started the login.
package sso

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
	"golang.org/x/oauth2"

	"postwerk/internal/store"
)

var (
	ErrNotConfigured   = errors.New("sso: not configured")
	ErrUnknownState    = errors.New("sso: unknown, used or expired state")
	ErrBrowserMismatch = errors.New("sso: login was started in another browser")
	ErrConfigChanged   = errors.New("sso: settings changed during login")
	ErrNonce           = errors.New("sso: nonce mismatch")
	ErrNoIDToken       = errors.New("sso: token response without id_token")
	ErrEmailMissing    = errors.New("sso: email claim missing")
	ErrEmailUnverified = errors.New("sso: email address not verified")
)

const (
	// StateTTL is how long a started login may take.
	StateTTL = 10 * time.Minute
	// maxPending bounds the number of open logins (memory protection).
	maxPending = 10000
	// providerTTL is how long discovery results are reused.
	providerTTL = time.Hour
	httpTimeout = 10 * time.Second
)

// Identity is the verified user returned by Finish.
type Identity struct {
	Subject string
	Email   string
	Name    string
	// Admin is the result of the admin claim check; AdminChecked tells
	// whether an admin claim is configured at all.
	Admin        bool
	AdminChecked bool
}

type pending struct {
	nonce, verifier, binding string
	redirectURI              string
	issuer, clientID         string
	expires                  time.Time
}

type cachedProvider struct {
	p       *oidc.Provider
	fetched time.Time
}

// Manager keeps open logins and discovered providers.
type Manager struct {
	// Client is used for discovery, JWKS and token requests.
	Client *http.Client
	// Now is replaceable in tests.
	Now func() time.Time

	mu        sync.Mutex
	pending   map[string]*pending
	providers map[string]cachedProvider
}

func New() *Manager {
	return &Manager{Client: &http.Client{Timeout: httpTimeout}, Now: time.Now,
		pending: map[string]*pending{}, providers: map[string]cachedProvider{}}
}

// Configured reports whether cfg is complete enough to start a login.
func Configured(cfg store.OIDCSettings) bool {
	return cfg.Enabled && strings.TrimSpace(cfg.IssuerURL) != "" && strings.TrimSpace(cfg.ClientID) != ""
}

func (m *Manager) ctx(ctx context.Context) context.Context { return oidc.ClientContext(ctx, m.Client) }

func (m *Manager) provider(ctx context.Context, issuer string) (*oidc.Provider, error) {
	m.mu.Lock()
	c, ok := m.providers[issuer]
	m.mu.Unlock()
	if ok && m.Now().Sub(c.fetched) < providerTTL {
		return c.p, nil
	}
	p, err := oidc.NewProvider(m.ctx(ctx), issuer)
	if err != nil {
		return nil, fmt.Errorf("discovery: %w", err)
	}
	m.mu.Lock()
	m.providers[issuer] = cachedProvider{p, m.Now()}
	m.mu.Unlock()
	return p, nil
}

func scopes(cfg store.OIDCSettings) []string {
	out := []string{oidc.ScopeOpenID}
	for _, s := range cfg.Scopes {
		if s = strings.TrimSpace(s); s != "" && s != oidc.ScopeOpenID {
			out = append(out, s)
		}
	}
	return out
}

func oauthConfig(p *oidc.Provider, cfg store.OIDCSettings, redirectURI string) *oauth2.Config {
	return &oauth2.Config{ClientID: strings.TrimSpace(cfg.ClientID), ClientSecret: cfg.ClientSecret, Endpoint: p.Endpoint(),
		RedirectURL: redirectURI, Scopes: scopes(cfg)}
}

func randomToken() string {
	return oauth2.GenerateVerifier() // 32 random bytes, base64url
}

func hash(s string) string {
	h := sha256.Sum256([]byte(s))
	return hex.EncodeToString(h[:])
}

// Begin starts a login. It returns the URL of the identity provider and a
// browser binding that the caller stores in a cookie and passes to Finish.
func (m *Manager) Begin(ctx context.Context, cfg store.OIDCSettings, redirectURI string) (authURL, binding string, err error) {
	if !Configured(cfg) {
		return "", "", ErrNotConfigured
	}
	issuer := strings.TrimSpace(cfg.IssuerURL)
	p, err := m.provider(ctx, issuer)
	if err != nil {
		return "", "", err
	}
	state, nonce, verifier, binding := randomToken(), randomToken(), oauth2.GenerateVerifier(), randomToken()
	now := m.Now()
	m.mu.Lock()
	for k, v := range m.pending {
		if now.After(v.expires) {
			delete(m.pending, k)
		}
	}
	if len(m.pending) >= maxPending {
		// Drop the oldest open login instead of refusing new ones.
		var oldest string
		for k, v := range m.pending {
			if oldest == "" || v.expires.Before(m.pending[oldest].expires) {
				oldest = k
			}
		}
		delete(m.pending, oldest)
	}
	m.pending[hash(state)] = &pending{nonce: nonce, verifier: verifier, binding: hash(binding), redirectURI: redirectURI,
		issuer: issuer, clientID: strings.TrimSpace(cfg.ClientID), expires: now.Add(StateTTL)}
	m.mu.Unlock()
	u := oauthConfig(p, cfg, redirectURI).AuthCodeURL(state, oidc.Nonce(nonce), oauth2.S256ChallengeOption(verifier))
	return u, binding, nil
}

// take removes and returns the login of state (single use).
func (m *Manager) take(state string) *pending {
	m.mu.Lock()
	defer m.mu.Unlock()
	k := hash(state)
	p := m.pending[k]
	delete(m.pending, k)
	if p == nil || m.Now().After(p.expires) {
		return nil
	}
	return p
}

// Finish completes a login: it checks state and browser binding, redeems the
// code (with the PKCE verifier) and verifies the ID token (signature, issuer,
// audience, expiry, nonce) and the email claim.
func (m *Manager) Finish(ctx context.Context, cfg store.OIDCSettings, state, code, binding string) (*Identity, error) {
	if !Configured(cfg) {
		return nil, ErrNotConfigured
	}
	if state == "" {
		return nil, ErrUnknownState
	}
	pd := m.take(state)
	if pd == nil {
		return nil, ErrUnknownState
	}
	if binding == "" || subtle.ConstantTimeCompare([]byte(hash(binding)), []byte(pd.binding)) != 1 {
		return nil, ErrBrowserMismatch
	}
	if pd.issuer != strings.TrimSpace(cfg.IssuerURL) || pd.clientID != strings.TrimSpace(cfg.ClientID) {
		return nil, ErrConfigChanged
	}
	p, err := m.provider(ctx, pd.issuer)
	if err != nil {
		return nil, err
	}
	tok, err := oauthConfig(p, cfg, pd.redirectURI).Exchange(m.ctx(ctx), code, oauth2.VerifierOption(pd.verifier))
	if err != nil {
		return nil, fmt.Errorf("token exchange: %w", err)
	}
	raw, _ := tok.Extra("id_token").(string)
	if raw == "" {
		return nil, ErrNoIDToken
	}
	idt, err := p.Verifier(&oidc.Config{ClientID: pd.clientID, Now: m.Now}).Verify(m.ctx(ctx), raw)
	if err != nil {
		return nil, fmt.Errorf("id token: %w", err)
	}
	if subtle.ConstantTimeCompare([]byte(idt.Nonce), []byte(pd.nonce)) != 1 {
		return nil, ErrNonce
	}
	claims := map[string]any{}
	if err := idt.Claims(&claims); err != nil {
		return nil, fmt.Errorf("id token claims: %w", err)
	}
	emailClaim := strings.TrimSpace(cfg.EmailClaim)
	if emailClaim == "" {
		emailClaim = "email"
	}
	if _, ok := claims[emailClaim].(string); !ok && p.UserInfoEndpoint() != "" {
		// Some providers only put the address into the userinfo response.
		if ui, err := p.UserInfo(m.ctx(ctx), oauth2.StaticTokenSource(tok)); err == nil && ui.Subject == idt.Subject {
			extra := map[string]any{}
			if ui.Claims(&extra) == nil {
				for k, v := range extra {
					if _, exists := claims[k]; !exists {
						claims[k] = v
					}
				}
			}
		}
	}
	email, _ := claims[emailClaim].(string)
	email = store.Normalize(email)
	if email == "" || !strings.Contains(email, "@") {
		return nil, ErrEmailMissing
	}
	// email_verified only vouches for the "email" claim. With another
	// claim (e.g. preferred_username) it counts only if both carry the
	// same address.
	verified := truthy(claims["email_verified"])
	if emailClaim != "email" {
		other, _ := claims["email"].(string)
		verified = verified && store.Normalize(other) == email
	}
	if !cfg.AllowUnverifiedEmail && !verified {
		return nil, ErrEmailUnverified
	}
	id := &Identity{Subject: idt.Subject, Email: email}
	for _, k := range []string{"name", "preferred_username"} {
		if v, ok := claims[k].(string); ok && strings.TrimSpace(v) != "" {
			id.Name = strings.TrimSpace(v)
			break
		}
	}
	if c := strings.TrimSpace(cfg.AdminClaim); c != "" {
		id.AdminChecked = true
		id.Admin = claimHas(claims[c], strings.TrimSpace(cfg.AdminValue))
	}
	return id, nil
}

// truthy accepts true and "true" (some providers send strings).
func truthy(v any) bool {
	switch t := v.(type) {
	case bool:
		return t
	case string:
		return strings.EqualFold(t, "true")
	}
	return false
}

// claimHas reports whether a claim (string, list or boolean) contains want.
// With an empty want, a boolean true claim matches.
func claimHas(v any, want string) bool {
	switch t := v.(type) {
	case string:
		if want == "" {
			return strings.EqualFold(t, "true")
		}
		for _, part := range strings.Fields(strings.ReplaceAll(t, ",", " ")) {
			if part == want {
				return true
			}
		}
		return t == want
	case []any:
		for _, e := range t {
			if s, ok := e.(string); ok && s == want {
				return true
			}
		}
	case bool:
		return want == "" && t
	}
	return false
}

// Discovery describes a provider for the admin test.
type Discovery struct {
	Issuer                string   `json:"issuer"`
	AuthorizationEndpoint string   `json:"authorizationEndpoint"`
	TokenEndpoint         string   `json:"tokenEndpoint"`
	UserinfoEndpoint      string   `json:"userinfoEndpoint"`
	EndSessionEndpoint    string   `json:"endSessionEndpoint"`
	ScopesSupported       []string `json:"scopesSupported"`
	PKCEMethods           []string `json:"pkceMethods"`
}

// Discover fetches the discovery document of issuer (bypassing the cache).
func (m *Manager) Discover(ctx context.Context, issuer string) (*Discovery, error) {
	issuer = strings.TrimSpace(issuer)
	p, err := oidc.NewProvider(m.ctx(ctx), issuer)
	if err != nil {
		return nil, err
	}
	m.mu.Lock()
	m.providers[issuer] = cachedProvider{p, m.Now()}
	m.mu.Unlock()
	var meta struct {
		Issuer      string   `json:"issuer"`
		Auth        string   `json:"authorization_endpoint"`
		Token       string   `json:"token_endpoint"`
		UserInfo    string   `json:"userinfo_endpoint"`
		EndSession  string   `json:"end_session_endpoint"`
		Scopes      []string `json:"scopes_supported"`
		PKCEMethods []string `json:"code_challenge_methods_supported"`
	}
	if err := p.Claims(&meta); err != nil {
		return nil, err
	}
	return &Discovery{meta.Issuer, meta.Auth, meta.Token, meta.UserInfo, meta.EndSession, meta.Scopes, meta.PKCEMethods}, nil
}

// EndSessionURL returns the RP-initiated logout URL of the provider, or ""
// if it has none.
func (m *Manager) EndSessionURL(ctx context.Context, cfg store.OIDCSettings, postLogoutRedirect string) (string, error) {
	if !Configured(cfg) {
		return "", ErrNotConfigured
	}
	p, err := m.provider(ctx, strings.TrimSpace(cfg.IssuerURL))
	if err != nil {
		return "", err
	}
	var meta struct {
		EndSession string `json:"end_session_endpoint"`
	}
	if err := p.Claims(&meta); err != nil || meta.EndSession == "" {
		return "", err
	}
	u, err := url.Parse(meta.EndSession)
	if err != nil {
		return "", err
	}
	q := u.Query()
	q.Set("client_id", strings.TrimSpace(cfg.ClientID))
	if postLogoutRedirect != "" {
		q.Set("post_logout_redirect_uri", postLogoutRedirect)
	}
	u.RawQuery = q.Encode()
	return u.String(), nil
}
