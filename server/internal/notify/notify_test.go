package notify

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"mime"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"postwerk/internal/store"
)

type sentMail struct {
	from  string
	rcpts []string
	raw   string
}

type fakeMailer struct {
	sent []sentMail
	fail bool
}

func (m *fakeMailer) SendSystemMail(ctx context.Context, from string, rcpts []string, raw []byte) error {
	if m.fail {
		return errors.New("kaputt")
	}
	m.sent = append(m.sent, sentMail{from, rcpts, string(raw)})
	return nil
}

func (m *fakeMailer) SystemSender(context.Context) string { return "postmaster@example.test" }

func (m *fakeMailer) subjects() []string {
	var out []string
	for _, s := range m.sent {
		for _, l := range strings.Split(s.raw, "\r\n") {
			if strings.HasPrefix(l, "Subject:") {
				v := strings.TrimSpace(strings.TrimPrefix(l, "Subject:"))
				if d, err := new(mime.WordDecoder).DecodeHeader(v); err == nil {
					v = d
				}
				out = append(out, v)
			}
		}
	}
	return out
}

type fixture struct {
	n        *Service
	mailer   *fakeMailer
	warnings []Warning
	now      time.Time
}

func newFixture(t *testing.T, mutate func(*store.NotificationSettings)) *fixture {
	t.Helper()
	ctx := context.Background()
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	settings, err := st.LoadSettings(ctx, "mail.example.test")
	if err != nil {
		t.Fatal(err)
	}
	if err := st.CreateAccount(ctx, &store.Account{Email: "admin@example.test", Name: "Admin", Role: "admin"}); err != nil {
		t.Fatal(err)
	}
	if err := st.CreateAccount(ctx, &store.Account{Email: "user@example.test", Name: "User", Role: "user"}); err != nil {
		t.Fatal(err)
	}
	s := settings.Get()
	s.Notifications.Enabled = true
	if mutate != nil {
		mutate(&s.Notifications)
	}
	if err := settings.Save(ctx, s); err != nil {
		t.Fatal(err)
	}
	f := &fixture{mailer: &fakeMailer{}, now: time.Date(2026, 11, 21, 10, 0, 0, 0, time.UTC)}
	f.n = &Service{
		Store: st, Settings: settings, Mailer: f.mailer,
		Warnings: func(context.Context) []Warning { return f.warnings },
		Log:      slog.New(slog.NewTextHandler(io.Discard, nil)),
		Now:      func() time.Time { return f.now },
	}
	return f
}

func (f *fixture) eval(t *testing.T) {
	t.Helper()
	if err := f.n.Evaluate(context.Background()); err != nil {
		t.Fatal(err)
	}
}

var certWarning = Warning{ID: "cert", Severity: "warning", Title: "Zertifikat läuft in 5 Tagen ab", Detail: "Gültig bis ...", Link: "/admin/settings/tls"}

func TestDedupeRenotifyAndClear(t *testing.T) {
	f := newFixture(t, nil)
	f.warnings = []Warning{certWarning}
	f.eval(t)
	if s := f.mailer.subjects(); len(s) != 1 || s[0] != "[Postwerk] Warnung: Zertifikat läuft in 5 Tagen ab" {
		t.Fatalf("first mail: %v", s)
	}
	m := f.mailer.sent[0]
	if m.from != "postmaster@example.test" || len(m.rcpts) != 1 || m.rcpts[0] != "admin@example.test" {
		t.Fatalf("default recipients should be the admins: %+v", m)
	}
	if !strings.Contains(m.raw, "Auto-Submitted: auto-generated") {
		t.Fatalf("missing Auto-Submitted header:\n%s", m.raw)
	}

	// Still active: no repeat within 24 hours, also not after a title change.
	f.now = f.now.Add(23 * time.Hour)
	f.warnings[0].Title = "Zertifikat läuft in 4 Tagen ab"
	f.eval(t)
	if n := len(f.mailer.sent); n != 1 {
		t.Fatalf("repeated within 24h: %v", f.mailer.subjects())
	}

	// After 24 hours one reminder, then quiet again.
	f.now = f.now.Add(90 * time.Minute)
	f.eval(t)
	f.eval(t)
	if s := f.mailer.subjects(); len(s) != 2 || s[1] != "[Postwerk] Weiterhin aktiv: Zertifikat läuft in 4 Tagen ab" {
		t.Fatalf("reminder: %v", s)
	}

	// Cleared: one "behoben" mail and the state is gone.
	f.warnings = nil
	f.eval(t)
	f.eval(t)
	if s := f.mailer.subjects(); len(s) != 3 || s[2] != "[Postwerk] Behoben: Zertifikat läuft in 4 Tagen ab" {
		t.Fatalf("resolved: %v", s)
	}
	if st, _ := f.n.Store.ListNotifyState(context.Background()); len(st) != 0 {
		t.Fatalf("state not cleared: %v", st)
	}

	// Coming back is a new problem again.
	f.warnings = []Warning{certWarning}
	f.eval(t)
	if s := f.mailer.subjects(); len(s) != 4 || !strings.HasPrefix(s[3], "[Postwerk] Warnung:") {
		t.Fatalf("recurrence: %v", s)
	}
}

