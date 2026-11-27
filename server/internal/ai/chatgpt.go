package ai

// Provider "chatgpt": the admin signs in with a ChatGPT subscription (Plus,
// Pro, ...) through the OAuth device-code flow of the Codex CLI ("Sign in
// with ChatGPT"); requests then go to the Responses endpoint of the ChatGPT
// backend and count against the plan's usage limits. Embeddings are not
// offered there, so semantic search needs the separate embedding endpoint.

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"postwerk/internal/store"
)

const (
	ProviderChatGPT = "chatgpt"

	// ChatGPTClientID is the public OAuth client of the Codex CLI.
	ChatGPTClientID = "app_EMoamEEZ73f0CkXaXp7hrann"
	// ChatGPTDefaultModel is the first model of the Codex model catalog.
	ChatGPTDefaultModel = "gpt-6.2-sol"

	chatGPTIssuer     = "https://auth.openai.com"
	chatGPTBackend    = "https://chatgpt.com/backend-api/codex"
	chatGPTOriginator = "codex_cli_rs"
	chatGPTUserAgent  = "Postwerk"

	// Refresh this long before the access token expires.
	chatGPTRefreshWindow = 5 * time.Minute
	// Tokens without a known expiry are refreshed after this time.
	chatGPTRefreshInterval = 8 * 24 * time.Hour
	chatGPTAuthTimeout     = 45 * time.Second
)

// ChatGPTModels are the models of the Codex catalog that are listed for
// ChatGPT sign-ins, by priority. Other model names can be entered freely.
var ChatGPTModels = []string{
	"gpt-6.2-sol", "gpt-6.2-astra", "gpt-6.1-terra", "gpt-6.1-sol", "gpt-6-astra",
	"gpt-6-sol", "gpt-6-luna", "gpt-5.6-sol", "gpt-5.6-terra", "gpt-5.6-luna", "gpt-5.5",
}

// Variables so tests can run the device flow quickly.
var (
	chatGPTPollUnit     = time.Second
	chatGPTLoginTimeout = 15 * time.Minute
)

var (
	// ErrNotConnected means the chatgpt provider has no valid sign-in.
	ErrNotConnected = errors.New("ChatGPT ist nicht verbunden – bitte in den KI-Einstellungen mit ChatGPT anmelden")
	// ErrRateLimited matches errors of exhausted usage or rate limits.
	ErrRateLimited = errors.New("rate limited")
	// ErrUnavailable matches temporary failures worth retrying later.
	ErrUnavailable = errors.New("temporarily unavailable")
)

// LimitError reports an exhausted rate or usage limit (HTTP 429).
type LimitError struct {
	Msg string
	// RetryAt is when the limit resets (zero if unknown).
	RetryAt time.Time
	at      time.Time
}

func (e *LimitError) Error() string        { return e.Msg }
func (e *LimitError) Is(target error) bool { return target == ErrRateLimited }

// tempError is a temporary failure (e.g. token refresh not possible right now).
type tempError struct{ msg string }

func (e *tempError) Error() string        { return e.msg }
func (e *tempError) Is(target error) bool { return target == ErrUnavailable }

// RetryAt returns when a rate-limited request may be retried (zero if unknown).
func RetryAt(err error) time.Time {
	var le *LimitError
	if errors.As(err, &le) {
		return le.RetryAt
	}
	var ae *apiError
	if errors.As(err, &ae) && ae.RetryAfter > 0 {
		return time.Now().Add(ae.RetryAfter)
	}
	return time.Time{}
}

// chatGPTState is the in-memory part of the chatgpt provider.
type chatGPTState struct {
	refreshMu sync.Mutex // single flight for token refreshes
	mu        sync.Mutex
	login     *deviceLogin
	limit     *LimitError
}

// ChatGPTLogin is the state of a device-code sign-in.
type ChatGPTLogin struct {
	ID string `json:"id"`
	// Status: pending | success | expired | denied | failed | canceled
	Status          string    `json:"status"`
	UserCode        string    `json:"userCode"`
	VerificationURL string    `json:"verificationUrl"`
	ExpiresAt       time.Time `json:"expiresAt"`
	Interval        int       `json:"interval"`
	Error           string    `json:"error,omitempty"`
	Email           string    `json:"email,omitempty"`
	Plan            string    `json:"plan,omitempty"`
}

type deviceLogin struct {
	ChatGPTLogin
	deviceAuthID string
	cancel       context.CancelFunc
}

