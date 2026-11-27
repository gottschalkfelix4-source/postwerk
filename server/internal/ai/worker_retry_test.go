package ai

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync"
	"testing"
	"time"

	"postwerk/internal/store"
)

const retryTestReply = `{"category":"Rechnung","priority":"normal","spam_probability":0.01,"phishing":false,"reasons":[],"summary":"Test","tags":[],"events":[],"tasks":[]}`

func writeChatJSON(w http.ResponseWriter, content string) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"choices": []any{map[string]any{"message": map[string]string{"role": "assistant", "content": content}}},
	})
}

// aiErrorEndpoint answers every request with the given HTTP status.
func aiErrorEndpoint(t *testing.T, status int) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(status)
		_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]string{"message": "provider failure"}})
	}))
	t.Cleanup(srv.Close)
	return srv
}

// disableEmbeddings keeps worker tests focused on the chat path; the fake
// endpoints below do not implement /v1/embeddings.
func disableEmbeddings(t *testing.T, w *Worker) {
	t.Helper()
	cfg := w.Client.Settings.Get()
	cfg.AI.Features.SemanticSearch = false
	if err := w.Client.Settings.Save(context.Background(), cfg); err != nil {
		t.Fatal(err)
	}
}

func mustAIState(t *testing.T, st *store.Store, id int64) store.AIState {
	t.Helper()
	state, err := st.AIMessageState(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	return state
}

func TestWorkerPausesOnTemporaryProviderErrors(t *testing.T) {
	for _, status := range []int{500, 502, 503, 504, 408} {
		t.Run(strconv.Itoa(status), func(t *testing.T) {
			srv := aiErrorEndpoint(t, status)
			st, w, m := setup(t, srv.URL)
			w.process(context.Background())

			if w.PausedUntil().IsZero() {
				t.Fatal("worker did not pause on a temporary provider error")
			}
			state := mustAIState(t, st, m.ID)
			if state.Attempts != 0 {
				t.Fatalf("attempts = %d, want 0 (temporary errors must not consume messages)", state.Attempts)
			}
			if state.NextAttemptAt != nil {
				t.Fatalf("next attempt = %v, want nil", state.NextAttemptAt)
			}
			got, err := st.GetMessageAny(context.Background(), m.ID)
			if err != nil {
				t.Fatal(err)
			}
			if got.AIDone {
				t.Fatal("message was marked as analyzed after a provider error")
			}
		})
	}
}

func TestWorkerPausesOnConfigErrorsWithoutConsumingMessages(t *testing.T) {
	for _, status := range []int{401, 403, 404} {
		t.Run(strconv.Itoa(status), func(t *testing.T) {
			srv := aiErrorEndpoint(t, status)
			st, w, m := setup(t, srv.URL)
			ctx := context.Background()

			// Three separate runs must neither consume attempts nor mark the
			// message as analyzed. Resume() simulates a settings change.
			for i := 0; i < 3; i++ {
				w.Resume()
				w.process(ctx)
				if w.PausedUntil().IsZero() {
					t.Fatalf("run %d: worker did not pause on a configuration error", i+1)
				}
			}
			state := mustAIState(t, st, m.ID)
			if state.Attempts != 0 {
				t.Fatalf("attempts = %d, want 0 (configuration errors must not consume messages)", state.Attempts)
			}
			if state.NextAttemptAt != nil {
				t.Fatalf("next attempt = %v, want nil", state.NextAttemptAt)
			}
			got, err := st.GetMessageAny(ctx, m.ID)
			if err != nil {
				t.Fatal(err)
			}
			if got.AIDone {
				t.Fatal("message was marked as analyzed after a configuration error")
			}
		})
	}
}

func TestWorkerRetriesBadRequestsAsMessageFailures(t *testing.T) {
	for _, status := range []int{400, 422} {
		t.Run(strconv.Itoa(status), func(t *testing.T) {
			srv := aiErrorEndpoint(t, status)
			st, w, m := setup(t, srv.URL)
			disableEmbeddings(t, w)
			w.process(context.Background())

			// A request-specific 4xx must not block the queue: the message gets
			// the persistent per-message backoff and the worker keeps going.
			if until := w.PausedUntil(); !until.IsZero() {
				t.Fatalf("worker paused until %v, want no global pause", until)
			}
			state := mustAIState(t, st, m.ID)
			if state.Attempts != 1 {
				t.Fatalf("attempts = %d, want 1", state.Attempts)
			}
			if state.NextAttemptAt == nil || !state.NextAttemptAt.After(time.Now()) {
				t.Fatalf("next attempt = %v, want a future time", state.NextAttemptAt)
			}
		})
	}
}

func TestWorkerTreatsTimeoutAsTemporary(t *testing.T) {
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-release:
		case <-time.After(5 * time.Second):
		}
	}))
	t.Cleanup(func() {
		close(release)
		srv.Close()
	})

	st, w, m := setup(t, srv.URL)
	cfg := w.Client.Settings.Get()
	cfg.AI.TimeoutSeconds = 1
	if err := w.Client.Settings.Save(context.Background(), cfg); err != nil {
		t.Fatal(err)
	}

	start := time.Now()
	w.process(context.Background())
	if elapsed := time.Since(start); elapsed > 4*time.Second {
		t.Fatalf("process took %v, timeout was not applied", elapsed)
	}
	if w.PausedUntil().IsZero() {
		t.Fatal("worker did not pause on a timeout")
	}
	state := mustAIState(t, st, m.ID)
	if state.Attempts != 0 {
		t.Fatalf("attempts = %d, want 0 (timeouts must not consume messages)", state.Attempts)
	}
	if state.NextAttemptAt != nil {
		t.Fatalf("next attempt = %v, want nil", state.NextAttemptAt)
	}
}