func TestDisabledAndFilters(t *testing.T) {
	f := newFixture(t, func(n *store.NotificationSettings) { n.Enabled = false })
	f.warnings = []Warning{certWarning}
	f.eval(t)
	if len(f.mailer.sent) != 0 {
		t.Fatal("disabled notifications sent a mail")
	}

	f = newFixture(t, func(n *store.NotificationSettings) {
		n.MinSeverity = "error"
		n.Categories = []string{"certificate", "backup"}
		n.Recipients = []string{"Ops@Example.test", "ops@example.test"}
	})
	f.warnings = []Warning{
		certWarning, // below the minimum severity
		{ID: "dnsbl-1.2.3.4-zen.spamhaus.org", Severity: "error", Title: "Sperrliste"}, // category not selected
		{ID: "backup-failed", Severity: "error", Title: "Letzte Sicherung fehlgeschlagen"},
	}
	f.eval(t)
	if s := f.mailer.subjects(); len(s) != 1 || s[0] != "[Postwerk] Fehler: Letzte Sicherung fehlgeschlagen" {
		t.Fatalf("filtered mails: %v", s)
	}
	if r := f.mailer.sent[0].rcpts; len(r) != 1 || r[0] != "ops@example.test" {
		t.Fatalf("configured recipients not used: %v", r)
	}

	// A warning that is still present but no longer selected is forgotten
	// silently, not reported as fixed.
	s := f.n.Settings.Get()
	s.Notifications.Categories = []string{"certificate"}
	_ = f.n.Settings.Save(context.Background(), s)
	f.eval(t)
	if len(f.mailer.sent) != 1 {
		t.Fatalf("deselected warning produced mail: %v", f.mailer.subjects())
	}
	if st, _ := f.n.Store.ListNotifyState(context.Background()); len(st) != 0 {
		t.Fatalf("state kept: %v", st)
	}
}

func TestFailedMailIsRetriedAndEscalation(t *testing.T) {
	f := newFixture(t, nil)
	f.warnings = []Warning{certWarning}
	f.mailer.fail = true
	if err := f.n.Evaluate(context.Background()); err == nil {
		t.Fatal("expected error from failing mailer")
	}
	if st, _ := f.n.Store.ListNotifyState(context.Background()); len(st) != 0 {
		t.Fatal("failed notification must not be recorded")
	}
	f.mailer.fail = false
	f.now = f.now.Add(5 * time.Minute)
	f.eval(t)
	if len(f.mailer.sent) != 1 {
		t.Fatalf("not retried: %v", f.mailer.subjects())
	}
	// Escalation to an error is announced again right away.
	f.warnings[0].Severity = "error"
	f.warnings[0].Title = "Zertifikat ist abgelaufen"
	f.eval(t)
	if s := f.mailer.subjects(); len(s) != 2 || s[1] != "[Postwerk] Fehler: Zertifikat ist abgelaufen" {
		t.Fatalf("escalation: %v", s)
	}
}

func TestWebhookPayload(t *testing.T) {
	var got WebhookPayload
	var auth, ctype string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth, ctype = r.Header.Get("Authorization"), r.Header.Get("Content-Type")
		if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
			t.Error(err)
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()
	f := newFixture(t, func(n *store.NotificationSettings) {
		n.WebhookURL = srv.URL + "/message"
		n.WebhookToken = "geheim"
	})
	s := f.n.Settings.Get()
	s.Web.PublicURL = "https://mail.example.test"
	_ = f.n.Settings.Save(context.Background(), s)

	f.warnings = []Warning{{ID: "backup-failed", Severity: "error", Title: "Letzte Sicherung fehlgeschlagen", Detail: "Ziel nicht erreichbar", Link: "/admin/backup"}}
	f.eval(t)
	if auth != "Bearer geheim" || ctype != "application/json" {
		t.Fatalf("headers: auth=%q type=%q", auth, ctype)
	}
	want := WebhookPayload{Event: EventNew, ID: "backup-failed", Category: "backup", Severity: "error",
		Title: "Fehler: Letzte Sicherung fehlgeschlagen", Message: "Ziel nicht erreichbar", Priority: 8,
		Host: "mail.example.test", Link: "https://mail.example.test/admin/backup",
		Since: "2026-11-21T10:00:00Z", Time: "2026-11-21T10:00:00Z"}
	if got != want {
		t.Fatalf("payload\n got %+v\nwant %+v", got, want)
	}

	f.warnings = nil
	f.now = f.now.Add(time.Hour)
	f.eval(t)
	if got.Event != EventResolved || got.Priority != 3 || got.Title != "Behoben: Letzte Sicherung fehlgeschlagen" {
		t.Fatalf("resolved payload: %+v", got)
	}

	res := f.n.SendTest(context.Background())
	if !res.Webhook || res.WebhookErr != "" || res.MailError != "" || got.Event != EventTest {
		t.Fatalf("test notification: %+v / %+v", res, got)
	}
}

func TestWebhookFailureDoesNotBlockMail(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()
	f := newFixture(t, func(n *store.NotificationSettings) { n.WebhookURL = srv.URL })
	f.warnings = []Warning{certWarning}
	f.eval(t)
	if len(f.mailer.sent) != 1 {
		t.Fatal("mail not sent when the webhook fails")
	}
	res := f.n.SendTest(context.Background())
	if !strings.Contains(res.WebhookErr, "500") {
		t.Fatalf("webhook error not reported: %+v", res)
	}
}

func TestValidWebhookURLAndCategory(t *testing.T) {
	for u, ok := range map[string]bool{
		"https://ntfy.sh/topic": true, "http://gotify.lan/message": true,
		"ftp://x": false, "javascript:alert(1)": false, "https://": false, "https://u:p@host/": false, "/relative": false,
	} {
		if ValidWebhookURL(u) != ok {
			t.Errorf("ValidWebhookURL(%q) != %v", u, ok)
		}
	}
	for id, cat := range map[string]string{"cert": "certificate", "cert-host": "certificate", "backup-old": "backup", "queue": "queue",
		"dnsbl-1.2.3.4-zen": "blacklist", "ptr-1.2.3.4": "dns", "sendlimit-7": "sendlimit", "other": "system"} {
		if Category(id) != cat {
			t.Errorf("Category(%q) = %q, want %q", id, Category(id), cat)
		}
	}
}
