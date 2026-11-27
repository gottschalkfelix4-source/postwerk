package ai

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"postwerk/internal/message"
	"postwerk/internal/store"
)

func TestDecodeLooseJSONRepairsModelOutput(t *testing.T) {
	cases := []struct {
		name    string
		in      string
		wantErr bool
		check   func(t *testing.T, a *Analysis)
	}{
		{
			name: "fenced with prose",
			in:   "Hier ist das Ergebnis:\n```json\n{\"summary\":\"Hallo\",\"tags\":[\"a\"]}\n```",
			check: func(t *testing.T, a *Analysis) {
				if a.Summary != "Hallo" || len(a.Tags) != 1 || a.Tags[0] != "a" {
					t.Fatalf("analysis = %+v", a)
				}
			},
		},
		{
			name: "trailing comma",
			in:   `{"summary":"Hallo",}`,
			check: func(t *testing.T, a *Analysis) {
				if a.Summary != "Hallo" {
					t.Fatalf("summary = %q", a.Summary)
				}
			},
		},
		{
			name: "quoted number and bool",
			in:   `{"summary":"Hallo","spam_probability":"0.9","phishing":"true"}`,
			check: func(t *testing.T, a *Analysis) {
				if a.SpamProbability != 0.9 || !a.Phishing {
					t.Fatalf("analysis = %+v", a)
				}
			},
		},
		{
			name: "german decimal comma",
			in:   `{"summary":"Hallo","spam_probability":"0,8"}`,
			check: func(t *testing.T, a *Analysis) {
				if a.SpamProbability != 0.8 {
					t.Fatalf("spam = %v", a.SpamProbability)
				}
			},
		},
		{
			name: "python literals",
			in:   `{"summary":None,"phishing":True,"suspicious":False}`,
			check: func(t *testing.T, a *Analysis) {
				if a.Summary != "" || !a.Phishing || a.Suspicious {
					t.Fatalf("analysis = %+v", a)
				}
			},
		},
		{
			name: "comments",
			in:   "{\"summary\":\"Hallo\" /* note */, \"tags\":[\"a\"] // rest\n}",
			check: func(t *testing.T, a *Analysis) {
				if a.Summary != "Hallo" || len(a.Tags) != 1 {
					t.Fatalf("analysis = %+v", a)
				}
			},
		},
		{
			name: "array aside before object",
			in:   `Hinweis [1, 2] und dann {"summary":"Hallo"}`,
			check: func(t *testing.T, a *Analysis) {
				if a.Summary != "Hallo" {
					t.Fatalf("summary = %q", a.Summary)
				}
			},
		},
		{
			name: "array wrapper",
			in:   `[{"summary":"Hallo"}]`,
			check: func(t *testing.T, a *Analysis) {
				if a.Summary != "Hallo" || !a.Present["summary"] {
					t.Fatalf("analysis = %+v, present = %v", a, a.Present)
				}
			},
		},
		{
			name: "single event object",
			in:   `{"summary":"Hallo","events":{"title":"Termin","start":"2026-12-01T10:00:00"},"tasks":[]}`,
			check: func(t *testing.T, a *Analysis) {
				if len(a.Events) != 1 || a.Events[0].Title != "Termin" {
					t.Fatalf("events = %+v", a.Events)
				}
			},
		},
		{
			name: "german yes",
			in:   `{"summary":"Hallo","phishing":"ja"}`,
			check: func(t *testing.T, a *Analysis) {
				if !a.Phishing {
					t.Fatalf("phishing = false, want true")
				}
			},
		},
		{
			name: "string content is preserved",
			in:   `{"summary":"Text mit \"events\": { und ,} und None"}`,
			check: func(t *testing.T, a *Analysis) {
				want := `Text mit "events": { und ,} und None`
				if a.Summary != want {
					t.Fatalf("summary = %q, want %q", a.Summary, want)
				}
			},
		},
		{
			name: "empty object",
			in:   `{}`,
			check: func(t *testing.T, a *Analysis) {
				if len(a.Present) != 0 {
					t.Fatalf("present = %v, want none", a.Present)
				}
			},
		},
		{
			name: "null",
			in:   `null`,
			check: func(t *testing.T, a *Analysis) {
				if len(a.Present) != 0 {
					t.Fatalf("present = %v, want none", a.Present)
				}
			},
		},
		{
			name:    "array of two objects is not truncated",
			in:      `[{"summary":"A"},{"summary":"B"}]`,
			wantErr: true,
		},
		{
			name:    "truncated",
			in:      `{"summary":"Hallo"`,
			wantErr: true,
		},
		{
			name:    "not json",
			in:      `Das ist kein JSON`,
			wantErr: true,
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var a Analysis
			err := decodeLooseJSON(c.in, &a)
			if c.wantErr {
				if err == nil {
					t.Fatalf("decodeLooseJSON(%q) succeeded, want an error", c.in)
				}
				return
			}
			if err != nil {
				t.Fatalf("decodeLooseJSON(%q): %v", c.in, err)
			}
			if c.check != nil {
				c.check(t, &a)
			}
		})
	}
}

