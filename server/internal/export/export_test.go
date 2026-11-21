package export

import (
	"archive/zip"
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"postwerk/internal/store"
)

func setup(t *testing.T) (*store.Store, *store.Account, string) {
	t.Helper()
	dir := t.TempDir()
	st, err := store.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	ctx := context.Background()
	a := &store.Account{Email: "bob@example.test", Name: "Bob", PasswordHash: "$2a$secret-hash", Role: "user"}
	if err := st.CreateAccount(ctx, a); err != nil {
		t.Fatal(err)
	}
	secret := "TOTPSECRETVALUE"
	st.UpdateAccount(ctx, a.ID, store.AccountPatch{TOTPSecret: &secret})
	inbox, _ := st.MailboxByRole(ctx, a.ID, "inbox")
	for i, body := range []string{
		"From: Alice <alice@example.org>\r\nSubject: Eins\r\n\r\nHallo\r\nFrom the start\r\n>From quoted\r\n",
		"From: Carol <carol@example.org>\r\nSubject: Zwei\r\n\r\nZweite Nachricht\r\n",
	} {
		h, _ := st.Blobs.Put([]byte(body))
		m := &store.Message{AccountID: a.ID, MailboxID: inbox, Subject: "s", From: store.Address{Email: []string{"alice@example.org", "carol@example.org"}[i]},
			ReceivedAt: time.Date(2026, 11, 2, 10, 0, 0, 0, time.UTC), Size: int64(len(body)), BlobHash: h}
		if err := st.InsertMessage(ctx, m); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := st.CreateMailbox(ctx, a.ID, "Projekte/2026"); err != nil {
		t.Fatal(err)
	}
	loc := "Büro"
	if err := st.CreateEvent(ctx, a.ID, &store.CalendarEvent{Title: "Besprechung, wichtig", Start: "2026-11-25T10:00", Location: &loc}); err != nil {
		t.Fatal(err)
	}
	due := "2026-11-30"
	if err := st.CreateTask(ctx, a.ID, &store.Task{Title: "Steuer", Due: &due}); err != nil {
		t.Fatal(err)
	}
	st.CreateAppPassword(ctx, a.ID, "Thunderbird", "app-hash-value")
	return st, a, dir
}

func unzip(t *testing.T, data []byte) map[string]string {
	t.Helper()
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]string{}
	for _, f := range zr.File {
		rc, _ := f.Open()
		b, _ := io.ReadAll(rc)
		rc.Close()
		out[f.Name] = string(b)
	}
	return out
}

func TestBuild(t *testing.T) {
	st, a, _ := setup(t)
	var buf bytes.Buffer
	var phases []string
	if err := Build(context.Background(), st, a.ID, &buf, func(p string, done, total int64) { phases = append(phases, p) }); err != nil {
		t.Fatal(err)
	}
	files := unzip(t, buf.Bytes())
	for _, name := range []string{"konto.json", "app-passwoerter.json", "sitzungen.json", "mail/Posteingang.mbox", "mail/Projekte_2026.mbox", "mail/ordner.json", "kalender.ics", "LIESMICH.txt"} {
		if _, ok := files[name]; !ok {
			t.Errorf("missing %s (have %v)", name, keys(files))
		}
	}
	all := strings.Join(values(files), "\n")
	for _, secret := range []string{"TOTPSECRETVALUE", "secret-hash", "app-hash-value", "enc:v1:"} {
		if strings.Contains(all, secret) {
			t.Errorf("export contains secret %q", secret)
		}
	}
	if !strings.Contains(files["konto.json"], `"email": "bob@example.test"`) || !strings.Contains(files["app-passwoerter.json"], "Thunderbird") {
		t.Fatal("account data missing")
	}
	mbox := files["mail/Posteingang.mbox"]
	if strings.Count(mbox, "\nFrom ")+boolInt(strings.HasPrefix(mbox, "From ")) != 2 {
		t.Fatalf("mbox separators:\n%s", mbox)
	}
	if !strings.Contains(mbox, "\n>From the start\n") || !strings.Contains(mbox, "\n>>From quoted\n") || strings.Contains(mbox, "\r") {
		t.Fatalf("mboxrd quoting:\n%s", mbox)
	}
	if !strings.HasPrefix(mbox, "From alice@example.org Mon Nov  2 10:00:00 2026\n") {
		t.Fatalf("from line: %q", strings.SplitN(mbox, "\n", 2)[0])
	}
	ics := files["kalender.ics"]
	for _, want := range []string{"BEGIN:VEVENT", "DTSTART:20261125T100000", "SUMMARY:Besprechung\\, wichtig", "LOCATION:Büro",
		"BEGIN:VTODO", "DUE;VALUE=DATE:20261130", "STATUS:NEEDS-ACTION"} {
		if !strings.Contains(ics, want) {
			t.Errorf("ics missing %q:\n%s", want, ics)
		}
	}
	if len(phases) == 0 || phases[len(phases)-1] != "Abschluss" {
		t.Fatalf("phases = %v", phases)
	}
}

