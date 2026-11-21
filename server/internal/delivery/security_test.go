package delivery_test

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/smtp"
	"strings"
	"testing"

	"postwerk/internal/av"
	"postwerk/internal/mailauth"
	"postwerk/internal/store"
)

// fakeAuth returns a fixed authentication result (keeping the From domain).
func fakeAuth(r mailauth.Result) func(context.Context, net.IP, string, string, string, []byte) mailauth.Result {
	return func(_ context.Context, _ net.IP, _, mailFrom, fromDomain string, _ []byte) mailauth.Result {
		res := r
		res.FromDomain = fromDomain
		if res.SPFDomain == "" {
			res.SPFDomain = store.DomainOf(mailFrom)
		}
		return res
	}
}

func mail(from, to, subject string, extra ...string) []byte {
	h := ""
	if from != "" {
		h = "From: " + from + "\r\n"
	}
	for _, e := range extra {
		h += e + "\r\n"
	}
	return []byte(h + "To: " + to + "\r\nSubject: " + subject + "\r\nMessage-ID: <" + strings.ReplaceAll(subject, " ", "") + "@sender.test>\r\nDate: Fri, 20 Nov 2026 10:00:00 +0100\r\n\r\nInhalt\r\n")
}

func mailboxCount(t *testing.T, st *store.Store, email, role string) int {
	t.Helper()
	a, err := st.GetAccountByEmail(context.Background(), email)
	if err != nil {
		t.Fatal(err)
	}
	mb, _ := st.MailboxByRole(context.Background(), a.ID, role)
	msgs, err := st.ListMailboxMessages(context.Background(), mb)
	if err != nil {
		t.Fatal(err)
	}
	return len(msgs)
}

func expectCode(t *testing.T, err error, code string) {
	t.Helper()
	if err == nil || !strings.Contains(err.Error(), code) {
		t.Fatalf("expected %s, got %v", code, err)
	}
}

func TestDMARCRejectAndQuarantine(t *testing.T) {
	e := setup(t)
	// Spam filter disabled: DMARC must be enforced anyway.
	s := e.svc.Settings.Get()
	s.Spam.Enabled = false
	if err := e.svc.Settings.Save(context.Background(), s); err != nil {
		t.Fatal(err)
	}

	e.svc.AuthCheck = fakeAuth(mailauth.Result{SPF: "fail", DKIM: "none", DMARC: "fail", DMARCPolicy: "reject"})
	err := smtp.SendMail(e.smtp, nil, "x@bank.test", []string{"alice@example.test"}, mail("Bank <service@bank.test>", "alice@example.test", "Konto"))
	expectCode(t, err, "550")
	if !strings.Contains(err.Error(), "DMARC") {
		t.Fatalf("unexpected reply: %v", err)
	}

	e.svc.AuthCheck = fakeAuth(mailauth.Result{SPF: "fail", DKIM: "none", DMARC: "fail", DMARCPolicy: "quarantine"})
	if err := smtp.SendMail(e.smtp, nil, "x@bank.test", []string{"alice@example.test"}, mail("service@bank.test", "alice@example.test", "Quarantaene")); err != nil {
		t.Fatal(err)
	}
	if n := mailboxCount(t, e.st, "alice@example.test", "junk"); n != 1 {
		t.Fatalf("junk = %d, want 1", n)
	}
	if n := mailboxCount(t, e.st, "alice@example.test", "inbox"); n != 0 {
		t.Fatalf("inbox = %d, want 0", n)
	}

	e.svc.AuthCheck = fakeAuth(mailauth.Result{SPF: "fail", DKIM: "none", DMARC: "fail", DMARCPolicy: "none"})
	if err := smtp.SendMail(e.smtp, nil, "x@bank.test", []string{"alice@example.test"}, mail("service@bank.test", "alice@example.test", "Nur Bericht")); err != nil {
		t.Fatal(err)
	}
	if n := mailboxCount(t, e.st, "alice@example.test", "inbox"); n != 1 {
		t.Fatalf("p=none: inbox = %d, want 1", n)
	}
}

