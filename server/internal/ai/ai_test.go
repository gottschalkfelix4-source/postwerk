package ai

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"postwerk/internal/message"
	"postwerk/internal/store"
)

// fakeEndpoint mimics the OpenAI chat/embeddings API.
func fakeEndpoint(t *testing.T, chatReply string) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/chat/completions":
			var req map[string]any
			json.NewDecoder(r.Body).Decode(&req)
			if req["model"] != "test-model" {
				t.Errorf("model = %v", req["model"])
			}
			json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"message": map[string]string{"role": "assistant", "content": chatReply}}}})
		case "/v1/embeddings":
			json.NewEncoder(w).Encode(map[string]any{"data": []any{map[string]any{"embedding": []float32{0.1, 0.2, 0.3}}}})
		case "/v1/models":
			json.NewEncoder(w).Encode(map[string]any{"data": []any{map[string]string{"id": "test-model"}}})
		default:
			http.NotFound(w, r)
		}
	}))
}

func setup(t *testing.T, url string) (*store.Store, *Worker, *store.Message) {
	ctx := context.Background()
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	settings, _ := st.LoadSettings(ctx, "mail.example.test")
	cfg := settings.Get()
	cfg.AI.Enabled = true
	cfg.AI.BaseURL = url + "/v1"
	cfg.AI.ChatModel = "test-model"
	settings.Save(ctx, cfg)
	acct := &store.Account{Email: "a@example.test", PasswordHash: "x", Role: "user"}
	if err := st.CreateAccount(ctx, acct); err != nil {
		t.Fatal(err)
	}
	inbox, _ := st.MailboxByRole(ctx, acct.ID, "inbox")
	raw := message.Build(message.Draft{From: store.Address{Name: "Hausverwaltung", Email: "verwaltung@haus.test"}, To: []store.Address{{Email: acct.Email}},
		Subject: "Nebenkostenabrechnung 2025", Text: "Bitte bis 15.12. 120 EUR überweisen.", MessageID: "n1@haus.test"})
	hash, _ := st.Blobs.Put(raw)
	m := &store.Message{AccountID: acct.ID, MailboxID: inbox, Subject: "Nebenkostenabrechnung 2025", From: store.Address{Email: "verwaltung@haus.test"},
		ReceivedAt: time.Now(), Size: int64(len(raw)), BlobHash: hash}
	if err := st.InsertMessage(ctx, m); err != nil {
		t.Fatal(err)
	}
	w := NewWorker(NewClient(settings), st, slog.New(slog.NewTextHandler(io.Discard, nil)))
	return st, w, m
}

func TestWorkerAnalyzesAndEmbeds(t *testing.T) {
	reply := "```json\n" + `{"category":"rechnung","priority":"high","spam_probability":0.02,"phishing":false,"reasons":[],
		"summary":"Nachzahlung Nebenkosten bis 15.12.","tags":["Nebenkosten"],
		"events":[],"tasks":[{"title":"120 EUR überweisen","due":"2026-12-15"}]}` + "\n```"
	srv := fakeEndpoint(t, reply)
	defer srv.Close()
	st, w, m := setup(t, srv.URL)
	w.Client.Settings.Get()
	cfg := w.Client.Settings.Get()
	cfg.AI.Categories = []string{"Rechnung", "Newsletter"}
	w.Client.Settings.Save(context.Background(), cfg)

	var hooked string
	w.Hooks.OnAnalyzed = func(ctx context.Context, m *store.Message, a *Analysis) { hooked = a.Category }
	w.process(context.Background())

	got, err := st.GetMessageAny(context.Background(), m.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Category == nil || *got.Category != "Rechnung" {
		t.Errorf("category = %v", got.Category)
	}
	if got.Priority != "high" || got.Summary == nil || !strings.Contains(*got.Summary, "Nebenkosten") {
		t.Errorf("priority/summary = %s / %v", got.Priority, got.Summary)
	}
	if len(got.AITasks) != 1 || got.AITasks[0].Title != "120 EUR überweisen" {
		t.Errorf("tasks = %+v", got.AITasks)
	}
	if hooked != "Rechnung" {
		t.Errorf("hook not called with category, got %q", hooked)
	}
	hits, _ := st.SimilarMessages(context.Background(), m.AccountID, []float32{0.1, 0.2, 0.3}, 5)
	if len(hits) != 1 || hits[0].Score < 0.99 {
		t.Errorf("embedding search = %+v", hits)
	}
}

func TestPhishingMovesToJunk(t *testing.T) {
	srv := fakeEndpoint(t, `<think>hmm</think>{"category":null,"priority":"low","spam_probability":0.97,"phishing":true,"reasons":["Gefälschter Absender"],"summary":"x","tags":[],"events":[],"tasks":[]}`)
	defer srv.Close()
	st, w, m := setup(t, srv.URL)
	w.process(context.Background())
	got, _ := st.GetMessageAny(context.Background(), m.ID)
	junk, _ := st.MailboxByRole(context.Background(), m.AccountID, "junk")
	if got.MailboxID != junk {
		t.Fatalf("phishing mail should be in junk, mailbox=%d junk=%d", got.MailboxID, junk)
	}
	if !contains(got.Tags, "Phishing-Verdacht") {
		t.Errorf("tags = %v", got.Tags)
	}
}
