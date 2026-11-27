package ai

import (
	"context"
	"net/http"
	"testing"
)

func TestWorkerStatusReportsFailuresAndResetsRetries(t *testing.T) {
	srv := aiErrorEndpoint(t, http.StatusBadRequest)
	st, w, m := setup(t, srv.URL)
	disableEmbeddings(t, w)
	ctx := context.Background()

	w.process(ctx)

	s := w.Status()
	if s.Failed != 1 || s.LastError == "" || s.LastErrorAt == nil {
		t.Fatalf("status = %+v, want one recorded failure", s)
	}
	q, err := st.AIQueueStats(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if q.Due != 0 || q.Waiting != 1 {
		t.Fatalf("queue = %+v, want 0 due and 1 waiting", q)
	}
	fails, err := st.AIFailures(ctx, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(fails) != 1 || fails[0].ID != m.ID || fails[0].Attempts != 1 || fails[0].Account != "a@example.test" {
		t.Fatalf("failures = %+v", fails)
	}

	n, err := st.ResetAIRetries(ctx)
	if err != nil || n != 1 {
		t.Fatalf("reset = %d, %v; want 1", n, err)
	}
	if q, _ := st.AIQueueStats(ctx); q.Due != 1 || q.Waiting != 0 {
		t.Fatalf("queue after reset = %+v, want 1 due", q)
	}
}

func TestWorkerStatusCountsWhyMessagesStay(t *testing.T) {
	// Category without a target folder: analyzed, but stays in the inbox.
	srv := fakeEndpoint(t, `{"category":"Privat","priority":"normal","spam_probability":0,"phishing":false,"reasons":[],"summary":"x","tags":[],"events":[],"tasks":[]}`)
	t.Cleanup(srv.Close)
	st, w, _ := setup(t, srv.URL)
	disableEmbeddings(t, w)
	ctx := context.Background()
	cfg := w.Client.Settings.Get()
	cfg.AI.Categories = []string{"Rechnung", "Privat"}
	cfg.AI.AutoFile.Enabled = true
	cfg.AI.AutoFile.Mode = "categories"
	cfg.AI.AutoFile.CategoryFolders = map[string]string{"Rechnung": "Rechnungen"}
	if err := w.Client.Settings.Save(ctx, cfg); err != nil {
		t.Fatal(err)
	}

	w.process(ctx)

	s := w.Status()
	if s.Analyzed != 1 || s.Filed != 0 || s.Kept[KeepNoTarget] != 1 {
		t.Fatalf("status = %+v, want one message kept for lack of a target folder", s)
	}
	if q, _ := st.AIQueueStats(ctx); q.Analyzed != 1 || q.Due != 0 {
		t.Fatalf("queue = %+v", q)
	}
}