func TestSpoofedLocalSender(t *testing.T) {
	e := setup(t)
	e.svc.AuthCheck = fakeAuth(mailauth.Result{SPF: "pass", SPFDomain: "attacker.test", DKIM: "none", DMARC: "none"})
	// Header From claims a hosted domain, SPF passes only for the attacker.
	err := smtp.SendMail(e.smtp, nil, "x@attacker.test", []string{"alice@example.test"}, mail("Chef <bob@example.test>", "alice@example.test", "Ueberweisung"))
	expectCode(t, err, "550")
	if !strings.Contains(err.Error(), "Spoofed local sender") {
		t.Fatalf("unexpected reply: %v", err)
	}
	// MAIL FROM claims a hosted domain without SPF pass.
	e.svc.AuthCheck = fakeAuth(mailauth.Result{SPF: "softfail", DKIM: "none", DMARC: "none"})
	err = smtp.SendMail(e.smtp, nil, "bob@example.test", []string{"alice@example.test"}, mail("x@sender.test", "alice@example.test", "Envelope"))
	expectCode(t, err, "550")

	// A valid DKIM signature of the hosted domain is accepted.
	e.svc.AuthCheck = fakeAuth(mailauth.Result{SPF: "fail", SPFDomain: "list.test", DKIM: "pass", DKIMDomains: []string{"example.test"}, DMARC: "pass"})
	if err := smtp.SendMail(e.smtp, nil, "x@list.test", []string{"alice@example.test"}, mail("bob@example.test", "alice@example.test", "Weitergeleitet")); err != nil {
		t.Fatal(err)
	}
	// SPF pass for the hosted domain is accepted.
	e.svc.AuthCheck = fakeAuth(mailauth.Result{SPF: "pass", DKIM: "none", DMARC: "pass"})
	if err := smtp.SendMail(e.smtp, nil, "bob@example.test", []string{"alice@example.test"}, mail("bob@example.test", "alice@example.test", "SPF ok")); err != nil {
		t.Fatal(err)
	}
	if n := inboxCount(t, e.st, "alice@example.test"); n != 2 {
		t.Fatalf("inbox = %d, want 2", n)
	}
}

func TestInboundInvalidFromHeader(t *testing.T) {
	e := setup(t)
	e.svc.AuthCheck = fakeAuth(mailauth.Result{SPF: "none", DKIM: "none", DMARC: "none"})
	err := smtp.SendMail(e.smtp, nil, "x@sender.test", []string{"alice@example.test"}, mail("", "alice@example.test", "Ohne From"))
	expectCode(t, err, "550")
	err = smtp.SendMail(e.smtp, nil, "x@sender.test", []string{"alice@example.test"}, mail("a@sender.test", "alice@example.test", "Zwei From", "From: bob@example.test"))
	expectCode(t, err, "550")
	err = smtp.SendMail(e.smtp, nil, "x@sender.test", []string{"alice@example.test"}, mail("<<kaputt", "alice@example.test", "Kaputt"))
	expectCode(t, err, "550")
	err = smtp.SendMail(e.smtp, nil, "x@sender.test", []string{"alice@example.test"}, mail("a@sender.test, b@bank.test", "alice@example.test", "Zwei Domains"))
	expectCode(t, err, "550")
}

func TestHopLimit(t *testing.T) {
	e := setup(t)
	e.svc.AuthCheck = fakeAuth(mailauth.Result{SPF: "none", DKIM: "none", DMARC: "none"})
	var hops []string
	for i := 0; i < 31; i++ {
		hops = append(hops, fmt.Sprintf("Received: from hop%d.test by hop%d.test; Fri, 20 Nov 2026 10:00:00 +0100", i, i+1))
	}
	err := smtp.SendMail(e.smtp, nil, "x@sender.test", []string{"alice@example.test"}, mail("x@sender.test", "alice@example.test", "Schleife", hops...))
	expectCode(t, err, "554")
	if !strings.Contains(err.Error(), "5.4.6") {
		t.Fatalf("unexpected reply: %v", err)
	}
	if err := smtp.SendMail(e.smtp, nil, "x@sender.test", []string{"alice@example.test"}, mail("x@sender.test", "alice@example.test", "Kurz", hops[:30]...)); err != nil {
		t.Fatalf("30 hops must pass: %v", err)
	}
}