func TestAnalysisHasContent(t *testing.T) {
	all := store.AIFeatures{Classify: true, Priority: true, SpamCheck: true, Summarize: true, ExtractEvents: true}
	cases := []struct {
		name    string
		a       Analysis
		f       store.AIFeatures
		folders []string
		want    bool
	}{
		{"empty", Analysis{Present: map[string]bool{}}, all, nil, false},
		{"category null is an answer", Analysis{Present: map[string]bool{"category": true}}, all, nil, true},
		{"priority", Analysis{Priority: "normal", Present: map[string]bool{"priority": true}}, all, nil, true},
		{"empty summary only", Analysis{Present: map[string]bool{"summary": true}}, all, nil, false},
		{"summary", Analysis{Summary: "Kurz", Present: map[string]bool{"summary": true}}, all, nil, true},
		{"empty event lists are an answer", Analysis{Present: map[string]bool{"events": true}}, all, nil, true},
		{"unrequested field does not count", Analysis{Summary: "Kurz", Present: map[string]bool{"summary": true}}, store.AIFeatures{Classify: true}, nil, false},
		{"spam zero is an answer", Analysis{Present: map[string]bool{"spam_probability": true}}, store.AIFeatures{SpamCheck: true}, nil, true},
		{"reasons alone are not an assessment", Analysis{Present: map[string]bool{"reasons": true}}, store.AIFeatures{SpamCheck: true}, nil, false},
		{"folder without choices does not count", Analysis{Present: map[string]bool{"folder": true}}, all, nil, false},
		{"folder with choices", Analysis{Present: map[string]bool{"folder": true}}, all, []string{"Rechnungen"}, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := analysisHasContent(&c.a, c.f, c.folders); got != c.want {
				t.Fatalf("analysisHasContent(%+v) = %v, want %v", c.a, got, c.want)
			}
		})
	}
}