// ChatGPTStatus is what the admin API shows of the sign-in: never tokens.
type ChatGPTStatus struct {
	Connected   bool          `json:"connected"`
	Email       string        `json:"email,omitempty"`
	Plan        string        `json:"plan,omitempty"`
	ExpiresAt   *time.Time    `json:"expiresAt,omitempty"`
	ConnectedAt *time.Time    `json:"connectedAt,omitempty"`
	Error       string        `json:"error,omitempty"`
	Limit       string        `json:"limit,omitempty"`
	LimitUntil  *time.Time    `json:"limitUntil,omitempty"`
	Login       *ChatGPTLogin `json:"login,omitempty"`
}

func (c *Client) issuer() string {
	if c.ChatGPTIssuer != "" {
		return strings.TrimRight(c.ChatGPTIssuer, "/")
	}
	return chatGPTIssuer
}

func (c *Client) backend() string {
	if c.ChatGPTBackend != "" {
		return strings.TrimRight(c.ChatGPTBackend, "/")
	}
	return chatGPTBackend
}

func (c *Client) chatGPTChanged(connected bool) {
	if c.OnChatGPTChange != nil {
		c.OnChatGPTChange(connected)
	}
}

// ---- status ----

// ChatGPTStatus returns the sign-in state for the admin interface.
func (c *Client) ChatGPTStatus(ctx context.Context) (*ChatGPTStatus, error) {
	a, err := c.Settings.Store().ChatGPTAuth(ctx)
	if err != nil {
		return nil, err
	}
	st := &ChatGPTStatus{}
	if a != nil {
		st.Connected = a.Connected()
		st.Email, st.Plan, st.Error = a.Email, a.Plan, a.Error
		if !a.ExpiresAt.IsZero() && st.Connected {
			t := a.ExpiresAt
			st.ExpiresAt = &t
		}
		if !a.ConnectedAt.IsZero() {
			t := a.ConnectedAt
			st.ConnectedAt = &t
		}
	}
	if l := c.ChatGPTLimit(); l != nil {
		st.Limit = l.Msg
		if !l.RetryAt.IsZero() {
			t := l.RetryAt
			st.LimitUntil = &t
		}
	}
	c.cg.mu.Lock()
	if c.cg.login != nil && c.cg.login.Status == "pending" {
		l := c.cg.login.ChatGPTLogin
		st.Login = &l
	}
	c.cg.mu.Unlock()
	return st, nil
}

// ChatGPTLimit returns the usage limit hit last, while it is still active.
func (c *Client) ChatGPTLimit() *LimitError {
	c.cg.mu.Lock()
	defer c.cg.mu.Unlock()
	l := c.cg.limit
	if l == nil {
		return nil
	}
	now := time.Now()
	if (!l.RetryAt.IsZero() && now.Before(l.RetryAt)) || (l.RetryAt.IsZero() && now.Sub(l.at) < 15*time.Minute) {
		cp := *l
		return &cp
	}
	return nil
}

func (c *Client) setLimit(l *LimitError) {
	c.cg.mu.Lock()
	c.cg.limit = l
	c.cg.mu.Unlock()
}

// ---- device-code sign-in ----

// postAuth sends a JSON or form body to the sign-in service.
func (c *Client) postAuth(ctx context.Context, u, contentType string, body []byte) (int, []byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u, bytes.NewReader(body))
	if err != nil {
		return 0, nil, err
	}
	req.Header.Set("Content-Type", contentType)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", chatGPTUserAgent)
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	return resp.StatusCode, data, err
}

func jsonBody(v any) []byte {
	b, _ := json.Marshal(v)
	return b
}

// flexInt accepts a number or a numeric string ("5").
type flexInt int

func (f *flexInt) UnmarshalJSON(b []byte) error {
	s := strings.Trim(strings.TrimSpace(string(b)), `"`)
	if s == "" || s == "null" {
		*f = 0
		return nil
	}
	n, err := strconv.Atoi(s)
	if err != nil {
		return err
	}
	*f = flexInt(n)
	return nil
}