func TestVirusScannerFailureDefers(t *testing.T) {
	e := setup(t)
	e.svc.AuthCheck = fakeAuth(mailauth.Result{SPF: "none", DKIM: "none", DMARC: "none"})
	s := e.svc.Settings.Get()
	s.Antivirus.Enabled = true
	if err := e.svc.Settings.Save(context.Background(), s); err != nil {
		t.Fatal(err)
	}
	e.svc.VirusScan = func(string, []byte) (string, error) { return "", errors.New("clamd connect: connection refused") }
	err := smtp.SendMail(e.smtp, nil, "x@sender.test", []string{"alice@example.test"}, mail("x@sender.test", "alice@example.test", "Scanner weg"))
	expectCode(t, err, "451")
	e.svc.VirusScan = func(string, []byte) (string, error) { return "", av.ErrSizeLimit }
	err = smtp.SendMail(e.smtp, nil, "x@sender.test", []string{"alice@example.test"}, mail("x@sender.test", "alice@example.test", "Zu gross"))
	expectCode(t, err, "451")
	if n := inboxCount(t, e.st, "alice@example.test"); n != 0 {
		t.Fatalf("message accepted despite scanner failure")
	}
	// Antivirus disabled: the scanner is not consulted.
	s.Antivirus.Enabled = false
	_ = e.svc.Settings.Save(context.Background(), s)
	if err := smtp.SendMail(e.smtp, nil, "x@sender.test", []string{"alice@example.test"}, mail("x@sender.test", "alice@example.test", "Ohne AV")); err != nil {
		t.Fatal(err)
	}
}

func TestForgedTrustHeadersStripped(t *testing.T) {
	e := setup(t)
	e.svc.AuthCheck = fakeAuth(mailauth.Result{SPF: "fail", DKIM: "none", DMARC: "none"})
	msg := mail("x@sender.test", "alice@example.test", "Gefaelscht",
		"Authentication-Results: mail.example.test; spf=pass smtp.mailfrom=bank.test; dkim=pass; dmarc=pass",
		"Authentication-Results: other.test; spf=pass",
		"X-Spam-Status: No",
		"X-Spam-Score: -10",
		"X-Postwerk-Verified: yes",
		"X-Virus: none")
	if err := smtp.SendMail(e.smtp, nil, "x@sender.test", []string{"alice@example.test"}, msg); err != nil {
		t.Fatal(err)
	}
	a, _ := e.st.GetAccountByEmail(context.Background(), "alice@example.test")
	list, _, _ := e.st.ListMessages(context.Background(), store.MessageQuery{AccountID: a.ID, Limit: 10})
	raw, _ := e.st.Blobs.Get(list[0].BlobHash)
	s := string(raw)
	if strings.Count(s, "Authentication-Results: mail.example.test;") != 1 || !strings.Contains(s, "Authentication-Results: mail.example.test; spf=fail") {
		t.Fatalf("forged Authentication-Results kept:\n%s", s)
	}
	if !strings.Contains(s, "Authentication-Results: other.test; spf=pass") {
		t.Fatalf("foreign Authentication-Results removed:\n%s", s)
	}
	if strings.Count(s, "X-Spam-Status:") > 1 || strings.Contains(s, "X-Spam-Score: -10") || strings.Contains(s, "X-Postwerk-Verified") || strings.Contains(s, "X-Virus: none") {
		t.Fatalf("forged spam headers kept:\n%s", s)
	}
}