// countingChatServer answers chat requests from replies in order and records
// the user prompt of every call.
func countingChatServer(t *testing.T, replies ...string) (*httptest.Server, func() (int, []string)) {
	t.Helper()
	var mu sync.Mutex
	calls := 0
	var prompts []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Messages []struct {
				Role    string `json:"role"`
				Content string `json:"content"`
			} `json:"messages"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		mu.Lock()
		n := calls
		calls++
		if len(req.Messages) > 0 {
			prompts = append(prompts, req.Messages[len(req.Messages)-1].Content)
		}
		mu.Unlock()
		if n < len(replies) {
			writeChatJSON(w, replies[n])
			return
		}
		writeChatJSON(w, replies[len(replies)-1])
	}))
	t.Cleanup(srv.Close)
	return srv, func() (int, []string) {
		mu.Lock()
		defer mu.Unlock()
		return calls, append([]string{}, prompts...)
	}
}

func TestAnalyzeRepairsMalformedAnswer(t *testing.T) {
	srv, stats := countingChatServer(t, `{"summary":`, retryTestReply)
	st, w, m := setup(t, srv.URL)
	disableEmbeddings(t, w)

	w.process(context.Background())

	calls, prompts := stats()
	if calls != 2 {
		t.Fatalf("calls = %d, want 2 (initial answer + one repair)", calls)
	}
	if len(prompts) != 2 || !strings.Contains(prompts[1], "vorherige Antwort") {
		t.Fatalf("repair prompt missing: %q", prompts)
	}
	got, err := st.GetMessageAny(context.Background(), m.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !got.AIDone || got.Summary == nil || *got.Summary != "Test" {
		t.Fatalf("message not analyzed after repair: %+v", got)
	}
	if state := mustAIState(t, st, m.ID); state.Attempts != 0 {
		t.Fatalf("attempts = %d, want 0 after a successful repair", state.Attempts)
	}
}

func TestAnalyzeRepairsEmptyResult(t *testing.T) {
	srv, stats := countingChatServer(t, `{}`, retryTestReply)
	st, w, m := setup(t, srv.URL)
	disableEmbeddings(t, w)

	w.process(context.Background())

	if calls, _ := stats(); calls != 2 {
		t.Fatalf("calls = %d, want 2 (empty answer + one repair)", calls)
	}
	got, err := st.GetMessageAny(context.Background(), m.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !got.AIDone || got.Category == nil {
		t.Fatalf("message not analyzed after repairing an empty answer: %+v", got)
	}
	if state := mustAIState(t, st, m.ID); state.Attempts != 0 {
		t.Fatalf("attempts = %d, want 0", state.Attempts)
	}
}

func TestAnalyzeEmptyResultAfterRepairStillBacksOff(t *testing.T) {
	srv, stats := countingChatServer(t, `{}`)
	st, w, m := setup(t, srv.URL)
	disableEmbeddings(t, w)

	w.process(context.Background())

	if calls, _ := stats(); calls != 2 {
		t.Fatalf("calls = %d, want 2 (initial + one repair, no endless retry)", calls)
	}
	state := mustAIState(t, st, m.ID)
	if state.Attempts != 1 {
		t.Fatalf("attempts = %d, want 1", state.Attempts)
	}
	if state.NextAttemptAt == nil {
		t.Fatal("empty result did not schedule a retry")
	}
	if !strings.Contains(state.LastError, "verwertbaren") && !strings.Contains(state.LastError, "unusable") {
		t.Fatalf("last error = %q", state.LastError)
	}
	got, err := st.GetMessageAny(context.Background(), m.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.AIDone {
		t.Fatal("empty result was stored as a successful analysis")
	}
}

func TestSummarizeRepairsEmptyAnswer(t *testing.T) {
	srv, stats := countingChatServer(t, `{"summary":"","bullets":[]}`, `{"summary":"Kurz","bullets":["Punkt"]}`)
	_, w, m := setup(t, srv.URL)

	s, err := w.Client.Summarize(context.Background(), []*store.Message{m}, []*message.Parsed{{}})
	if err != nil {
		t.Fatal(err)
	}
	if s.Summary != "Kurz" || len(s.Bullets) != 1 {
		t.Fatalf("summary = %+v", s)
	}
	if calls, _ := stats(); calls != 2 {
		t.Fatalf("calls = %d, want 2", calls)
	}
}

func TestExtractEmptyListsIsValid(t *testing.T) {
	srv, stats := countingChatServer(t, `{"events":[],"tasks":[],"suspicious":false}`)
	_, w, m := setup(t, srv.URL)

	e, err := w.Client.Extract(context.Background(), m, &message.Parsed{})
	if err != nil {
		t.Fatal(err)
	}
	if len(e.Events) != 0 || len(e.Tasks) != 0 {
		t.Fatalf("extraction = %+v", e)
	}
	if calls, _ := stats(); calls != 1 {
		t.Fatalf("calls = %d, want 1 (empty lists need no repair)", calls)
	}
}

func TestChatJSONDoesNotRepairProviderErrors(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusInternalServerError)
		_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]string{"message": "down"}})
	}))
	t.Cleanup(srv.Close)

	_, w, _ := setup(t, srv.URL)
	var out Summary
	err := w.Client.ChatJSON(context.Background(), "system", "user", &out)
	if !errors.Is(err, ErrUnavailable) {
		t.Fatalf("error = %v, want ErrUnavailable", err)
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("calls = %d, want 1 (provider errors must not trigger a repair request)", got)
	}
}