// StartChatGPTLogin requests a user code and polls for the sign-in in the
// background. A login already running is canceled.
func (c *Client) StartChatGPTLogin(ctx context.Context) (*ChatGPTLogin, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	status, data, err := c.postAuth(ctx, c.issuer()+"/api/accounts/deviceauth/usercode", "application/json",
		jsonBody(map[string]string{"client_id": ChatGPTClientID}))
	if err != nil {
		return nil, fmt.Errorf("ChatGPT-Anmeldedienst nicht erreichbar: %w", err)
	}
	if status == http.StatusNotFound {
		return nil, errors.New("die Anmeldung per Gerätecode wird vom ChatGPT-Anmeldedienst derzeit nicht angeboten")
	}
	if status >= 300 {
		return nil, fmt.Errorf("ChatGPT-Anmeldedienst: HTTP %d %s", status, oauthErrorText(data))
	}
	var uc struct {
		DeviceAuthID string  `json:"device_auth_id"`
		UserCode     string  `json:"user_code"`
		UserCodeAlt  string  `json:"usercode"`
		Interval     flexInt `json:"interval"`
	}
	if err := json.Unmarshal(data, &uc); err != nil {
		return nil, fmt.Errorf("ChatGPT-Anmeldedienst: unerwartete Antwort: %w", err)
	}
	if uc.UserCode == "" {
		uc.UserCode = uc.UserCodeAlt
	}
	if uc.DeviceAuthID == "" || uc.UserCode == "" {
		return nil, errors.New("ChatGPT-Anmeldedienst: kein Code erhalten")
	}
	interval := int(uc.Interval)
	if interval <= 0 {
		interval = 5
	}
	id := make([]byte, 16)
	_, _ = rand.Read(id)
	pollCtx, pollCancel := context.WithTimeout(context.Background(), chatGPTLoginTimeout)
	l := &deviceLogin{
		ChatGPTLogin: ChatGPTLogin{
			ID: hex.EncodeToString(id), Status: "pending", UserCode: uc.UserCode,
			VerificationURL: c.issuer() + "/codex/device", ExpiresAt: time.Now().Add(chatGPTLoginTimeout), Interval: interval,
		},
		deviceAuthID: uc.DeviceAuthID,
		cancel:       pollCancel,
	}
	c.cg.mu.Lock()
	if old := c.cg.login; old != nil && old.Status == "pending" {
		old.cancel()
	}
	c.cg.login = l
	out := l.ChatGPTLogin
	c.cg.mu.Unlock()
	go c.pollChatGPTLogin(pollCtx, l)
	return &out, nil
}

// ChatGPTLoginStatus returns the state of the sign-in with this id.
func (c *Client) ChatGPTLoginStatus(id string) (*ChatGPTLogin, bool) {
	c.cg.mu.Lock()
	defer c.cg.mu.Unlock()
	if c.cg.login == nil || c.cg.login.ID != id {
		return nil, false
	}
	out := c.cg.login.ChatGPTLogin
	return &out, true
}

func (c *Client) finishLogin(l *deviceLogin, status, msg string, a *store.ChatGPTAuth) {
	c.cg.mu.Lock()
	defer c.cg.mu.Unlock()
	if l.Status != "pending" {
		return
	}
	l.Status, l.Error = status, msg
	if a != nil {
		l.Email, l.Plan = a.Email, a.Plan
	}
	l.cancel()
}

func (c *Client) pollChatGPTLogin(ctx context.Context, l *deviceLogin) {
	defer l.cancel()
	wait := time.Duration(l.Interval) * chatGPTPollUnit
	body := jsonBody(map[string]string{"device_auth_id": l.deviceAuthID, "user_code": l.UserCode})
	for {
		status, data, err := c.postAuth(ctx, c.issuer()+"/api/accounts/deviceauth/token", "application/json", body)
		switch {
		case ctx.Err() != nil:
			// handled below
		case err != nil:
			// Network trouble: keep trying until the code expires.
		case status == http.StatusOK:
			var code struct {
				AuthorizationCode string `json:"authorization_code"`
				CodeChallenge     string `json:"code_challenge"`
				CodeVerifier      string `json:"code_verifier"`
			}
			if err := json.Unmarshal(data, &code); err != nil || code.AuthorizationCode == "" {
				c.finishLogin(l, "failed", "Unerwartete Antwort des ChatGPT-Anmeldedienstes.", nil)
				return
			}
			a, err := c.exchangeChatGPTCode(ctx, code.AuthorizationCode, code.CodeVerifier)
			if err != nil {
				c.finishLogin(l, "failed", err.Error(), nil)
				return
			}
			// Save under both locks: a logout or token refresh running at the
			// same time must neither be undone nor undo this sign-in.
			c.cg.refreshMu.Lock()
			c.cg.mu.Lock()
			canceled := l.Status != "pending"
			if !canceled {
				err = c.Settings.Store().SaveChatGPTAuth(context.WithoutCancel(ctx), a)
			}
			c.cg.mu.Unlock()
			c.cg.refreshMu.Unlock()
			if canceled {
				return
			}
			if err != nil {
				c.finishLogin(l, "failed", "Anmeldung konnte nicht gespeichert werden: "+err.Error(), nil)
				return
			}
			c.setLimit(nil)
			c.chatGPTChanged(true)
			c.finishLogin(l, "success", "", a)
			return
		case status == http.StatusForbidden || status == http.StatusNotFound:
			// Not confirmed yet.
		case status == http.StatusTooManyRequests || status >= 500:
			wait += chatGPTPollUnit
		default:
			c.finishLogin(l, "denied", fmt.Sprintf("Die Anmeldung wurde abgelehnt (HTTP %d) %s", status, oauthErrorText(data)), nil)
			return
		}
		select {
		case <-ctx.Done():
			if errors.Is(ctx.Err(), context.DeadlineExceeded) {
				c.finishLogin(l, "expired", "Der Code ist abgelaufen – bitte die Anmeldung neu starten.", nil)
			} else {
				c.finishLogin(l, "canceled", "Die Anmeldung wurde abgebrochen.", nil)
			}
			return
		case <-time.After(wait):
		}
	}
}

