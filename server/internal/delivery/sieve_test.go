package delivery_test

import (
	"context"
	"net/smtp"
	"strings"
	"testing"

	"postwerk/internal/mailauth"
	"postwerk/internal/store"
)

func sieveSetup(t *testing.T) *env {
	t.Helper()
	e := setup(t)
	e.svc.AuthCheck = fakeAuth(mailauth.Result{SPF: "pass", DKIM: "none", DMARC: "none"})
	return e
}

func activate(t *testing.T, st *store.Store, email, script string) {
	t.Helper()
	ctx := context.Background()
	a, err := st.GetAccountByEmail(ctx, email)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.PutSieveScript(ctx, a.ID, "test", script); err != nil {
		t.Fatal(err)
	}
	if err := st.SetActiveSieveScript(ctx, a.ID, "test"); err != nil {
		t.Fatal(err)
	}
}

func folder(t *testing.T, st *store.Store, email, name string) []*store.Message {
	t.Helper()
	ctx := context.Background()
	a, _ := st.GetAccountByEmail(ctx, email)
	id, err := st.MailboxByName(ctx, a.ID, name)
	if err != nil {
		return nil
	}
	msgs, err := st.ListMailboxMessages(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	return msgs
}

func send(t *testing.T, e *env, from string, to []string, raw []byte) error {
	t.Helper()
	return smtp.SendMail(e.smtp, nil, from, to, raw)
}

func queued(t *testing.T, st *store.Store) []*store.QueueItem {
	t.Helper()
	items, err := st.ListQueue(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return items
}

func TestSieveFileintoAndFlags(t *testing.T) {
	e := sieveSetup(t)
	activate(t, e.st, "alice@example.test", `require ["fileinto", "imap4flags", "mailbox"];
if header :contains "subject" "Rechnung" { addflag "\\Seen"; fileinto "Rechnungen"; stop; }
if header :contains "subject" "Projekt" { fileinto :create :flags ["\\Flagged", "$Arbeit"] "Projekte/Alpha"; }`)

	// Missing folder without :create: kept in the inbox.
	if err := send(t, e, "x@sender.test", []string{"alice@example.test"}, mail("x@sender.test", "alice@example.test", "Rechnung 1")); err != nil {
		t.Fatal(err)
	}
	if n := inboxCount(t, e.st, "alice@example.test"); n != 1 {
		t.Fatalf("inbox = %d, want 1 (missing folder)", n)
	}
	alice, _ := e.st.GetAccountByEmail(context.Background(), "alice@example.test")
	if _, err := e.st.CreateMailbox(context.Background(), alice.ID, "Rechnungen"); err != nil {
		t.Fatal(err)
	}
	if err := send(t, e, "x@sender.test", []string{"alice@example.test"}, mail("x@sender.test", "alice@example.test", "Rechnung 2")); err != nil {
		t.Fatal(err)
	}
	got := folder(t, e.st, "alice@example.test", "Rechnungen")
	if len(got) != 1 || !got[0].Seen || got[0].Subject != "Rechnung 2" {
		t.Fatalf("Rechnungen = %+v", got)
	}
	// :create makes the folder; flags and keywords are set.
	if err := send(t, e, "x@sender.test", []string{"alice@example.test"}, mail("x@sender.test", "alice@example.test", "Projekt Start")); err != nil {
		t.Fatal(err)
	}
	got = folder(t, e.st, "alice@example.test", "Projekte/Alpha")
	if len(got) != 1 || !got[0].Flagged || got[0].Seen {
		t.Fatalf("Projekte/Alpha = %+v", got)
	}
	msgs, _ := e.st.IMAPMessages(context.Background(), got[0].MailboxID)
	if len(msgs) != 1 || len(msgs[0].Flags.Keywords) != 1 || msgs[0].Flags.Keywords[0] != "$Arbeit" {
		t.Fatalf("keywords = %+v", msgs)
	}
	if n := inboxCount(t, e.st, "alice@example.test"); n != 1 {
		t.Fatalf("inbox = %d, want still 1", n)
	}
	// Bob has no script: normal delivery.
	if err := send(t, e, "x@sender.test", []string{"bob@example.test"}, mail("x@sender.test", "bob@example.test", "Rechnung 3")); err != nil {
		t.Fatal(err)
	}
	if n := inboxCount(t, e.st, "bob@example.test"); n != 1 {
		t.Fatalf("bob inbox = %d", n)
	}
}

func TestSieveDiscardAndReject(t *testing.T) {
	e := sieveSetup(t)
	activate(t, e.st, "alice@example.test", `require ["reject"];
if header :contains "subject" "Werbung" { discard; }
if header :contains "subject" "Nein" { reject "Keine Zustellung gewünscht"; }`)

	if err := send(t, e, "x@sender.test", []string{"alice@example.test"}, mail("x@sender.test", "alice@example.test", "Werbung")); err != nil {
		t.Fatalf("discard must accept: %v", err)
	}
	if n := inboxCount(t, e.st, "alice@example.test"); n != 0 {
		t.Fatalf("discarded message stored: %d", n)
	}
	// Single recipient: rejected in the SMTP dialogue.
	err := send(t, e, "x@sender.test", []string{"alice@example.test"}, mail("x@sender.test", "alice@example.test", "Nein danke"))
	expectCode(t, err, "550")
	if !strings.Contains(err.Error(), "Keine Zustellung gewuenscht") {
		t.Fatalf("reject reason missing: %v", err)
	}
	if len(queued(t, e.st)) != 0 {
		t.Fatal("synchronous reject must not bounce")
	}
	// Several recipients: Bob gets the message, the sender a bounce.
	if err := send(t, e, "x@sender.test", []string{"alice@example.test", "bob@example.test"}, mail("x@sender.test", "alice@example.test", "Nein 2")); err != nil {
		t.Fatal(err)
	}
	if n := inboxCount(t, e.st, "bob@example.test"); n != 1 {
		t.Fatalf("bob inbox = %d", n)
	}
	items := queued(t, e.st)
	if len(items) != 1 || items[0].From != "" || items[0].To[0] != "x@sender.test" {
		t.Fatalf("expected bounce, got %+v", items)
	}
	raw, _ := e.st.Blobs.Get(items[0].BlobHash)
	if !strings.Contains(string(raw), "Keine Zustellung gewuenscht") || !strings.Contains(string(raw), "alice@example.test") {
		t.Fatalf("bounce text:\n%s", raw)
	}
}

func TestSieveRedirect(t *testing.T) {
	e := sieveSetup(t)
	activate(t, e.st, "alice@example.test", `require ["copy"];
if header :contains "subject" "Extern" { redirect "ziel@other.test"; }
if header :contains "subject" "Kopie" { redirect :copy "bob@example.test"; }`)

	if err := send(t, e, "orig@sender.test", []string{"alice@example.test"}, mail("orig@sender.test", "alice@example.test", "Extern")); err != nil {
		t.Fatal(err)
	}
	items := queued(t, e.st)
	if len(items) != 1 || items[0].To[0] != "ziel@other.test" {
		t.Fatalf("queue = %+v", items)
	}
	if !strings.HasPrefix(items[0].From, "SRS0=") || !strings.HasSuffix(items[0].From, "=sender.test=orig@example.test") {
		t.Fatalf("redirect without SRS: %q", items[0].From)
	}
	raw, _ := e.st.Blobs.Get(items[0].BlobHash)
	// ARC headers (if the domain has a signing key) precede the marker.
	if !strings.Contains("\r\n"+string(raw), "\r\nX-Sieve-Redirected-By: alice@example.test\r\n") {
		t.Fatalf("redirect header missing:\n%s", raw)
	}
	if keys, _ := e.st.SigningKeys(context.Background(), "example.test"); len(keys) > 0 && !strings.HasPrefix(string(raw), "ARC-Seal: i=1;") {
		t.Fatalf("external redirect not ARC-sealed:\n%s", raw)
	}
	if n := inboxCount(t, e.st, "alice@example.test"); n != 0 {
		t.Fatalf("redirect without :copy kept a copy: %d", n)
	}
	_ = e.st.DeleteQueueItem(context.Background(), items[0].ID)

	if err := send(t, e, "orig@sender.test", []string{"alice@example.test"}, mail("orig@sender.test", "alice@example.test", "Kopie")); err != nil {
		t.Fatal(err)
	}
	if inboxCount(t, e.st, "alice@example.test") != 1 || inboxCount(t, e.st, "bob@example.test") != 1 {
		t.Fatal("redirect :copy to local account failed")
	}
}

func TestSieveRedirectLoop(t *testing.T) {
	e := sieveSetup(t)
	activate(t, e.st, "alice@example.test", `redirect "bob@example.test";`)
	activate(t, e.st, "bob@example.test", `redirect "alice@example.test";`)
	if err := send(t, e, "x@sender.test", []string{"alice@example.test"}, mail("x@sender.test", "alice@example.test", "Schleife")); err != nil {
		t.Fatal(err)
	}
	// Alice's second redirect is suppressed and the message kept.
	if a, b := inboxCount(t, e.st, "alice@example.test"), inboxCount(t, e.st, "bob@example.test"); a != 1 || b != 0 {
		t.Fatalf("alice=%d bob=%d, want 1/0", a, b)
	}
	if len(queued(t, e.st)) != 0 {
		t.Fatal("loop produced queue items")
	}
}

func TestSieveSkipsJunk(t *testing.T) {
	e := sieveSetup(t)
	alice, _ := e.st.GetAccountByEmail(context.Background(), "alice@example.test")
	if _, err := e.st.CreateMailbox(context.Background(), alice.ID, "Alles"); err != nil {
		t.Fatal(err)
	}
	activate(t, e.st, "alice@example.test", `require "fileinto"; fileinto "Alles";`)
	s := e.svc.Settings.Get()
	s.Spam.Enabled = true
	s.Spam.JunkThreshold = -100
	s.Spam.RejectThreshold = 1000
	s.Spam.Greylisting.Enabled = false
	_ = e.svc.Settings.Save(context.Background(), s)
	if err := send(t, e, "x@sender.test", []string{"alice@example.test"}, mail("x@sender.test", "alice@example.test", "Spam")); err != nil {
		t.Fatal(err)
	}
	if n := mailboxCount(t, e.st, "alice@example.test", "junk"); n != 1 {
		t.Fatalf("junk = %d", n)
	}
	if n := len(folder(t, e.st, "alice@example.test", "Alles")); n != 0 {
		t.Fatalf("junk mail was filtered into a folder: %d", n)
	}
}

func TestSubaddressDeliveryAndDetail(t *testing.T) {
	e := sieveSetup(t)
	activate(t, e.st, "alice@example.test", `require ["fileinto", "envelope", "subaddress", "variables", "mailbox"];
if envelope :detail :matches "to" "*" { fileinto :create "Tags/${1}"; }`)
	if err := send(t, e, "x@sender.test", []string{"alice+newsletter@example.test"}, mail("x@sender.test", "alice+newsletter@example.test", "Tag")); err != nil {
		t.Fatal(err)
	}
	if got := folder(t, e.st, "alice@example.test", "Tags/newsletter"); len(got) != 1 {
		t.Fatalf("Tags/newsletter = %d", len(got))
	}
	// Without detail the script does not match.
	if err := send(t, e, "x@sender.test", []string{"alice@example.test"}, mail("x@sender.test", "alice@example.test", "Ohne")); err != nil {
		t.Fatal(err)
	}
	if n := inboxCount(t, e.st, "alice@example.test"); n != 1 {
		t.Fatalf("inbox = %d", n)
	}
	// Unknown base address stays unknown.
	expectCode(t, send(t, e, "x@sender.test", []string{"nobody+x@example.test"}, mail("x@sender.test", "nobody@example.test", "Weg")), "550")
}

func TestSieveVacation(t *testing.T) {
	e := sieveSetup(t)
	alice, _ := e.st.GetAccountByEmail(context.Background(), "alice@example.test")
	// The account vacation notice is enabled too: the script's own vacation wins.
	if err := e.st.SetVacation(context.Background(), alice.ID, &store.Vacation{Enabled: true, Subject: "UI", Body: "UI-Text"}); err != nil {
		t.Fatal(err)
	}
	activate(t, e.st, "alice@example.test", `require "vacation"; vacation :days 3 :subject "Sieve-Urlaub" "Bin bis Montag weg.";`)
	for i := 0; i < 2; i++ {
		if err := send(t, e, "d@sender.test", []string{"alice@example.test"}, mail("d@sender.test", "alice@example.test", "Hallo")); err != nil {
			t.Fatal(err)
		}
	}
	items := queued(t, e.st)
	if len(items) != 1 || items[0].To[0] != "d@sender.test" || items[0].From != "" {
		t.Fatalf("expected one vacation reply, got %+v", items)
	}
	raw, _ := e.st.Blobs.Get(items[0].BlobHash)
	if !strings.Contains(string(raw), "Subject: Sieve-Urlaub") || !strings.Contains(string(raw), "Auto-Submitted: auto-replied") {
		t.Fatalf("reply:\n%s", raw)
	}
	if n := inboxCount(t, e.st, "alice@example.test"); n != 2 {
		t.Fatalf("inbox = %d", n)
	}
}

func TestUIVacationWithFilingScript(t *testing.T) {
	e := sieveSetup(t)
	alice, _ := e.st.GetAccountByEmail(context.Background(), "alice@example.test")
	if err := e.st.SetVacation(context.Background(), alice.ID, &store.Vacation{Enabled: true, Subject: "Urlaub", Body: "Bin weg"}); err != nil {
		t.Fatal(err)
	}
	activate(t, e.st, "alice@example.test", `require ["fileinto", "mailbox"]; fileinto :create "Abgelegt";`)
	if err := send(t, e, "d@sender.test", []string{"alice@example.test"}, mail("d@sender.test", "alice@example.test", "Hallo")); err != nil {
		t.Fatal(err)
	}
	items := queued(t, e.st)
	if len(items) != 1 || items[0].To[0] != "d@sender.test" {
		t.Fatalf("expected account vacation reply, got %+v", items)
	}
	raw, _ := e.st.Blobs.Get(items[0].BlobHash)
	if !strings.Contains(string(raw), "Subject: Urlaub") {
		t.Fatalf("reply:\n%s", raw)
	}
}

func TestInvalidActiveScriptFallsBack(t *testing.T) {
	e := sieveSetup(t)
	alice, _ := e.st.GetAccountByEmail(context.Background(), "alice@example.test")
	// Stored directly, bypassing validation.
	if err := e.st.PutSieveScript(context.Background(), alice.ID, "kaputt", `fileinto "X"`); err != nil {
		t.Fatal(err)
	}
	_ = e.st.SetActiveSieveScript(context.Background(), alice.ID, "kaputt")
	if err := send(t, e, "x@sender.test", []string{"alice@example.test"}, mail("x@sender.test", "alice@example.test", "Kaputt")); err != nil {
		t.Fatal(err)
	}
	if n := inboxCount(t, e.st, "alice@example.test"); n != 1 {
		t.Fatalf("inbox = %d", n)
	}
	// Run-time errors fall back to the implicit keep as well.
	activate(t, e.st, "alice@example.test", `require "reject"; keep; reject "x";`)
	if err := send(t, e, "x@sender.test", []string{"alice@example.test"}, mail("x@sender.test", "alice@example.test", "Laufzeit")); err != nil {
		t.Fatal(err)
	}
	if n := inboxCount(t, e.st, "alice@example.test"); n != 2 {
		t.Fatalf("inbox = %d", n)
	}
}

func TestSieveForSubmittedMail(t *testing.T) {
	e := sieveSetup(t)
	activate(t, e.st, "bob@example.test", `require ["fileinto", "mailbox"]; if address :is "from" "alice@example.test" { fileinto :create "Von Alice"; }`)
	alice, _ := e.st.GetAccountByEmail(context.Background(), "alice@example.test")
	raw := []byte("From: alice@example.test\r\nTo: bob@example.test\r\nSubject: Intern\r\n\r\nText\r\n")
	if _, err := e.svc.Submit(context.Background(), alice, "alice@example.test", []string{"bob@example.test"}, raw, false); err != nil {
		t.Fatal(err)
	}
	if got := folder(t, e.st, "bob@example.test", "Von Alice"); len(got) != 1 {
		t.Fatalf("Von Alice = %d", len(got))
	}
}

func TestSieveDryRun(t *testing.T) {
	e := sieveSetup(t)
	alice, _ := e.st.GetAccountByEmail(context.Background(), "alice@example.test")
	res, err := e.svc.SieveDryRun(context.Background(), alice, `require "fileinto"; if header :contains "subject" "Test" { fileinto "X"; }`,
		mail("a@b.test", "alice@example.test", "Test"), "a@b.test", "")
	if err != nil {
		t.Fatal(err)
	}
	if a := res.Final(); len(a) != 1 || a[0].Type != "fileinto" || a[0].Mailbox != "X" {
		t.Fatalf("actions = %+v", a)
	}
	if n := inboxCount(t, e.st, "alice@example.test"); n != 0 {
		t.Fatal("dry run stored a message")
	}
}
