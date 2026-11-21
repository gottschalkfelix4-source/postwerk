// Package ssotest provides a fake OpenID Connect provider for tests: it
// serves discovery, JWKS, an authorization endpoint that redirects back with
// a code, and a token endpoint that checks the PKCE verifier and returns
// signed ID tokens.
package ssotest

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync"
	"time"

	"github.com/coreos/go-oidc/v3/oidc/oidctest"
)

const keyID = "test-key"

// Provider is a fake identity provider.
type Provider struct {
	*httptest.Server
	ClientID     string
	ClientSecret string
	// Claims are added to every ID token (email, email_verified, groups, ...).
	Claims map[string]any
	// Audience overrides the aud claim (default: ClientID).
	Audience string
	// Nonce overrides the nonce claim (default: the one from the request).
	Nonce *string

	key   *rsa.PrivateKey
	keys  *oidctest.Server
	mu    sync.Mutex
	codes map[string]authRequest
	// TokenRequests counts calls of the token endpoint.
	TokenRequests int
}

type authRequest struct {
	challenge, nonce, redirect string
}

// New starts a provider.
func New(clientID, clientSecret string) *Provider {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		panic(err)
	}
	p := &Provider{ClientID: clientID, ClientSecret: clientSecret, key: key, codes: map[string]authRequest{},
		Claims: map[string]any{}}
	p.keys = &oidctest.Server{PublicKeys: []oidctest.PublicKey{{PublicKey: key.Public(), KeyID: keyID, Algorithm: "RS256"}}}
	mux := http.NewServeMux()
	mux.HandleFunc("/.well-known/openid-configuration", p.discovery)
	mux.Handle("/keys", p.keys)
	mux.HandleFunc("/auth", p.authorize)
	mux.HandleFunc("/token", p.token)
	p.Server = httptest.NewServer(mux)
	p.keys.SetIssuer(p.URL)
	return p
}

func (p *Provider) discovery(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"issuer":                                p.URL,
		"authorization_endpoint":                p.URL + "/auth",
		"token_endpoint":                        p.URL + "/token",
		"jwks_uri":                              p.URL + "/keys",
		"end_session_endpoint":                  p.URL + "/logout",
		"response_types_supported":              []string{"code"},
		"subject_types_supported":               []string{"public"},
		"id_token_signing_alg_values_supported": []string{"RS256"},
		"code_challenge_methods_supported":      []string{"S256"},
	})
}

// authorize behaves like a user who is already signed in: it redirects back
// to redirect_uri with a code (or validates and fails with an error).
func (p *Provider) authorize(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	redirect := q.Get("redirect_uri")
	if q.Get("client_id") != p.ClientID || q.Get("response_type") != "code" || q.Get("code_challenge_method") != "S256" ||
		q.Get("code_challenge") == "" || q.Get("state") == "" || q.Get("nonce") == "" {
		http.Error(w, "bad authorization request", http.StatusBadRequest)
		return
	}
	code := rand.Text()
	p.mu.Lock()
	p.codes[code] = authRequest{challenge: q.Get("code_challenge"), nonce: q.Get("nonce"), redirect: redirect}
	p.mu.Unlock()
	u, _ := url.Parse(redirect)
	rq := u.Query()
	rq.Set("code", code)
	rq.Set("state", q.Get("state"))
	u.RawQuery = rq.Encode()
	http.Redirect(w, r, u.String(), http.StatusFound)
}

func (p *Provider) token(w http.ResponseWriter, r *http.Request) {
	p.mu.Lock()
	p.TokenRequests++
	p.mu.Unlock()
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad form", http.StatusBadRequest)
		return
	}
	id, secret, ok := r.BasicAuth()
	if !ok {
		id, secret = r.PostForm.Get("client_id"), r.PostForm.Get("client_secret")
	}
	if id != p.ClientID || secret != p.ClientSecret {
		tokenError(w, http.StatusUnauthorized, "invalid_client")
		return
	}
	code := r.PostForm.Get("code")
	p.mu.Lock()
	req, found := p.codes[code]
	delete(p.codes, code)
	p.mu.Unlock()
	if !found || r.PostForm.Get("redirect_uri") != req.redirect {
		tokenError(w, http.StatusBadRequest, "invalid_grant")
		return
	}
	sum := sha256.Sum256([]byte(r.PostForm.Get("code_verifier")))
	if base64.RawURLEncoding.EncodeToString(sum[:]) != req.challenge {
		tokenError(w, http.StatusBadRequest, "invalid_grant")
		return
	}
	claims := map[string]any{
		"iss": p.URL, "aud": p.ClientID, "sub": "user-1",
		"iat": time.Now().Unix(), "exp": time.Now().Add(5 * time.Minute).Unix(), "nonce": req.nonce,
	}
	if p.Audience != "" {
		claims["aud"] = p.Audience
	}
	if p.Nonce != nil {
		claims["nonce"] = *p.Nonce
	}
	for k, v := range p.Claims {
		claims[k] = v
	}
	raw, _ := json.Marshal(claims)
	idToken := oidctest.SignIDToken(p.key, keyID, "RS256", string(raw))
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"access_token": hex.EncodeToString(sum[:8]), "token_type": "Bearer", "expires_in": 300, "id_token": idToken,
	})
}

func tokenError(w http.ResponseWriter, status int, code string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": code})
}