// tokenResponse is the answer of {issuer}/oauth/token.
type tokenResponse struct {
	IDToken      string `json:"id_token"`
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	ExpiresIn    int64  `json:"expires_in"`
}

// exchangeChatGPTCode trades the authorization code for tokens.
func (c *Client) exchangeChatGPTCode(ctx context.Context, code, verifier string) (*store.ChatGPTAuth, error) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), chatGPTAuthTimeout)
	defer cancel()
	form := url.Values{
		"grant_type":    {"authorization_code"},
		"client_id":     {ChatGPTClientID},
		"code":          {code},
		"redirect_uri":  {c.issuer() + "/deviceauth/callback"},
		"code_verifier": {verifier},
	}
	status, data, err := c.postAuth(ctx, c.issuer()+"/oauth/token", "application/x-www-form-urlencoded", []byte(form.Encode()))
	if err != nil {
		return nil, fmt.Errorf("Token-Austausch fehlgeschlagen: %w", err)
	}
	if status >= 300 {
		return nil, fmt.Errorf("Token-Austausch fehlgeschlagen: HTTP %d %s", status, oauthErrorText(data))
	}
	var tr tokenResponse
	if err := json.Unmarshal(data, &tr); err != nil || tr.AccessToken == "" || tr.RefreshToken == "" {
		return nil, errors.New("Token-Austausch fehlgeschlagen: unvollständige Antwort")
	}
	now := time.Now()
	a := &store.ChatGPTAuth{ConnectedAt: now}
	applyTokens(a, tr, now)
	return a, nil
}

// applyTokens stores new tokens and the claims of the ID token.
func applyTokens(a *store.ChatGPTAuth, tr tokenResponse, now time.Time) {
	if tr.AccessToken != "" {
		a.AccessToken = tr.AccessToken
	}
	if tr.RefreshToken != "" {
		a.RefreshToken = tr.RefreshToken
	}
	if tr.IDToken != "" {
		a.IDToken = tr.IDToken
	}
	a.LastRefresh = now
	a.Error, a.ErrorAt = "", time.Time{}
	if c, ok := jwtClaims(a.IDToken); ok {
		if e := firstNonEmpty(c.Email, c.Profile.Email); e != "" {
			a.Email = e
		}
		if c.Auth.PlanType != "" {
			a.Plan = planName(c.Auth.PlanType)
		}
		if c.Auth.AccountID != "" {
			a.AccountID = c.Auth.AccountID
		}
	}
	a.ExpiresAt = time.Time{}
	if c, ok := jwtClaims(a.AccessToken); ok {
		if a.AccountID == "" && c.Auth.AccountID != "" {
			a.AccountID = c.Auth.AccountID
		}
		if c.Exp > 0 {
			a.ExpiresAt = time.Unix(c.Exp, 0)
		}
	}
	if a.ExpiresAt.IsZero() && tr.ExpiresIn > 0 {
		a.ExpiresAt = now.Add(time.Duration(tr.ExpiresIn) * time.Second)
	}
}

type tokenClaims struct {
	Email   string `json:"email"`
	Exp     int64  `json:"exp"`
	Profile struct {
		Email string `json:"email"`
	} `json:"https://api.openai.com/profile"`
	Auth struct {
		PlanType  string `json:"chatgpt_plan_type"`
		AccountID string `json:"chatgpt_account_id"`
	} `json:"https://api.openai.com/auth"`
}

// jwtClaims decodes the payload of a JWT without verifying it: the token
// comes straight from the sign-in service over TLS.
func jwtClaims(tok string) (*tokenClaims, bool) {
	parts := strings.Split(tok, ".")
	if len(parts) != 3 || parts[1] == "" {
		return nil, false
	}
	b, err := base64.RawURLEncoding.DecodeString(strings.TrimRight(parts[1], "="))
	if err != nil {
		return nil, false
	}
	var c tokenClaims
	if json.Unmarshal(b, &c) != nil {
		return nil, false
	}
	return &c, true
}

