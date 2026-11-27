package ai

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"postwerk/internal/store"
)

// testJWT builds an unsigned JWT with the given claims.
func testJWT(claims map[string]any) string {
	enc := func(v any) string {
		b, _ := json.Marshal(v)
		return base64.RawURLEncoding.EncodeToString(b)
	}
	return enc(map[string]string{"alg": "none"}) + "." + enc(claims) + ".sig"
}

func idToken(email, plan, account string) string {
	return testJWT(map[string]any{
		"email":                          email,
		"https://api.openai.com/auth":    map[string]any{"chatgpt_plan_type": plan, "chatgpt_account_id": account},
		"https://api.openai.com/profile": map[string]any{"email": email},
	})
}

func accessToken(name string, exp time.Time) string {
	return testJWT(map[string]any{"jti": name, "exp": exp.Unix()})
}

// fakeChatGPT mimics the sign-in service (auth.openai.com) and the Codex
// Responses endpoint of the ChatGPT backend.
type fakeChatGPT struct {
	t   *testing.T
	srv *httptest.Server

	mu            sync.Mutex
	pendingPolls  int    // device token polls answered with 403 before approval
	denyPolls     bool   // answer polls with 400
	validAccess   string // access token accepted by /responses
	refreshToken  string // refresh token accepted by /oauth/token
	refreshFail   string // error code returned for refreshes
	refreshes     atomic.Int32
	revoked       []string
	lastBody      map[string]any
	lastHeaders   http.Header
	responseSSE   string
	responseCode  int
	responseError string
	generation    int
}

func newFakeChatGPT(t *testing.T) *fakeChatGPT {
	f := &fakeChatGPT{t: t, refreshToken: "rt-1", responseSSE: sseText("OK")}
	f.srv = httptest.NewServer(http.HandlerFunc(f.handle))
	t.Cleanup(f.srv.Close)
	return f
}

func sseText(parts ...string) string {
	var b strings.Builder
	b.WriteString("event: response.created\ndata: {\"type\":\"response.created\",\"response\":{\"id\":\"resp_1\"}}\n\n")
	for _, p := range parts {
		d, _ := json.Marshal(map[string]string{"type": "response.output_text.delta", "delta": p})
		b.WriteString("event: response.output_text.delta\ndata: " + string(d) + "\n\n")
	}
	b.WriteString("event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_1\",\"output\":[]}}\n\n")
	return b.String()
}

func (f *fakeChatGPT) issue() map[string]any {
	f.generation++
	access := accessToken(fmt.Sprintf("at-%d", f.generation), time.Now().Add(time.Hour))
	f.validAccess = access
	f.refreshToken = fmt.Sprintf("rt-%d", f.generation+1)
	return map[string]any{
		"id_token":      idToken("admin@example.test", "plus", "acct-123"),
		"access_token":  access,
		"refresh_token": f.refreshToken,
	}
}