func TestSubmissionFromHeaderChecks(t *testing.T) {
	e := setup(t)
	alice, _ := e.st.GetAccountByEmail(context.Background(), "alice@example.test")
	cases := map[string]string{
		"missing":       "To: bob@example.test\r\nSubject: x\r\n\r\nx\r\n",
		"unparseable":   "From: <<<\r\nTo: bob@example.test\r\nSubject: x\r\n\r\nx\r\n",
		"duplicate":     "From: alice@example.test\r\nFrom: bob@example.test\r\nTo: bob@example.test\r\nSubject: x\r\n\r\nx\r\n",
		"two addresses": "From: alice@example.test, bob@example.test\r\nTo: bob@example.test\r\nSubject: x\r\n\r\nx\r\n",
		"foreign":       "From: bob@example.test\r\nTo: bob@example.test\r\nSubject: x\r\n\r\nx\r\n",
		"sender":        "From: alice@example.test\r\nSender: bob@example.test\r\nTo: bob@example.test\r\nSubject: x\r\n\r\nx\r\n",
	}
	for name, raw := range cases {
		_, err := e.svc.Submit(context.Background(), alice, "alice@example.test", []string{"bob@example.test"}, []byte(raw), false)
		if err == nil || !strings.Contains(err.Error(), "553") {
			t.Errorf("%s: expected 553, got %v", name, err)
		}
	}
	if n := inboxCount(t, e.st, "bob@example.test"); n != 0 {
		t.Fatalf("bob received %d rejected messages", n)
	}
	ok := "From: Alice <alice@example.test>\r\nSender: alice@example.test\r\nTo: bob@example.test\r\nSubject: x\r\n\r\nx\r\n"
	if _, err := e.svc.Submit(context.Background(), alice, "alice@example.test", []string{"bob@example.test"}, []byte(ok), false); err != nil {
		t.Fatal(err)
	}
}

func setQuota(t *testing.T, st *store.Store, email string, q int64) {
	t.Helper()
	a, err := st.GetAccountByEmail(context.Background(), email)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.UpdateAccount(context.Background(), a.ID, store.AccountPatch{QuotaBytes: &q}); err != nil {
		t.Fatal(err)
	}
}

func TestQuotaFullRecipientBounces(t *testing.T) {
	e := setup(t)
	e.svc.AuthCheck = fakeAuth(mailauth.Result{SPF: "none", DKIM: "none", DMARC: "none"})
	setQuota(t, e.st, "bob@example.test", 100)
	// Bob still has room at RCPT time, but this message does not fit.
	if err := smtp.SendMail(e.smtp, nil, "x@sender.test", []string{"alice@example.test", "bob@example.test"}, mail("x@sender.test", "alice@example.test", "Mehrere")); err != nil {
		t.Fatal(err)
	}
	if n := inboxCount(t, e.st, "alice@example.test"); n != 1 {
		t.Fatalf("alice inbox = %d", n)
	}
	items, _ := e.st.ListQueue(context.Background())
	if len(items) != 1 || items[0].From != "" || items[0].To[0] != "x@sender.test" {
		t.Fatalf("expected bounce to sender, got %+v", items)
	}
	raw, _ := e.st.Blobs.Get(items[0].BlobHash)
	if !strings.Contains(string(raw), "bob@example.test") {
		t.Fatalf("bounce does not name the recipient:\n%s", raw)
	}

	// Single full recipient: rejected directly.
	err := smtp.SendMail(e.smtp, nil, "x@sender.test", []string{"bob@example.test"}, mail("x@sender.test", "bob@example.test", "Einzeln"))
	expectCode(t, err, "552")

	// Already full at RCPT time: store something without quota, then set a
	// quota below the used space.
	bob, _ := e.st.GetAccountByEmail(context.Background(), "bob@example.test")
	inbox, _ := e.st.MailboxByRole(context.Background(), bob.ID, "inbox")
	setQuota(t, e.st, "bob@example.test", 0)
	if _, err := e.svc.StoreMessage(context.Background(), bob.ID, inbox, []byte("Subject: x\r\n\r\nx\r\n"), nil, nil); err != nil {
		t.Fatal(err)
	}
	setQuota(t, e.st, "bob@example.test", 1)
	c, err := smtp.Dial(e.smtp)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if err := c.Mail("x@sender.test"); err != nil {
		t.Fatal(err)
	}
	expectCode(t, c.Rcpt("bob@example.test"), "552")
}