func planName(p string) string {
	p = strings.TrimSpace(p)
	if p == "" {
		return ""
	}
	return strings.ToUpper(p[:1]) + p[1:]
}

func firstNonEmpty(v ...string) string {
	for _, s := range v {
		if s != "" {
			return s
		}
	}
	return ""
}

// oauthErrorText extracts error/error_description of an OAuth error body.
func oauthErrorText(data []byte) string {
	code, desc := oauthError(data)
	switch {
	case desc != "":
		return "(" + printable(truncate(desc, 160)) + ")"
	case code != "":
		return "(" + printable(code) + ")"
	}
	return ""
}

// oauthError reads {"error":"x","error_description":"y"} and
// {"error":{"code":"x","message":"y"}}.
func oauthError(data []byte) (code, desc string) {
	var e struct {
		Error            json.RawMessage `json:"error"`
		ErrorDescription string          `json:"error_description"`
		Code             string          `json:"code"`
	}
	if json.Unmarshal(data, &e) != nil {
		return "", ""
	}
	var s string
	if json.Unmarshal(e.Error, &s) == nil {
		return s, e.ErrorDescription
	}
	var o struct {
		Code    string `json:"code"`
		Type    string `json:"type"`
		Message string `json:"message"`
	}
	if json.Unmarshal(e.Error, &o) == nil {
		return firstNonEmpty(o.Code, o.Type, e.Code), firstNonEmpty(o.Message, e.ErrorDescription)
	}
	return e.Code, e.ErrorDescription
}

// ---- tokens ----

func needsRefresh(a *store.ChatGPTAuth, now time.Time) bool {
	if !a.ExpiresAt.IsZero() {
		return !now.Add(chatGPTRefreshWindow).Before(a.ExpiresAt)
	}
	return a.LastRefresh.IsZero() || now.Sub(a.LastRefresh) > chatGPTRefreshInterval
}

// chatGPTAuth returns valid tokens, refreshing them when they are about to
// expire, or when rejected is the access token the backend just refused.
// Refreshes run one at a time; a caller that waited for a concurrent
// refresh uses its result instead of refreshing again.
func (c *Client) chatGPTAuth(ctx context.Context, rejected string) (*store.ChatGPTAuth, error) {
	c.cg.refreshMu.Lock()
	defer c.cg.refreshMu.Unlock()
	a, err := c.Settings.Store().ChatGPTAuth(ctx)
	if err != nil {
		return nil, err
	}
	if !a.Connected() {
		if a != nil && a.Error != "" {
			return nil, fmt.Errorf("%w (%s)", ErrNotConnected, a.Error)
		}
		return nil, ErrNotConnected
	}
	if rejected != "" {
		if a.AccessToken != rejected {
			return a, nil // refreshed meanwhile
		}
	} else if !needsRefresh(a, time.Now()) {
		return a, nil
	}
	return c.refreshChatGPT(ctx, a)
}

var permanentRefreshErrors = map[string]string{
	"refresh_token_expired":     "Das Anmelde-Token ist abgelaufen.",
	"refresh_token_reused":      "Das Anmelde-Token wurde bereits verwendet.",
	"refresh_token_invalidated": "Das Anmelde-Token wurde widerrufen.",
	"invalid_grant":             "Das Anmelde-Token ist nicht mehr gültig.",
}

// refreshChatGPT renews the tokens. The caller holds refreshMu. The request
// is not tied to the caller's context: a rotated refresh token must be
// stored even if the caller gave up meanwhile.
func (c *Client) refreshChatGPT(ctx context.Context, a *store.ChatGPTAuth) (*store.ChatGPTAuth, error) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), chatGPTAuthTimeout)
	defer cancel()
	status, data, err := c.postAuth(ctx, c.issuer()+"/oauth/token", "application/json", jsonBody(map[string]string{
		"client_id": ChatGPTClientID, "grant_type": "refresh_token", "refresh_token": a.RefreshToken,
	}))
	if err != nil {
		return nil, &tempError{"ChatGPT-Token konnte nicht erneuert werden: " + err.Error()}
	}
	if status >= 300 {
		code, _ := oauthError(data)
		reason, permanent := permanentRefreshErrors[strings.ToLower(code)]
		if status == http.StatusUnauthorized && !permanent {
			reason, permanent = "Die Anmeldung wurde abgelehnt.", true
		}
		if !permanent {
			return nil, &tempError{fmt.Sprintf("ChatGPT-Token konnte nicht erneuert werden: HTTP %d %s", status, oauthErrorText(data))}
		}
		lost := *a
		lost.AccessToken, lost.RefreshToken, lost.IDToken = "", "", ""
		lost.ExpiresAt = time.Time{}
		lost.Error = reason + " Bitte erneut mit ChatGPT anmelden."
		lost.ErrorAt = time.Now()
		if err := c.Settings.Store().SaveChatGPTAuth(ctx, &lost); err != nil {
			return nil, err
		}
		if c.Log != nil {
			c.Log.Warn("chatgpt sign-in lost", "component", "ai", "status", status, "code", code)
		}
		c.chatGPTChanged(false)
		return nil, fmt.Errorf("%w (%s)", ErrNotConnected, lost.Error)
	}
	var tr tokenResponse
	if err := json.Unmarshal(data, &tr); err != nil || tr.AccessToken == "" {
		return nil, &tempError{"ChatGPT-Token konnte nicht erneuert werden: unvollständige Antwort"}
	}
	next := *a
	applyTokens(&next, tr, time.Now())
	if err := c.Settings.Store().SaveChatGPTAuth(ctx, &next); err != nil {
		return nil, err
	}
	return &next, nil
}