func TestWorkerRetriesMalformedJSONWithoutPermanentSkip(t *testing.T) {
	var mu sync.Mutex
	reply := "this is not json"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		current := reply
		mu.Unlock()
		writeChatJSON(w, current)
	}))
	t.Cleanup(srv.Close)

	st, w, m := setup(t, srv.URL)
	disableEmbeddings(t, w)
	ctx := context.Background()

	w.process(ctx)
	state := mustAIState(t, st, m.ID)
	if state.Attempts != 1 {
		t.Fatalf("attempts = %d, want 1", state.Attempts)
	}
	if state.NextAttemptAt == nil || !state.NextAttemptAt.After(time.Now()) {
		t.Fatalf("next attempt = %v, want a future time", state.NextAttemptAt)
	}
	if state.LastError == "" {
		t.Fatal("last error was not persisted")
	}

	// The persisted backoff keeps the message out of the next batch, so a
	// restart or a new mail does not hammer the endpoint.
	w.process(ctx)
	if state = mustAIState(t, st, m.ID); state.Attempts != 1 {
		t.Fatalf("attempts = %d after second run, want 1", state.Attempts)
	}

	// Let the backoff expire and answer correctly: the message must still be
	// retried (the old implementation skipped it forever after three errors).
	if _, err := st.DB.ExecContext(ctx, `UPDATE messages SET ai_next_attempt_at = 0 WHERE id = ?`, m.ID); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	reply = retryTestReply
	mu.Unlock()
	w.process(ctx)

	got, err := st.GetMessageAny(ctx, m.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !got.AIDone {
		t.Fatal("message was not analyzed after a successful retry")
	}
	state = mustAIState(t, st, m.ID)
	if state.Attempts != 0 || state.NextAttemptAt != nil || state.LastError != "" {
		t.Fatalf("retry state not reset after success: %+v", state)
	}
}

func TestPendingAIMessagesHonorsPersistedBackoff(t *testing.T) {
	srv := aiErrorEndpoint(t, 500)
	st, _, m := setup(t, srv.URL)
	ctx := context.Background()

	if err := st.SetAIFailure(ctx, m.ID, 5, "boom", time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	ids, err := st.PendingAIMessages(ctx, 20)
	if err != nil {
		t.Fatal(err)
	}
	if len(ids) != 0 {
		t.Fatalf("pending = %v, want none while the backoff is active", ids)
	}

	if err := st.SetAIFailure(ctx, m.ID, 5, "boom", time.Now().Add(-time.Minute)); err != nil {
		t.Fatal(err)
	}
	ids, err = st.PendingAIMessages(ctx, 20)
	if err != nil {
		t.Fatal(err)
	}
	if len(ids) != 1 || ids[0] != m.ID {
		t.Fatalf("pending = %v, want [%d] after the backoff expired", ids, m.ID)
	}
}

func TestClientClassifiesHTTPFailures(t *testing.T) {
	cases := []struct {
		status int
		target error
		want   bool
	}{
		{500, ErrUnavailable, true},
		{502, ErrUnavailable, true},
		{503, ErrUnavailable, true},
		{504, ErrUnavailable, true},
		{408, ErrUnavailable, true},
		{429, ErrRateLimited, true},
		{401, ErrConfig, true},
		{403, ErrConfig, true},
		{404, ErrConfig, true},
		{400, ErrConfig, false},
		{422, ErrConfig, false},
		{500, ErrRateLimited, false},
		{401, ErrUnavailable, false},
		{404, ErrUnavailable, false},
		{400, ErrRateLimited, false},
	}
	for _, c := range cases {
		err := &apiError{Status: c.status, Msg: "test"}
		if got := errors.Is(err, c.target); got != c.want {
			t.Errorf("status %d: errors.Is(err, %v) = %v, want %v", c.status, c.target, got, c.want)
		}
	}
}

func TestDoClassifiesTimeoutAsTemporary(t *testing.T) {
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-release:
		case <-time.After(5 * time.Second):
		}
	}))
	t.Cleanup(func() {
		close(release)
		srv.Close()
	})

	c := &Client{HTTP: srv.Client()}
	err := c.do(context.Background(), store.AISettings{TimeoutSeconds: 1}, http.MethodPost, srv.URL, nil, nil, &struct{}{})
	if !errors.Is(err, ErrUnavailable) {
		t.Fatalf("error = %v, want ErrUnavailable", err)
	}
}

func TestDoHonorsRetryAfter(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Retry-After", "2")
		w.WriteHeader(http.StatusTooManyRequests)
		_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]string{"message": "slow down"}})
	}))
	t.Cleanup(srv.Close)

	c := &Client{HTTP: srv.Client()}
	err := c.do(context.Background(), store.AISettings{TimeoutSeconds: 5}, http.MethodPost, srv.URL, nil, nil, &struct{}{})
	if !errors.Is(err, ErrRateLimited) {
		t.Fatalf("error = %v, want ErrRateLimited", err)
	}
	at := RetryAt(err)
	if at.IsZero() {
		t.Fatal("RetryAt returned no time for a Retry-After header")
	}
	if d := time.Until(at); d < time.Second || d > 3*time.Second {
		t.Fatalf("retry after %v, want about 2s", d)
	}
}

func TestMessageRetryDelay(t *testing.T) {
	cases := []struct {
		attempts int
		want     time.Duration
	}{
		{0, 30 * time.Second},
		{1, 30 * time.Second},
		{2, time.Minute},
		{3, 2 * time.Minute},
		{4, 4 * time.Minute},
		{100, 6 * time.Hour},
	}
	for _, c := range cases {
		if got := messageRetryDelay(c.attempts); got != c.want {
			t.Errorf("messageRetryDelay(%d) = %v, want %v", c.attempts, got, c.want)
		}
	}
}