func (f *fakeChatGPT) handle(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	body, _ := io.ReadAll(r.Body)
	jsonReply := func(code int, v any) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(code)
		json.NewEncoder(w).Encode(v)
	}
	switch r.URL.Path {
	case "/api/accounts/deviceauth/usercode":
		var req map[string]string
		json.Unmarshal(body, &req)
		if req["client_id"] != ChatGPTClientID {
			f.t.Errorf("usercode client_id = %q", req["client_id"])
		}
		jsonReply(200, map[string]any{"device_auth_id": "dev-1", "user_code": "ABCD-1234", "interval": "1"})
	case "/api/accounts/deviceauth/token":
		var req map[string]string
		json.Unmarshal(body, &req)
		if req["device_auth_id"] != "dev-1" || req["user_code"] != "ABCD-1234" {
			f.t.Errorf("poll body = %v", req)
		}
		switch {
		case f.denyPolls:
			jsonReply(400, map[string]string{"error": "access_denied"})
		case f.pendingPolls > 0:
			f.pendingPolls--
			jsonReply(403, map[string]string{"error": "authorization_pending"})
		default:
			jsonReply(200, map[string]string{"authorization_code": "code-1", "code_challenge": "challenge", "code_verifier": "verifier-1"})
		}
	case "/oauth/token":
		if strings.HasPrefix(r.Header.Get("Content-Type"), "application/x-www-form-urlencoded") {
			v, _ := url.ParseQuery(string(body))
			want := map[string]string{"grant_type": "authorization_code", "client_id": ChatGPTClientID, "code": "code-1",
				"code_verifier": "verifier-1", "redirect_uri": f.srv.URL + "/deviceauth/callback"}
			for k, exp := range want {
				if v.Get(k) != exp {
					f.t.Errorf("token exchange %s = %q, want %q", k, v.Get(k), exp)
				}
			}
			jsonReply(200, f.issue())
			return
		}
		var req map[string]string
		json.Unmarshal(body, &req)
		if req["grant_type"] != "refresh_token" || req["client_id"] != ChatGPTClientID {
			f.t.Errorf("refresh body = %v", req)
		}
		f.refreshes.Add(1)
		if f.refreshFail != "" {
			jsonReply(400, map[string]any{"error": map[string]string{"code": f.refreshFail, "message": "nope"}})
			return
		}
		if req["refresh_token"] != f.refreshToken {
			jsonReply(400, map[string]any{"error": map[string]string{"code": "refresh_token_reused"}})
			return
		}
		jsonReply(200, f.issue())
	case "/oauth/revoke":
		var req map[string]string
		json.Unmarshal(body, &req)
		f.revoked = append(f.revoked, req["token"])
		jsonReply(200, map[string]string{})
	case "/responses":
		if r.Header.Get("Authorization") != "Bearer "+f.validAccess {
			jsonReply(401, map[string]any{"error": map[string]string{"message": "token expired"}})
			return
		}
		f.lastHeaders = r.Header.Clone()
		f.lastBody = nil
		json.Unmarshal(body, &f.lastBody)
		if f.responseCode != 0 {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(f.responseCode)
			io.WriteString(w, f.responseError)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, f.responseSSE)
	default:
		http.NotFound(w, r)
	}
}

// chatGPTSetup returns a worker whose client talks to the fake service.
func chatGPTSetup(t *testing.T) (*store.Store, *Worker, *store.Message, *fakeChatGPT) {
	f := newFakeChatGPT(t)
	st, w, m := setup(t, "http://unused.invalid")
	ctx := context.Background()
	cfg := w.Client.Settings.Get()
	cfg.AI.Provider = ProviderChatGPT
	cfg.AI.BaseURL = ""
	cfg.AI.ChatModel = "gpt-test"
	cfg.AI.TimeoutSeconds = 10
	w.Client.Settings.Save(ctx, cfg)
	w.Client.ChatGPTIssuer = f.srv.URL
	w.Client.ChatGPTBackend = f.srv.URL
	w.Log = slog.New(slog.NewTextHandler(io.Discard, nil))
	return st, w, m, f
}

func fastPolling(t *testing.T, timeout time.Duration) {
	unit, to := chatGPTPollUnit, chatGPTLoginTimeout
	chatGPTPollUnit, chatGPTLoginTimeout = time.Millisecond, timeout
	t.Cleanup(func() { chatGPTPollUnit, chatGPTLoginTimeout = unit, to })
}

