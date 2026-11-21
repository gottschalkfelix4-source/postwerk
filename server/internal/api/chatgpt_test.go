package api

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"postwerk/internal/ai"
	"postwerk/internal/logx"
)

// fakeChatGPTAuth approves the device code at the first poll.
func fakeChatGPTAuth(t *testing.T) *httptest.Server {
	b64 := func(v any) string {
		b, _ := json.Marshal(v)
		return base64.RawURLEncoding.EncodeToString(b)
	}
	idToken := b64(map[string]string{"alg": "none"}) + "." + b64(map[string]any{
		"email": "admin@example.test", "https://api.openai.com/auth": map[string]string{"chatgpt_plan_type": "pro", "chatgpt_account_id": "acct-9"},
	}) + ".sig"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/accounts/deviceauth/usercode":
			json.NewEncoder(w).Encode(map[string]any{"device_auth_id": "dev", "user_code": "WXYZ-9876", "interval": 5})
		case "/api/accounts/deviceauth/token":
			json.NewEncoder(w).Encode(map[string]string{"authorization_code": "code", "code_challenge": "c", "code_verifier": "v"})
		case "/oauth/token":
			json.NewEncoder(w).Encode(map[string]any{"id_token": idToken, "access_token": "secret-access-token",
				"refresh_token": "secret-refresh-token", "expires_in": 3600})
		case "/oauth/revoke":
			w.Write([]byte("{}"))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestChatGPTLoginEndpointsKeepTokensOnServer(t *testing.T) {
	e := adminEnv(t)
	srv := fakeChatGPTAuth(t)
	e.s.AI = ai.NewClient(e.s.Settings)
	e.s.AI.ChatGPTIssuer = srv.URL
	e.s.Logs = logx.NewRing(10)
	c := e.login(t, "")

	rec := e.do(t, "PUT", "/api/admin/settings", map[string]any{"ai": map[string]any{"enabled": true, "provider": "chatgpt", "chatModel": "gpt-5.5"}}, c)
	if rec.Code != 200 {
		t.Fatalf("provider chatgpt rejected: %d %s", rec.Code, rec.Body.String())
	}
	// Selected but not signed in: dashboard warning.
	if ws := e.s.dashboardWarnings(context.Background()); !hasWarning(ws, "ai-chatgpt-auth") {
		t.Errorf("missing sign-in warning: %+v", ws)
	}

	rec = e.do(t, "POST", "/api/admin/ai/chatgpt/login", nil, c)
	if rec.Code != 200 {
		t.Fatalf("login start: %d %s", rec.Code, rec.Body.String())
	}
	var login ai.ChatGPTLogin
	json.Unmarshal(rec.Body.Bytes(), &login)
	if login.UserCode != "WXYZ-9876" || login.VerificationURL != srv.URL+"/codex/device" || login.ID == "" {
		t.Fatalf("login = %+v", login)
	}
	deadline := time.Now().Add(5 * time.Second)
	for login.Status == "pending" && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
		rec = e.do(t, "GET", "/api/admin/ai/chatgpt/login/"+login.ID, nil, c)
		json.Unmarshal(rec.Body.Bytes(), &login)
	}
	if login.Status != "success" || login.Email != "admin@example.test" || login.Plan != "Pro" {
		t.Fatalf("login = %+v", login)
	}
	if rec := e.do(t, "GET", "/api/admin/ai/chatgpt/login/unknown", nil, c); rec.Code != 404 {
		t.Errorf("unknown login id: %d", rec.Code)
	}

	// Neither the settings nor the status reveal a token.
	for _, path := range []string{"/api/admin/settings", "/api/admin/ai/chatgpt", "/api/admin/dashboard"} {
		rec = e.do(t, "GET", path, nil, c)
		body := rec.Body.String()
		if rec.Code != 200 || strings.Contains(body, "secret-access-token") || strings.Contains(body, "secret-refresh-token") || strings.Contains(body, "eyJ") {
			t.Errorf("%s: %d leaks tokens: %s", path, rec.Code, body)
		}
	}
	rec = e.do(t, "GET", "/api/admin/ai/chatgpt", nil, c)
	var st ai.ChatGPTStatus
	json.Unmarshal(rec.Body.Bytes(), &st)
	if !st.Connected || st.Email != "admin@example.test" || st.Plan != "Pro" || st.ExpiresAt == nil {
		t.Errorf("status = %s", rec.Body.String())
	}
	if ws := e.s.dashboardWarnings(context.Background()); hasWarning(ws, "ai-chatgpt-auth") {
		t.Errorf("warning while signed in: %+v", ws)
	}

	// Tokens are stored encrypted.
	var raw string
	e.s.Store.DB.QueryRow(`SELECT value FROM settings WHERE key = 'chatgpt_auth'`).Scan(&raw)
	if raw == "" || strings.Contains(raw, "secret-") {
		t.Errorf("tokens not encrypted: %q", raw)
	}
	// Saving the AI settings must not touch the sign-in.
	e.do(t, "PUT", "/api/admin/settings", map[string]any{"ai": map[string]any{"chatModel": "gpt-6-sol", "chatgpt": map[string]string{"accessToken": "x"}}}, c)
	if a, _ := e.s.Store.ChatGPTAuth(context.Background()); a == nil || a.AccessToken != "secret-access-token" {
		t.Errorf("sign-in changed by settings: %+v", a)
	}

	if rec := e.do(t, "POST", "/api/admin/ai/chatgpt/logout", nil, c); rec.Code != 200 {
		t.Fatalf("logout: %d", rec.Code)
	}
	if a, _ := e.s.Store.ChatGPTAuth(context.Background()); a != nil {
		t.Errorf("sign-in after logout: %+v", a)
	}
}

func hasWarning(ws []dashWarning, id string) bool {
	for _, w := range ws {
		if w.ID == id {
			return true
		}
	}
	return false
}