func TestSubmitLocalQuotaBounces(t *testing.T) {
	e := setup(t)
	setQuota(t, e.st, "bob@example.test", 10)
	alice, _ := e.st.GetAccountByEmail(context.Background(), "alice@example.test")
	raw := []byte("From: alice@example.test\r\nTo: bob@example.test\r\nSubject: Voll\r\n\r\nText\r\n")
	if _, err := e.svc.Submit(context.Background(), alice, "alice@example.test", []string{"bob@example.test"}, raw, false); err != nil {
		t.Fatal(err)
	}
	if n := inboxCount(t, e.st, "alice@example.test"); n != 1 {
		t.Fatalf("alice should have received a bounce, inbox = %d", n)
	}
	if n := inboxCount(t, e.st, "bob@example.test"); n != 0 {
		t.Fatalf("bob over quota but got %d", n)
	}
}

func TestSpamToPureForwardIsNotDropped(t *testing.T) {
	e := setup(t)
	e.svc.AuthCheck = fakeAuth(mailauth.Result{SPF: "none", DKIM: "none", DMARC: "none"})
	s := e.svc.Settings.Get()
	s.Spam.Enabled = true
	s.Spam.JunkThreshold = -100
	s.Spam.RejectThreshold = 0
	s.Spam.Greylisting.Enabled = false
	_ = e.svc.Settings.Save(context.Background(), s)
	if _, err := e.st.CreateAlias(context.Background(), "fwd@example.test", []string{"ziel@other.test"}); err != nil {
		t.Fatal(err)
	}
	if err := smtp.SendMail(e.smtp, nil, "x@sender.test", []string{"fwd@example.test"}, mail("x@sender.test", "fwd@example.test", "Werbung")); err != nil {
		t.Fatal(err)
	}
	items, _ := e.st.ListQueue(context.Background())
	if len(items) != 1 || items[0].To[0] != "ziel@other.test" {
		t.Fatalf("spam forward dropped: %+v", items)
	}
	raw, _ := e.st.Blobs.Get(items[0].BlobHash)
	if !strings.Contains(string(raw), "X-Spam-Flag: YES") {
		t.Fatalf("forwarded spam not tagged:\n%s", raw)
	}
}

func TestSRSForwardAndBounce(t *testing.T) {
	e := setup(t)
	e.svc.AuthCheck = fakeAuth(mailauth.Result{SPF: "none", DKIM: "none", DMARC: "none"})
	if _, err := e.st.CreateAlias(context.Background(), "fwd@example.test", []string{"ziel@other.test"}); err != nil {
		t.Fatal(err)
	}
	if err := smtp.SendMail(e.smtp, nil, "orig@sender.test", []string{"fwd@example.test"}, mail("orig@sender.test", "fwd@example.test", "Weiterleitung")); err != nil {
		t.Fatal(err)
	}
	items, _ := e.st.ListQueue(context.Background())
	if len(items) != 1 {
		t.Fatalf("queue = %+v", items)
	}
	srsAddr := items[0].From
	if !strings.HasPrefix(srsAddr, "SRS0=") || !strings.HasSuffix(srsAddr, "=sender.test=orig@example.test") {
		t.Fatalf("forward sender not rewritten: %q", srsAddr)
	}
	_ = e.st.DeleteQueueItem(context.Background(), items[0].ID)

	// The bounce comes back to the SRS address and is sent on to the
	// original sender with a null envelope sender.
	dsn := mail("MAILER-DAEMON@other.test", srsAddr, "Undelivered")
	if err := smtp.SendMail(e.smtp, nil, "", []string{srsAddr}, dsn); err != nil {
		t.Fatalf("bounce to srs address: %v", err)
	}
	items, _ = e.st.ListQueue(context.Background())
	if len(items) != 1 || items[0].From != "" || items[0].To[0] != "orig@sender.test" {
		t.Fatalf("bounce not reverse-forwarded: %+v", items)
	}
	_ = e.st.DeleteQueueItem(context.Background(), items[0].ID)

	// Forged or tampered SRS addresses are rejected at RCPT time.
	forged := strings.Replace(srsAddr, "=orig@", "=mallory@", 1)
	err := smtp.SendMail(e.smtp, nil, "", []string{forged}, dsn)
	expectCode(t, err, "550")

	// A queue bounce for the forwarded message reaches the original sender.
	e.svc.Bounce(context.Background(), srsAddr, []string{"ziel@other.test"}, "Benutzer unbekannt", dsn)
	items, _ = e.st.ListQueue(context.Background())
	if len(items) != 1 || items[0].To[0] != "orig@sender.test" {
		t.Fatalf("queue bounce not reversed: %+v", items)
	}
}