func waitLogin(t *testing.T, c *Client, id string) *ChatGPTLogin {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		l, ok := c.ChatGPTLoginStatus(id)
		if !ok {
			t.Fatal("login vanished")
		}
		if l.Status != "pending" {
			return l
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("login still pending")
	return nil
}

// storeAuth saves a sign-in directly.
func storeAuth(t *testing.T, st *store.Store, f *fakeChatGPT, access string, exp time.Time) {
	t.Helper()
	f.mu.Lock()
	f.validAccess = accessToken("at-valid", time.Now().Add(time.Hour))
	f.mu.Unlock()
	a := &store.ChatGPTAuth{AccessToken: access, RefreshToken: "rt-1", IDToken: idToken("admin@example.test", "pro", "acct-123"),
		AccountID: "acct-123", Email: "admin@example.test", Plan: "Pro", ExpiresAt: exp, LastRefresh: time.Now(), ConnectedAt: time.Now()}
	if err := st.SaveChatGPTAuth(context.Background(), a); err != nil {
		t.Fatal(err)
	}
}

func TestChatGPTDeviceLoginAndChat(t *testing.T) {
	fastPolling(t, 5*time.Second)
	st, w, m, f := chatGPTSetup(t)
	ctx := context.Background()
	c := w.Client
	f.pendingPolls = 2
	var changes []bool
	c.OnChatGPTChange = func(connected bool) { changes = append(changes, connected) }

	if _, err := c.Models(ctx); !errors.Is(err, ErrNotConnected) {
		t.Fatalf("models before sign-in: %v", err)
	}
	l, err := c.StartChatGPTLogin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if l.Status != "pending" || l.UserCode != "ABCD-1234" || l.VerificationURL != f.srv.URL+"/codex/device" || l.Interval != 1 {
		t.Fatalf("login = %+v", l)
	}
	done := waitLogin(t, c, l.ID)
	if done.Status != "success" || done.Email != "admin@example.test" || done.Plan != "Plus" {
		t.Fatalf("login result = %+v", done)
	}
	if len(changes) != 1 || !changes[0] {
		t.Errorf("change callbacks = %v", changes)
	}
	a, _ := st.ChatGPTAuth(ctx)
	if !a.Connected() || a.AccountID != "acct-123" || a.RefreshToken != "rt-2" || a.ExpiresAt.IsZero() {
		t.Fatalf("stored auth = %+v", a)
	}
	status, _ := c.ChatGPTStatus(ctx)
	if !status.Connected || status.Email != "admin@example.test" || status.Plan != "Plus" || status.ExpiresAt == nil {
		t.Fatalf("status = %+v", status)
	}

	models, err := c.Models(ctx)
	if err != nil || len(models) == 0 || models[0] != ChatGPTDefaultModel {
		t.Fatalf("models = %v, %v", models, err)
	}

	// The analysis worker runs over the Responses stream; JSON comes from
	// the instructions, not from a response_format.
	f.responseSSE = sseText("```json\n", `{"category":"Rechnung","priority":"high",`, `"spam_probability":0.01,"phishing":false,"reasons":[],"summary":"Nebenkosten","tags":[],"events":[],"tasks":[]}`, "\n```")
	cfg := c.Settings.Get()
	cfg.AI.Categories = []string{"Rechnung"}
	c.Settings.Save(ctx, cfg)
	w.process(ctx)
	got, _ := st.GetMessageAny(ctx, m.ID)
	if got.Category == nil || *got.Category != "Rechnung" || got.Priority != "high" {
		t.Fatalf("analysis not stored: %+v", got)
	}
	body := f.lastBody
	if body["model"] != "gpt-test" || body["store"] != false || body["stream"] != true {
		t.Errorf("request body = %v", body)
	}
	if _, ok := body["response_format"]; ok {
		t.Error("response_format must not be sent")
	}
	input, _ := body["input"].([]any)
	if len(input) != 2 {
		t.Fatalf("input = %v", input)
	}
	dev := input[0].(map[string]any)
	devText := dev["content"].([]any)[0].(map[string]any)["text"].(string)
	if dev["role"] != "developer" || !strings.Contains(devText, "JSON") {
		t.Errorf("developer message = %v", dev)
	}
	if input[1].(map[string]any)["role"] != "user" {
		t.Errorf("user message = %v", input[1])
	}
	h := f.lastHeaders
	if h.Get("chatgpt-account-id") != "acct-123" || h.Get("originator") != chatGPTOriginator || h.Get("Accept") != "text/event-stream" {
		t.Errorf("headers = %v", h)
	}

	// No embeddings on this backend: semantic search needs its own endpoint.
	cfg.AI.EmbeddingModel = "test-embed"
	c.Settings.Save(ctx, cfg)
	if c.CanEmbed() {
		t.Error("chatgpt provider has no embeddings of its own")
	}
	embed := fakeEndpoint(t, "")
	defer embed.Close()
	cfg.AI.EmbeddingBaseURL = embed.URL + "/v1"
	c.Settings.Save(ctx, cfg)
	if !c.CanEmbed() {
		t.Error("separate embedding endpoint configured")
	}

	res, err := c.TestConnection(ctx)
	if err != nil || !res.ChatOK || !res.EmbeddingOK || len(res.Models) == 0 {
		t.Fatalf("test connection = %+v, %v", res, err)
	}

	// Logout revokes the refresh token and removes the sign-in.
	if err := c.LogoutChatGPT(ctx); err != nil {
		t.Fatal(err)
	}
	if a, _ := st.ChatGPTAuth(ctx); a != nil {
		t.Errorf("auth after logout = %+v", a)
	}
	if len(f.revoked) != 1 || f.revoked[0] != "rt-2" {
		t.Errorf("revoked = %v", f.revoked)
	}
	if _, err := c.Chat(ctx, "s", "u", false); !errors.Is(err, ErrNotConnected) {
		t.Errorf("chat after logout: %v", err)
	}
}

func TestChatGPTLoginExpired(t *testing.T) {
	fastPolling(t, 50*time.Millisecond)
	st, w, _, f := chatGPTSetup(t)
	f.pendingPolls = 1 << 30
	l, err := w.Client.StartChatGPTLogin(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	done := waitLogin(t, w.Client, l.ID)
	if done.Status != "expired" || done.Error == "" {
		t.Fatalf("login = %+v", done)
	}
	if a, _ := st.ChatGPTAuth(context.Background()); a != nil {
		t.Errorf("nothing must be stored: %+v", a)
	}
}

func TestChatGPTLoginDenied(t *testing.T) {
	fastPolling(t, 5*time.Second)
	_, w, _, f := chatGPTSetup(t)
	f.denyPolls = true
	l, err := w.Client.StartChatGPTLogin(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if done := waitLogin(t, w.Client, l.ID); done.Status != "denied" {
		t.Fatalf("login = %+v", done)
	}
}

func TestChatGPTRefreshBeforeExpiry(t *testing.T) {
	st, w, _, f := chatGPTSetup(t)
	ctx := context.Background()
	storeAuth(t, st, f, "at-old", time.Now().Add(time.Minute)) // inside the refresh window
	text, err := w.Client.Chat(ctx, "sys", "hallo", false)
	if err != nil || text != "OK" {
		t.Fatalf("chat = %q, %v", text, err)
	}
	if n := f.refreshes.Load(); n != 1 {
		t.Errorf("refreshes = %d", n)
	}
	a, _ := st.ChatGPTAuth(ctx)
	if a.AccessToken != f.validAccess || a.RefreshToken != "rt-2" || a.Plan != "Plus" || time.Until(a.ExpiresAt) < 30*time.Minute {
		t.Errorf("refreshed auth not stored: %+v", a)
	}
}

func TestChatGPTRefreshOn401SingleFlight(t *testing.T) {
	st, w, _, f := chatGPTSetup(t)
	ctx := context.Background()
	storeAuth(t, st, f, "at-revoked", time.Now().Add(time.Hour)) // looks valid, backend refuses it
	var wg sync.WaitGroup
	errs := make(chan error, 5)
	for range 5 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := w.Client.Chat(ctx, "sys", "hallo", false); err != nil {
				errs <- err
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Error(err)
	}
	if n := f.refreshes.Load(); n != 1 {
		t.Errorf("refreshes = %d, want exactly one", n)
	}
}

func TestChatGPTRefreshFailsPermanently(t *testing.T) {
	st, w, _, f := chatGPTSetup(t)
	ctx := context.Background()
	storeAuth(t, st, f, "at-old", time.Now().Add(-time.Minute))
	f.refreshFail = "refresh_token_expired"
	lost := false
	w.Client.OnChatGPTChange = func(connected bool) { lost = !connected }
	_, err := w.Client.Chat(ctx, "sys", "hallo", false)
	if !errors.Is(err, ErrNotConnected) || !strings.Contains(err.Error(), "abgelaufen") {
		t.Fatalf("err = %v", err)
	}
	a, _ := st.ChatGPTAuth(ctx)
	if a == nil || a.Connected() || a.AccessToken != "" || a.RefreshToken != "" || a.Error == "" || a.Email == "" {
		t.Fatalf("auth = %+v", a)
	}
	if !lost {
		t.Error("lost sign-in not reported")
	}
	// No further refresh attempts once the sign-in is gone.
	_, _ = w.Client.Chat(ctx, "sys", "hallo", false)
	if n := f.refreshes.Load(); n != 1 {
		t.Errorf("refreshes = %d", n)
	}
	status, _ := w.Client.ChatGPTStatus(ctx)
	if status.Connected || status.Error == "" {
		t.Errorf("status = %+v", status)
	}
}

func TestChatGPTUsageLimitPausesWorker(t *testing.T) {
	st, w, m, f := chatGPTSetup(t)
	ctx := context.Background()
	storeAuth(t, st, f, "at-valid", time.Now().Add(time.Hour))
	f.validAccess = "at-valid"
	reset := time.Now().Add(3 * time.Hour).Truncate(time.Second)
	f.responseCode = 429
	f.responseError = fmt.Sprintf(`{"error":{"type":"usage_limit_reached","message":"The usage limit has been reached","plan_type":"plus","resets_at":%d}}`, reset.Unix())

	_, err := w.Client.Chat(ctx, "sys", "hallo", true)
	if !errors.Is(err, ErrRateLimited) || !strings.Contains(err.Error(), "Nutzungslimit") || !strings.Contains(err.Error(), "(Plus)") {
		t.Fatalf("err = %v", err)
	}
	if !RetryAt(err).Equal(reset) {
		t.Errorf("retry at = %v, want %v", RetryAt(err), reset)
	}
	if l := w.Client.ChatGPTLimit(); l == nil || !l.RetryAt.Equal(reset) {
		t.Errorf("limit = %+v", l)
	}

	// The worker pauses instead of giving up on the message.
	w.process(ctx)
	if until := w.PausedUntil(); !until.Equal(reset) {
		t.Errorf("paused until %v, want %v", until, reset)
	}
	if state := mustAIState(t, st, m.ID); state.Attempts != 0 || state.NextAttemptAt != nil {
		t.Errorf("message must not consume a retry attempt: %+v", state)
	}
	got, _ := st.GetMessageAny(ctx, m.ID)
	if got.Category != nil {
		t.Error("message must stay pending")
	}

	// After the reset (here: Resume) the message is analyzed.
	f.responseCode = 0
	f.responseSSE = sseText(`{"category":"Rechnung","priority":"normal","spam_probability":0,"phishing":false,"reasons":[],"summary":"x","tags":[],"events":[],"tasks":[]}`)
	cfg := w.Client.Settings.Get()
	cfg.AI.Categories = []string{"Rechnung"}
	w.Client.Settings.Save(ctx, cfg)
	w.Resume()
	w.process(ctx)
	got, _ = st.GetMessageAny(ctx, m.ID)
	if got.Category == nil {
		t.Error("message not analyzed after the pause")
	}
	if w.Client.ChatGPTLimit() != nil {
		t.Error("limit cleared after a successful request")
	}
}

func TestParseResponsesStream(t *testing.T) {
	now := time.Now()
	stream := ": keep-alive\r\n" +
		"event: response.output_text.delta\r\ndata: {\"type\":\"response.output_text.delta\",\"delta\":\"Hallo \"}\r\n\r\n" +
		"data: {\"type\":\"response.output_text.delta\",\"delta\":\"Welt\"}\n\n" +
		"data: {\"type\":\"response.output_item.done\",\"item\":{\"type\":\"message\",\"role\":\"assistant\",\"content\":[{\"type\":\"output_text\",\"text\":\"Hallo Welt\"}]}}\n\n" +
		"data: {\"type\":\"response.completed\",\"response\":{\"id\":\"r\"}}\n\n" +
		"data: {\"type\":\"response.output_text.delta\",\"delta\":\"ignored\"}\n\n"
	if text, err := parseResponsesStream(strings.NewReader(stream), now); err != nil || text != "Hallo Welt" {
		t.Errorf("deltas: %q, %v", text, err)
	}

	// Without deltas the finished output items are used.
	items := "data: {\"type\":\"response.output_item.done\",\"item\":{\"type\":\"reasoning\"}}\n\n" +
		"data: {\"type\":\"response.output_item.done\",\"item\":{\"type\":\"message\",\"content\":[{\"type\":\"output_text\",\"text\":\"{\\\"a\\\":1}\"}]}}\n\n" +
		"data: {\"type\":\"response.completed\",\"response\":{}}\n\n"
	if text, err := parseResponsesStream(strings.NewReader(items), now); err != nil || text != `{"a":1}` {
		t.Errorf("items: %q, %v", text, err)
	}
	// ... or the output of the completed response.
	final := "data: {\"type\":\"response.completed\",\"response\":{\"output\":[{\"type\":\"message\",\"content\":[{\"type\":\"output_text\",\"text\":\"fertig\"}]}]}}\n\n"
	if text, err := parseResponsesStream(strings.NewReader(final), now); err != nil || text != "fertig" {
		t.Errorf("final: %q, %v", text, err)
	}

	failed := "data: {\"type\":\"response.failed\",\"response\":{\"error\":{\"code\":\"rate_limit_exceeded\",\"message\":\"slow down\"}}}\n\n"
	if _, err := parseResponsesStream(strings.NewReader(failed), now); !errors.Is(err, ErrRateLimited) {
		t.Errorf("failed: %v", err)
	}
	other := "data: {\"type\":\"error\",\"code\":\"invalid_request\",\"message\":\"Modell unbekannt\"}\n\n"
	if _, err := parseResponsesStream(strings.NewReader(other), now); err == nil || !strings.Contains(err.Error(), "Modell unbekannt") {
		t.Errorf("error event: %v", err)
	}
	cut := "data: {\"type\":\"response.output_text.delta\",\"delta\":\"{\\\"a\\\":\"}\n\n"
	if _, err := parseResponsesStream(strings.NewReader(cut), now); err == nil {
		t.Error("stream without response.completed must fail")
	}
	incomplete := "data: {\"type\":\"response.incomplete\",\"response\":{\"incomplete_details\":{\"reason\":\"max_output_tokens\"}}}\n\n"
	if _, err := parseResponsesStream(strings.NewReader(incomplete), now); err == nil || !strings.Contains(err.Error(), "max_output_tokens") {
		t.Errorf("incomplete: %v", err)
	}
}

func TestChatGPTHTTPErrors(t *testing.T) {
	now := time.Now()
	err := chatGPTHTTPError(429, http.Header{"Retry-After": {"30"}}, []byte(`{"error":{"message":"Too many"}}`), now)
	if !errors.Is(err, ErrRateLimited) || !RetryAt(err).Equal(now.Add(30*time.Second)) || !strings.Contains(err.Error(), "Zu viele Anfragen") {
		t.Errorf("429: %v (%v)", err, RetryAt(err))
	}
	err = chatGPTHTTPError(429, nil, []byte(`{"error":{"type":"usage_limit_reached","resets_in_seconds":60}}`), now)
	if !RetryAt(err).Equal(now.Add(time.Minute)) || !strings.Contains(err.Error(), "wieder verfügbar ab") {
		t.Errorf("usage limit: %v", err)
	}
	err = chatGPTHTTPError(400, nil, []byte(`{"detail":"The 'x' model is not supported when using Codex with a ChatGPT account."}`), now)
	if errors.Is(err, ErrRateLimited) || !strings.Contains(err.Error(), "not supported") {
		t.Errorf("400: %v", err)
	}
	err = chatGPTHTTPError(403, nil, []byte(`<html>cloudflare</html>`), now)
	if strings.Contains(err.Error(), "html") {
		t.Errorf("non-JSON bodies must not be echoed: %v", err)
	}
	// Other providers: HTTP 429 also counts as rate limit.
	if !errors.Is(&apiError{Status: 429, Msg: "x"}, ErrRateLimited) {
		t.Error("apiError 429 must match ErrRateLimited")
	}
}

func TestJWTClaims(t *testing.T) {
	c, ok := jwtClaims(idToken("a@b.test", "pro", "acc"))
	if !ok || c.Email != "a@b.test" || c.Auth.PlanType != "pro" || c.Auth.AccountID != "acc" {
		t.Fatalf("claims = %+v %v", c, ok)
	}
	if _, ok := jwtClaims("no-jwt"); ok {
		t.Error("garbage accepted")
	}
	a := &store.ChatGPTAuth{}
	applyTokens(a, tokenResponse{AccessToken: "opaque", RefreshToken: "r", IDToken: idToken("a@b.test", "team", "acc"), ExpiresIn: 3600}, time.Now())
	if a.Plan != "Team" || a.AccountID != "acc" || time.Until(a.ExpiresAt) < 59*time.Minute {
		t.Errorf("auth = %+v", a)
	}
}
