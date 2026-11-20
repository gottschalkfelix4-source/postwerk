package migrate

import (
	"archive/zip"
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"testing"

	"github.com/emersion/go-imap"

	"postwerk/internal/auth"
	"postwerk/internal/delivery"
	"postwerk/internal/store"
)

func TestMapFolder(t *testing.T) {
	cases := []struct {
		name, delim string
		attrs       []string
		want        string
	}{
		{"INBOX", "/", nil, "inbox"},
		{"Sent Items", "/", nil, "sent"},
		{"Gesendete Objekte", "/", nil, "sent"},
		{"INBOX.Sent", ".", nil, "sent"},
		{"[Gmail]/Gesendet", "/", []string{imap.SentAttr}, "sent"},
		{"Whatever", "/", []string{imap.TrashAttr}, "trash"},
		{"Projekte/Sent", "/", nil, "Projekte/Sent"},
		{"INBOX.Kunden.Mueller", ".", nil, "Kunden/Mueller"},
		{"Archiv", "/", nil, "archive"},
	}
	for _, c := range cases {
		if got, _ := mapFolder(c.name, c.delim, c.attrs); got != c.want {
			t.Errorf("mapFolder(%q) = %q, want %q", c.name, got, c.want)
		}
	}
}

func setup(t *testing.T) (*delivery.Service, *store.Account) {
	ctx := context.Background()
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	settings, _ := st.LoadSettings(ctx, "mail.example.test")
	h, _ := auth.HashPassword("geheim-passwort")
	a := &store.Account{Email: "u@example.test", PasswordHash: h, Role: "user"}
	if err := st.CreateAccount(ctx, a); err != nil {
		t.Fatal(err)
	}
	svc := &delivery.Service{Store: st, Settings: settings, Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	return svc, a
}

const mbox = "From alice@example.org Mon Jan  6 10:00:00 2025\n" +
	"From: Alice <alice@example.org>\nSubject: Erste\nMessage-ID: <m1@example.org>\nStatus: RO\n\nHallo\n>From the start\n\n" +
	"From bob@example.org Tue Jan  7 11:30:00 2025\n" +
	"From: Bob <bob@example.org>\nSubject: Zweite\nMessage-ID: <m2@example.org>\nX-Mozilla-Status: 0004\n\nText\n"

func TestImportMbox(t *testing.T) {
	svc, acct := setup(t)
	dir := t.TempDir()
	p := filepath.Join(dir, "Inbox")
	os.WriteFile(p, []byte(mbox), 0o600)
	fi := &FileImporter{Delivery: svc, Account: acct, MaxSize: 10 << 20}
	res, err := fi.ImportFile(context.Background(), p, "Inbox")
	if err != nil {
		t.Fatal(err)
	}
	if res.Imported != 2 || res.Failed != 0 {
		t.Fatalf("result = %+v", res)
	}
	list, _, _ := svc.Store.ListMessages(context.Background(), store.MessageQuery{AccountID: acct.ID, Limit: 10})
	bySubj := map[string]*store.Message{}
	for _, m := range list {
		bySubj[m.Subject] = m
	}
	if m := bySubj["Erste"]; m == nil || !m.Seen || m.ReceivedAt.Year() != 2025 {
		t.Errorf("first message wrong: %+v", m)
	}
	if m := bySubj["Zweite"]; m == nil || !m.Flagged {
		t.Errorf("second message should be flagged: %+v", m)
	}
	raw, _ := svc.Store.Blobs.Get(bySubj["Erste"].BlobHash)
	if !contains(string(raw), "\r\nFrom the start\r\n") {
		t.Errorf("mboxrd unescaping/CRLF wrong:\n%q", raw)
	}
	// Re-import is skipped as duplicates.
	res, _ = fi.ImportFile(context.Background(), p, "Inbox")
	if res.Imported != 0 || res.Skipped != 2 {
		t.Fatalf("second import = %+v", res)
	}
}

func TestImportZipFolders(t *testing.T) {
	svc, acct := setup(t)
	p := filepath.Join(t.TempDir(), "export.zip")
	f, _ := os.Create(p)
	zw := zip.NewWriter(f)
	w, _ := zw.Create("Kunden/a.eml")
	w.Write([]byte("From: x@example.org\r\nSubject: Kunde\r\nMessage-ID: <z1@example.org>\r\n\r\nHallo\r\n"))
	w, _ = zw.Create("Sent.mbox/mbox")
	w.Write([]byte(mbox))
	zw.Close()
	f.Close()
	fi := &FileImporter{Delivery: svc, Account: acct, MaxSize: 10 << 20}
	res, err := fi.ImportFile(context.Background(), p, "export.zip")
	if err != nil || res.Imported != 3 {
		t.Fatalf("res=%+v err=%v", res, err)
	}
	boxes, _ := svc.Store.ListMailboxes(context.Background(), acct.ID)
	counts := map[string]int{}
	for _, b := range boxes {
		counts[b.Name] = b.Total
	}
	if counts["Kunden"] != 1 || counts["Gesendet"] != 2 {
		t.Fatalf("folder counts = %v", counts)
	}
}

func contains(s, sub string) bool {
	return len(s) >= len(sub) && (func() bool {
		for i := 0; i+len(sub) <= len(s); i++ {
			if s[i:i+len(sub)] == sub {
				return true
			}
		}
		return false
	})()
}