// LogoutChatGPT removes the sign-in (and cancels a pending one). The refresh
// token is revoked on a best-effort basis.
func (c *Client) LogoutChatGPT(ctx context.Context) error {
	c.cg.mu.Lock()
	if l := c.cg.login; l != nil && l.Status == "pending" {
		l.Status, l.Error = "canceled", "Die Anmeldung wurde abgebrochen."
		l.cancel()
	}
	c.cg.limit = nil
	c.cg.mu.Unlock()

	c.cg.refreshMu.Lock()
	defer c.cg.refreshMu.Unlock()
	st := c.Settings.Store()
	a, err := st.ChatGPTAuth(ctx)
	if err != nil {
		return err
	}
	if a != nil && a.RefreshToken != "" {
		rctx, cancel := context.WithTimeout(ctx, 10*time.Second)
		_, _, _ = c.postAuth(rctx, c.issuer()+"/oauth/revoke", "application/json", jsonBody(map[string]string{
			"token": a.RefreshToken, "token_type_hint": "refresh_token", "client_id": ChatGPTClientID,
		}))
		cancel()
	}
	if err := st.SaveChatGPTAuth(ctx, nil); err != nil {
		return err
	}
	c.chatGPTChanged(false)
	return nil
}

// ---- requests ----

type respContent struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

type respItem struct {
	Type    string        `json:"type"`
	Role    string        `json:"role"`
	Content []respContent `json:"content"`
}

func inputMessage(role, text string) respItem {
	return respItem{Type: "message", Role: role, Content: []respContent{{Type: "input_text", Text: text}}}
}

func (c *Client) chatChatGPT(ctx context.Context, cfg store.AISettings, system, user string, jsonMode bool) (string, error) {
	if jsonMode {
		system += jsonOnlyInstruction
	}
	model := strings.TrimSpace(cfg.ChatModel)
	if model == "" {
		model = ChatGPTDefaultModel
	}
	// Same request shape as the Codex CLI: system prompt as developer
	// message, no server-side storage, streamed answer.
	body := map[string]any{
		"model":               model,
		"input":               []respItem{inputMessage("developer", system), inputMessage("user", user)},
		"tool_choice":         "auto",
		"parallel_tool_calls": false,
		"store":               false,
		"stream":              true,
		"include":             []string{},
	}
	a, err := c.chatGPTAuth(ctx, "")
	if err != nil {
		return "", err
	}
	text, err := c.chatGPTRequest(ctx, cfg, a, body)
	var ae *apiError
	if errors.As(err, &ae) && ae.Status == http.StatusUnauthorized {
		if a, err = c.chatGPTAuth(ctx, a.AccessToken); err != nil {
			return "", err
		}
		text, err = c.chatGPTRequest(ctx, cfg, a, body)
	}
	var le *LimitError
	switch {
	case errors.As(err, &le):
		c.setLimit(le)
	case err == nil:
		c.setLimit(nil)
	}
	return text, err
}