func TestRegisteredSection(t *testing.T) {
	st, a, _ := setup(t)
	RegisterSection(Section{Name: "Kontakte", Write: func(ctx context.Context, st *store.Store, accountID int64, add func(string) (io.Writer, error)) error {
		w, err := add("kontakte.vcf")
		if err != nil {
			return err
		}
		_, err = io.WriteString(w, "BEGIN:VCARD\r\nEND:VCARD\r\n")
		return err
	}})
	t.Cleanup(func() { sectionsMu.Lock(); sections = nil; sectionsMu.Unlock() })
	var buf bytes.Buffer
	if err := Build(context.Background(), st, a.ID, &buf, nil); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(unzip(t, buf.Bytes())["kontakte.vcf"], "BEGIN:VCARD") {
		t.Fatal("section not exported")
	}
}

func TestManagerLifecycle(t *testing.T) {
	st, a, dir := setup(t)
	ctx := context.Background()
	m := &Manager{Store: st, DataDir: dir}
	e, err := m.Start(ctx, a.ID, a.ID, false)
	if err != nil {
		t.Fatal(err)
	}
	m.Wait()
	e, _ = st.GetDataExport(ctx, e.ID)
	if e.Status != "done" || e.Size == 0 || e.ExpiresAt == nil || e.ExpiresAt.Sub(*e.FinishedAt) != DefaultTTL {
		t.Fatalf("export = %+v", e)
	}
	p, err := m.Path(e)
	if err != nil {
		t.Fatal(err)
	}
	if info, _ := os.Stat(p); info == nil || filepath.Dir(p) != m.Dir() {
		t.Fatal("file missing")
	}

	// Rate limit for the user, not for admins.
	var rl *RateLimitError
	if _, err := m.Start(ctx, a.ID, a.ID, false); !errors.As(err, &rl) || rl.RetryAfter <= 0 {
		t.Fatalf("second export not rate limited: %v", err)
	}
	if _, err := m.Start(ctx, a.ID, 999, true); err != nil {
		t.Fatalf("admin export limited: %v", err)
	}
	m.Wait()

	// Expiry removes the file.
	past := time.Now().Add(-time.Minute)
	e.ExpiresAt = &past
	st.SaveDataExport(ctx, e)
	os.WriteFile(filepath.Join(m.Dir(), "stray.zip"), []byte("x"), 0o600)
	m.Cleanup(ctx)
	if _, err := os.Stat(p); !os.IsNotExist(err) {
		t.Fatal("expired file kept")
	}
	if _, err := os.Stat(filepath.Join(m.Dir(), "stray.zip")); !os.IsNotExist(err) {
		t.Fatal("orphan file kept")
	}
	e, _ = st.GetDataExport(ctx, e.ID)
	if e.Status != "expired" {
		t.Fatalf("status = %s", e.Status)
	}
	if _, err := m.Path(e); err == nil {
		t.Fatal("expired export downloadable")
	}
	list, _ := st.ListDataExports(ctx, a.ID, a.ID)
	if len(list) != 1 {
		t.Fatalf("user sees %d exports, want only own", len(list))
	}
}

func TestFold(t *testing.T) {
	long := "DESCRIPTION:" + strings.Repeat("ä", 60)
	folded := fold(long)
	for _, l := range strings.Split(folded, "\r\n") {
		if len(l) > 75 {
			t.Fatalf("line too long: %d", len(l))
		}
	}
	if strings.ReplaceAll(folded, "\r\n ", "") != long {
		t.Fatal("unfold differs")
	}
}

func keys(m map[string]string) []string {
	var out []string
	for k := range m {
		out = append(out, k)
	}
	return out
}

func values(m map[string]string) []string {
	var out []string
	for _, v := range m {
		out = append(out, v)
	}
	return out
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}