func TestVacationOnlyWhenAddressed(t *testing.T) {
	e := setup(t)
	e.svc.AuthCheck = fakeAuth(mailauth.Result{SPF: "none", DKIM: "none", DMARC: "none"})
	alice, _ := e.st.GetAccountByEmail(context.Background(), "alice@example.test")
	if err := e.st.SetVacation(context.Background(), alice.ID, &store.Vacation{Enabled: true, Subject: "Urlaub", Body: "Bin weg"}); err != nil {
		t.Fatal(err)
	}
	queued := func() []*store.QueueItem {
		items, _ := e.st.ListQueue(context.Background())
		var out []*store.QueueItem
		for i := range items {
			if items[i].To[0] != "extern@other.test" {
				out = append(out, items[i])
			}
		}
		return out
	}
	// Bcc / alias delivery without alice in To or Cc: no reply.
	if err := smtp.SendMail(e.smtp, nil, "a@sender.test", []string{"alice@example.test"}, mail("a@sender.test", "jemand@sender.test", "Bcc")); err != nil {
		t.Fatal(err)
	}
	if q := queued(); len(q) != 0 {
		t.Fatalf("vacation reply to bcc: %+v", q)
	}
	// SRS envelope sender: no reply.
	if err := smtp.SendMail(e.smtp, nil, "SRS0=abcd=XY=sender.test=b@fwd.test", []string{"alice@example.test"}, mail("b@sender.test", "alice@example.test", "SRS")); err != nil {
		t.Fatal(err)
	}
	if q := queued(); len(q) != 0 {
		t.Fatalf("vacation reply to srs sender: %+v", q)
	}
	// List bounce address: no reply.
	if err := smtp.SendMail(e.smtp, nil, "liste-bounces@sender.test", []string{"alice@example.test"}, mail("c@sender.test", "alice@example.test", "Liste")); err != nil {
		t.Fatal(err)
	}
	if q := queued(); len(q) != 0 {
		t.Fatalf("vacation reply to list bounce: %+v", q)
	}
	// Addressed directly: one reply with a sanitized In-Reply-To.
	msg := mail("d@sender.test", "Alice <alice@example.test>", "Direkt")
	msg = []byte(strings.Replace(string(msg), "Message-ID: <Direkt@sender.test>", "Message-ID: <evil@sender.test>\r\n\tX-Injected: 1", 1))
	if err := smtp.SendMail(e.smtp, nil, "d@sender.test", []string{"alice@example.test"}, msg); err != nil {
		t.Fatal(err)
	}
	q := queued()
	if len(q) != 1 || q[0].To[0] != "d@sender.test" || q[0].From != "" {
		t.Fatalf("expected one vacation reply, got %+v", q)
	}
	raw, _ := e.st.Blobs.Get(q[0].BlobHash)
	if strings.Contains(string(raw), "\r\nX-Injected") {
		t.Fatalf("header injected via Message-ID:\n%s", raw)
	}
}