func (c *Client) chatGPTRequest(ctx context.Context, cfg store.AISettings, a *store.ChatGPTAuth, body any) (string, error) {
	timeout := time.Duration(cfg.TimeoutSeconds) * time.Second
	if timeout <= 0 {
		timeout = 120 * time.Second
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.backend()+"/responses", bytes.NewReader(jsonBody(body)))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "text/event-stream")
	req.Header.Set("Authorization", "Bearer "+a.AccessToken)
	if a.AccountID != "" {
		req.Header.Set("chatgpt-account-id", a.AccountID)
	}
	req.Header.Set("originator", chatGPTOriginator)
	req.Header.Set("User-Agent", chatGPTUserAgent)
	resp, err := c.HTTP.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return "", fmt.Errorf("ChatGPT antwortet nicht innerhalb von %d Sekunden", int(timeout.Seconds()))
		}
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		data, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
		return "", chatGPTHTTPError(resp.StatusCode, resp.Header, data, time.Now())
	}
	text, err := parseResponsesStream(resp.Body, time.Now())
	if err != nil && ctx.Err() != nil {
		return "", fmt.Errorf("ChatGPT antwortet nicht innerhalb von %d Sekunden", int(timeout.Seconds()))
	}
	return text, err
}

// usageError is the error body of the ChatGPT backend.
type usageError struct {
	Error struct {
		Type            string          `json:"type"`
		Code            string          `json:"code"`
		Message         string          `json:"message"`
		PlanType        json.RawMessage `json:"plan_type"`
		ResetsAt        int64           `json:"resets_at"`
		ResetsInSeconds int64           `json:"resets_in_seconds"`
	} `json:"error"`
	Detail json.RawMessage `json:"detail"`
}

func (u *usageError) message() string {
	if u.Error.Message != "" {
		return printable(truncate(u.Error.Message, 200))
	}
	var s string
	if json.Unmarshal(u.Detail, &s) == nil && s != "" {
		return printable(truncate(s, 200))
	}
	return ""
}

func (u *usageError) plan() string {
	var s string
	if json.Unmarshal(u.Error.PlanType, &s) == nil {
		return planName(s)
	}
	return ""
}

func resetText(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return " – wieder verfügbar ab " + t.Local().Format("02.01. 15:04") + " Uhr"
}

// chatGPTHTTPError turns an error response into a clear (German) error.
func chatGPTHTTPError(status int, h http.Header, data []byte, now time.Time) error {
	var u usageError
	_ = json.Unmarshal(data, &u)
	kind := firstNonEmpty(u.Error.Type, u.Error.Code)
	switch status {
	case http.StatusTooManyRequests:
		le := &LimitError{at: now}
		switch {
		case u.Error.ResetsAt > 0:
			le.RetryAt = time.Unix(u.Error.ResetsAt, 0)
		case u.Error.ResetsInSeconds > 0:
			le.RetryAt = now.Add(time.Duration(u.Error.ResetsInSeconds) * time.Second)
		default:
			if s, err := strconv.Atoi(strings.TrimSpace(h.Get("Retry-After"))); err == nil && s > 0 {
				le.RetryAt = now.Add(time.Duration(s) * time.Second)
			}
		}
		switch kind {
		case "usage_limit_reached":
			plan := ""
			if p := u.plan(); p != "" {
				plan = " (" + p + ")"
			}
			le.Msg = "Das Nutzungslimit des ChatGPT-Abos" + plan + " ist erreicht" + resetText(le.RetryAt) + "."
		case "usage_not_included":
			le.Msg = "Dieses ChatGPT-Abo enthält keine Codex-Nutzung – dafür ist z. B. ein Plus- oder Pro-Abo nötig."
			if le.RetryAt.IsZero() {
				le.RetryAt = now.Add(time.Hour)
			}
		case "insufficient_quota":
			le.Msg = "Das Kontingent des ChatGPT-Kontos ist aufgebraucht" + resetText(le.RetryAt) + "."
		default:
			le.Msg = "Zu viele Anfragen an ChatGPT – bitte später erneut versuchen" + resetText(le.RetryAt) + "."
		}
		return le
	case http.StatusUnauthorized:
		return &apiError{Status: status, Msg: "ChatGPT hat die Anmeldung abgelehnt (HTTP 401)"}
	}
	msg := u.message()
	if msg == "" {
		msg = "(keine Fehlermeldung des Anbieters)"
	}
	if status >= 500 {
		return &apiError{Status: status, Msg: fmt.Sprintf("ChatGPT ist gerade nicht verfügbar (HTTP %d): %s", status, msg)}
	}
	return &apiError{Status: status, Msg: fmt.Sprintf("ChatGPT HTTP %d: %s", status, msg)}
}

type streamEvent struct {
	Type     string          `json:"type"`
	Delta    string          `json:"delta"`
	Item     *respItem       `json:"item"`
	Response *streamResponse `json:"response"`
	Error    *streamError    `json:"error"`
	Code     string          `json:"code"`
	Message  string          `json:"message"`
}

type streamResponse struct {
	Output            []respItem   `json:"output"`
	Error             *streamError `json:"error"`
	IncompleteDetails *struct {
		Reason string `json:"reason"`
	} `json:"incomplete_details"`
}

