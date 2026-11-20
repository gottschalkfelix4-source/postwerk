package ai

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// fakeAnthropic mimics the Messages API; embeddings come from a separate
// OpenAI-compatible endpoint (embedSrv).
func fakeAnthropic(t *testing.T, reply string) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("x-api-key") != "sk-ant-test" || r.Header.Get("anthropic-version") == "" {
			w.WriteHeader(http.StatusUnauthorized)
			json.NewEncoder(w).Encode(map[string]any{"type": "error", "error": map[string]string{"type": "authentication_error", "message": "invalid x-api-key"}})
			return
		}
		if r.Header.Get("Authorization") != "" {
			t.Errorf("anthropic request must not send Authorization")
		}
		switch r.URL.Path {
		case "/v1/messages":
			var req map[string]any
			json.NewDecoder(r.Body).Decode(&req)
			if req["model"] != "claude-test" {
				t.Errorf("model = %v", req["model"])
			}
			if _, ok := req["temperature"]; ok {
				t.Errorf("temperature must not be sent")
			}
			if req["max_tokens"] == nil {
				t.Errorf("max_tokens missing")
			}
			if sys, _ := req["system"].(string); !strings.Contains(sys, "JSON") {
				t.Errorf("system prompt should demand JSON: %q", sys)
			}
			msgs, _ := req["messages"].([]any)
			if len(msgs) != 1 || msgs[0].(map[string]any)["role"] != "user" {
				t.Errorf("messages = %v", msgs)
			}
			json.NewEncoder(w).Encode(map[string]any{
				"content":     []any{map[string]string{"type": "thinking", "thinking": ""}, map[string]string{"type": "text", "text": reply}},
				"stop_reason": "end_turn",
			})
		case "/v1/models":
			json.NewEncoder(w).Encode(map[string]any{"data": []any{map[string]string{"id": "claude-test"}}})
		default:
			http.NotFound(w, r)
		}
	}))
}

func TestAnthropicProvider(t *testing.T) {
	reply := `{"category":"Rechnung","priority":"high","spam_probability":0.01,"phishing":false,"reasons":[],"summary":"Nebenkosten","tags":[],"events":[],"tasks":[]}`
	srv := fakeAnthropic(t, reply)
	defer srv.Close()
	embed := fakeEndpoint(t, "")
	defer embed.Close()

	st, w, m := setup(t, srv.URL)
	ctx := context.Background()
	cfg := w.Client.Settings.Get()
	cfg.AI.Provider = ProviderAnthropic
	cfg.AI.BaseURL = srv.URL // without /v1 – must still work
	cfg.AI.APIKey = "sk-ant-test"
	cfg.AI.ChatModel = "claude-test"
	cfg.AI.EmbeddingModel = "test-embed"
	cfg.AI.Categories = []string{"Rechnung"}
	w.Client.Settings.Save(ctx, cfg)

	// Without a separate embedding endpoint semantic search is off.
	if w.Client.CanEmbed() {
		t.Fatal("anthropic provider has no embeddings of its own")
	}
	models, err := w.Client.Models(ctx)
	if err != nil || len(models) != 1 || models[0] != "claude-test" {
		t.Fatalf("models = %v, %v", models, err)
	}

	cfg.AI.EmbeddingBaseURL = embed.URL + "/v1"
	w.Client.Settings.Save(ctx, cfg)
	if !w.Client.CanEmbed() {
		t.Fatal("embedding endpoint configured")
	}
	w.process(ctx)
	got, _ := st.GetMessageAny(ctx, m.ID)
	if got.Category == nil || *got.Category != "Rechnung" || got.Priority != "high" {
		t.Fatalf("analysis not stored: %+v", got)
	}
	hits, _ := st.SimilarMessages(ctx, m.AccountID, []float32{0.1, 0.2, 0.3}, 5)
	if len(hits) != 1 {
		t.Errorf("embedding via separate endpoint missing: %+v", hits)
	}

	// Wrong key: the API's error message is surfaced.
	cfg.AI.APIKey = "wrong"
	w.Client.Settings.Save(ctx, cfg)
	if _, err := w.Client.TestConnection(ctx); err == nil || !strings.Contains(err.Error(), "invalid x-api-key") {
		t.Errorf("expected auth error, got %v", err)
	}
}

func TestAnthropicURL(t *testing.T) {
	for in, want := range map[string]string{
		"https://api.anthropic.com":      "https://api.anthropic.com/v1/messages",
		"https://api.anthropic.com/":     "https://api.anthropic.com/v1/messages",
		"https://api.anthropic.com/v1":   "https://api.anthropic.com/v1/messages",
		"https://example.test/anthropic": "https://example.test/anthropic/v1/messages",
	} {
		if got := anthropicURL(in, "/messages"); got != want {
			t.Errorf("%s → %s, want %s", in, got, want)
		}
	}
}

// OpenAI-hosted reasoning models reject custom temperature: retried without it.
func TestOpenAITemperatureRetry(t *testing.T) {
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req map[string]any
		json.NewDecoder(r.Body).Decode(&req)
		calls++
		if _, ok := req["temperature"]; ok {
			w.WriteHeader(http.StatusBadRequest)
			json.NewEncoder(w).Encode(map[string]any{"error": map[string]string{"message": "Unsupported value: 'temperature' does not support 0.2"}})
			return
		}
		json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"message": map[string]string{"role": "assistant", "content": "OK"}}}})
	}))
	defer srv.Close()
	_, w, _ := setup(t, srv.URL)
	got, err := w.Client.Chat(context.Background(), "s", "u", false)
	if err != nil || got != "OK" || calls != 2 {
		t.Fatalf("got %q err=%v calls=%d", got, err, calls)
	}
}