type streamError struct {
	Type    string `json:"type"`
	Code    string `json:"code"`
	Message string `json:"message"`
}

func outputText(items []respItem) string {
	var b strings.Builder
	for _, it := range items {
		if it.Type != "message" {
			continue
		}
		for _, c := range it.Content {
			if c.Type == "output_text" {
				b.WriteString(c.Text)
			}
		}
	}
	return b.String()
}

func streamFailure(e *streamError, now time.Time) error {
	if e == nil {
		return errors.New("ChatGPT: Anfrage fehlgeschlagen")
	}
	code := firstNonEmpty(e.Code, e.Type)
	msg := printable(truncate(e.Message, 200))
	switch code {
	case "rate_limit_exceeded":
		return &LimitError{Msg: "ChatGPT-Ratenlimit erreicht – bitte später erneut versuchen.", RetryAt: now.Add(time.Minute), at: now}
	case "usage_limit_reached", "insufficient_quota":
		return &LimitError{Msg: "Das Nutzungslimit des ChatGPT-Abos ist erreicht.", at: now}
	case "context_length_exceeded":
		return errors.New("ChatGPT: Der Text ist zu lang für das Modell")
	case "server_is_overloaded", "slow_down":
		return &tempError{"ChatGPT ist gerade überlastet – bitte später erneut versuchen"}
	}
	if msg == "" {
		msg = code
	}
	return errors.New("ChatGPT: " + msg)
}

// parseResponsesStream reads a Responses API event stream (SSE) and returns
// the output text.
func parseResponsesStream(r io.Reader, now time.Time) (string, error) {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 64<<10), 16<<20)
	var text strings.Builder
	var items []respItem
	var data []string
	done := false
	var failure error
	dispatch := func() {
		payload := strings.Join(data, "\n")
		data = data[:0]
		if payload == "" || payload == "[DONE]" || done || failure != nil {
			return
		}
		var ev streamEvent
		if json.Unmarshal([]byte(payload), &ev) != nil {
			return
		}
		switch ev.Type {
		case "response.output_text.delta":
			text.WriteString(ev.Delta)
		case "response.output_item.done":
			if ev.Item != nil {
				items = append(items, *ev.Item)
			}
		case "response.completed":
			done = true
			if text.Len() == 0 {
				if t := outputText(items); t != "" {
					text.WriteString(t)
				} else if ev.Response != nil {
					text.WriteString(outputText(ev.Response.Output))
				}
			}
		case "response.incomplete":
			reason := "unbekannt"
			if ev.Response != nil && ev.Response.IncompleteDetails != nil && ev.Response.IncompleteDetails.Reason != "" {
				reason = ev.Response.IncompleteDetails.Reason
			}
			if reason == "content_filter" {
				failure = errors.New("ChatGPT hat die Antwort aus inhaltlichen Gründen abgebrochen")
			} else {
				failure = errors.New("ChatGPT-Antwort unvollständig: " + printable(reason))
			}
		case "response.failed":
			var e *streamError
			if ev.Response != nil {
				e = ev.Response.Error
			}
			failure = streamFailure(e, now)
		case "error":
			e := ev.Error
			if e == nil {
				e = &streamError{Code: ev.Code, Message: ev.Message}
			}
			failure = streamFailure(e, now)
		}
	}
	for sc.Scan() {
		line := strings.TrimSuffix(sc.Text(), "\r")
		switch {
		case line == "":
			dispatch()
		case strings.HasPrefix(line, ":"):
		case strings.HasPrefix(line, "data:"):
			data = append(data, strings.TrimPrefix(strings.TrimPrefix(line, "data:"), " "))
		}
		if done || failure != nil {
			break
		}
	}
	dispatch()
	if failure != nil {
		return "", failure
	}
	if err := sc.Err(); err != nil && !done {
		return "", fmt.Errorf("ChatGPT-Antwort abgebrochen: %w", err)
	}
	if !done {
		return "", errors.New("ChatGPT-Antwort abgebrochen, bevor sie vollständig war")
	}
	if text.Len() == 0 {
		return "", errors.New("ChatGPT hat keinen Text geliefert")
	}
	return text.String(), nil
}

// chatGPTModels lists the catalog models while signed in.
func (c *Client) chatGPTModels(ctx context.Context) ([]string, error) {
	a, err := c.Settings.Store().ChatGPTAuth(ctx)
	if err != nil {
		return nil, err
	}
	if !a.Connected() {
		return nil, ErrNotConnected
	}
	return append([]string{}, ChatGPTModels...), nil
}
