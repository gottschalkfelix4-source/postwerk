package delivery_test

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/base64"
	"net/smtp"
	"os"
	"strings"
	"testing"
	"time"

	"postwerk/internal/mailauth"
	"postwerk/internal/store"
)

func queuedBlobs(t *testing.T, st *store.Store) []string {
	t.Helper()
	items, err := st.ListQueue(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, it := range items {
		raw, err := st.Blobs.Get(it.BlobHash)
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, string(raw))
	}
	return out
}

func TestForwardIsARCSealed(t *testing.T) {
	e := setup(t)
	e.svc.AuthCheck = fakeAuth(mailauth.Result{SPF: "pass", DKIM: "pass", DKIMDomains: []string{"sender.test"}, DMARC: "pass", ARC: "none"})
	if _, err := e.st.CreateAlias(context.Background(), "fwd@example.test", []string{"ziel@other.test"}); err != nil {
		t.Fatal(err)
	}
	if err := smtp.SendMail(e.smtp, nil, "orig@sender.test", []string{"fwd@example.test"}, mail("orig@sender.test", "fwd@example.test", "Versiegelt")); err != nil {
		t.Fatal(err)
	}
	blobs := queuedBlobs(t, e.st)
	if len(blobs) != 1 {
		t.Fatalf("queue: %d", len(blobs))
	}
	raw := blobs[0]
	if !strings.HasPrefix(raw, "ARC-Seal: i=1; a=rsa-sha256;") {
		t.Fatalf("forward not sealed:\n%.400s", raw)
	}
	for _, want := range []string{"cv=none", "d=example.test; s=pw1", "ARC-Message-Signature: i=1;", "ARC-Authentication-Results: i=1; mail.example.test;", "dmarc=pass header.from=sender.test"} {
		if !strings.Contains(raw, want) {
			t.Errorf("missing %q in sealed message", want)
		}
	}
	// Local deliveries are not sealed.
	if err := smtp.SendMail(e.smtp, nil, "orig@sender.test", []string{"alice@example.test"}, mail("orig@sender.test", "alice@example.test", "Lokal")); err != nil {
		t.Fatal(err)
	}
	a, _ := e.st.GetAccountByEmail(context.Background(), "alice@example.test")
	list, _, _ := e.st.ListMessages(context.Background(), store.MessageQuery{AccountID: a.ID, Limit: 10})
	local, _ := e.st.Blobs.Get(list[0].BlobHash)
	if strings.Contains(string(local), "ARC-Seal") {
		t.Fatal("local delivery must not be sealed")
	}
	if !strings.Contains(string(local), "arc=none") {
		t.Fatalf("authentication-results without arc:\n%.400s", local)
	}
}

func TestDMARCOverrideByTrustedARC(t *testing.T) {
	e := setup(t)
	s := e.svc.Settings.Get()
	s.Spam.Enabled = false
	s.MailAuth.TrustedARCSealers = []string{"google.com"}
	if err := e.svc.Settings.Save(context.Background(), s); err != nil {
		t.Fatal(err)
	}
	chain := func(sealer, aar string) mailauth.ARCResult {
		return mailauth.ARCResult{Result: "pass", Sets: []mailauth.ARCSet{{Instance: 1, SealDomain: sealer, CV: "none", AuthResults: aar}}}
	}
	base := mailauth.Result{SPF: "fail", DKIM: "fail", DMARC: "fail", DMARCPolicy: "reject", ARC: "pass"}

	// Trusted sealer that saw a passing DMARC: delivered to the inbox.
	r := base
	r.ARCChain = chain("google.com", "mx.google.com; dkim=pass header.d=bank.test; dmarc=pass header.from=bank.test")
	e.svc.AuthCheck = fakeAuth(r)
	if err := smtp.SendMail(e.smtp, nil, "list@lists.test", []string{"alice@example.test"}, mail("service@bank.test", "alice@example.test", "Weitergeleitet")); err != nil {
		t.Fatalf("trusted arc: %v", err)
	}
	if n := mailboxCount(t, e.st, "alice@example.test", "inbox"); n != 1 {
		t.Fatalf("inbox = %d", n)
	}
	a, _ := e.st.GetAccountByEmail(context.Background(), "alice@example.test")
	list, _, _ := e.st.ListMessages(context.Background(), store.MessageQuery{AccountID: a.ID, Limit: 10})
	raw, _ := e.st.Blobs.Get(list[0].BlobHash)
	if !strings.Contains(string(raw), "dmarc=fail (policy overridden: arc=pass, sealed by google.com)") {
		t.Fatalf("override not recorded:\n%.500s", raw)
	}

	// Trusted sealer without passing results: reject becomes quarantine.
	r.ARCChain = chain("google.com", "mx.google.com; spf=fail smtp.mailfrom=bank.test; dmarc=fail header.from=bank.test")
	e.svc.AuthCheck = fakeAuth(r)
	if err := smtp.SendMail(e.smtp, nil, "list@lists.test", []string{"alice@example.test"}, mail("service@bank.test", "alice@example.test", "Herabgestuft")); err != nil {
		t.Fatalf("downgrade: %v", err)
	}
	if n := mailboxCount(t, e.st, "alice@example.test", "junk"); n != 1 {
		t.Fatalf("junk = %d", n)
	}

	// Untrusted sealer: still rejected.
	r.ARCChain = chain("random.test", "random.test; dmarc=pass header.from=bank.test")
	e.svc.AuthCheck = fakeAuth(r)
	err := smtp.SendMail(e.smtp, nil, "list@lists.test", []string{"alice@example.test"}, mail("service@bank.test", "alice@example.test", "Abgelehnt"))
	expectCode(t, err, "550")
}

func TestPostmasterAndAbuseDelivered(t *testing.T) {
	e := setup(t)
	e.svc.AuthCheck = fakeAuth(mailauth.Result{SPF: "none", DKIM: "none", DMARC: "none"})
	if err := e.st.CreateAccount(context.Background(), &store.Account{Email: "admin@example.test", PasswordHash: "x", Role: "admin"}); err != nil {
		t.Fatal(err)
	}
	for _, rcpt := range []string{"postmaster@example.test", "abuse@example.test"} {
		if err := smtp.SendMail(e.smtp, nil, "someone@sender.test", []string{rcpt}, mail("someone@sender.test", rcpt, "An "+rcpt)); err != nil {
			t.Fatalf("%s: %v", rcpt, err)
		}
	}
	if n := inboxCount(t, e.st, "admin@example.test"); n != 2 {
		t.Fatalf("admin inbox = %d", n)
	}
	// Null sender (bounces) to postmaster must be accepted as well.
	if err := smtp.SendMail(e.smtp, nil, "", []string{"postmaster@example.test"}, mail("mailer-daemon@sender.test", "postmaster@example.test", "Bounce")); err != nil {
		t.Fatalf("null sender: %v", err)
	}
}

// reportMail builds a DMARC aggregate report mail with doc as zipped attachment.
func reportMail(doc []byte, to string) []byte {
	var zb bytes.Buffer
	zw := zip.NewWriter(&zb)
	f, _ := zw.Create("google.com!example.test!1763596800!1763683199.xml")
	f.Write(doc)
	zw.Close()
	enc := base64.StdEncoding.EncodeToString(zb.Bytes())
	var body strings.Builder
	for len(enc) > 76 {
		body.WriteString(enc[:76] + "\r\n")
		enc = enc[76:]
	}
	body.WriteString(enc + "\r\n")
	return []byte("From: noreply-dmarc-support@google.com\r\nTo: " + to + "\r\n" +
		"Subject: Report domain: example.test Submitter: google.com Report-ID: 12345678901234567890\r\n" +
		"Message-ID: <report-1@google.com>\r\nDate: Fri, 20 Nov 2026 10:00:00 +0000\r\nMIME-Version: 1.0\r\n" +
		"Content-Type: multipart/mixed; boundary=\"zz\"\r\n\r\n--zz\r\nContent-Type: text/plain\r\n\r\nReport\r\n" +
		"--zz\r\nContent-Type: application/zip; name=\"r.zip\"\r\nContent-Disposition: attachment; filename=\"r.zip\"\r\nContent-Transfer-Encoding: base64\r\n\r\n" +
		body.String() + "--zz--\r\n")
}

func setupReports(t *testing.T) (*env, []byte) {
	t.Helper()
	e := setup(t)
	e.svc.AuthCheck = fakeAuth(mailauth.Result{SPF: "pass", DKIM: "pass", DKIMDomains: []string{"google.com"}, DMARC: "pass"})
	if err := e.st.CreateAccount(context.Background(), &store.Account{Email: "admin@example.test", PasswordHash: "x", Role: "admin"}); err != nil {
		t.Fatal(err)
	}
	doc, err := os.ReadFile("../reports/testdata/dmarc_aggregate.xml")
	if err != nil {
		t.Fatal(err)
	}
	return e, doc
}

func reportCount(t *testing.T, st *store.Store) int {
	t.Helper()
	sum, err := st.DMARCSummary(context.Background(), store.ReportFilter{Since: time.Unix(1763596800, 0).Add(-time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, d := range sum {
		n += int(d.Reports)
	}
	return n
}

func TestReportDeliverySkipped(t *testing.T) {
	e, doc := setupReports(t)
	s := e.svc.Settings.Get()
	s.MailAuth.SkipReportDelivery = true
	if err := e.svc.Settings.Save(context.Background(), s); err != nil {
		t.Fatal(err)
	}
	send := func(from string, rcpts []string, msg []byte) {
		t.Helper()
		if err := smtp.SendMail(e.smtp, nil, from, rcpts, msg); err != nil {
			t.Fatal(err)
		}
	}

	// A fully evaluated report to postmaster@ is stored but not delivered.
	send("noreply-dmarc-support@google.com", []string{"postmaster@example.test"}, reportMail(doc, "postmaster@example.test"))
	if n := inboxCount(t, e.st, "admin@example.test"); n != 0 {
		t.Fatalf("admin inbox = %d, want 0", n)
	}
	if n := reportCount(t, e.st); n != 1 {
		t.Fatalf("reports = %d, want 1", n)
	}

	// A resent report is not stored twice and not delivered either.
	send("noreply-dmarc-support@google.com", []string{"postmaster@example.test"}, reportMail(doc, "postmaster@example.test"))
	if n := inboxCount(t, e.st, "admin@example.test"); n != 0 {
		t.Fatalf("admin inbox after resend = %d, want 0", n)
	}
	if n := reportCount(t, e.st); n != 1 {
		t.Fatalf("reports after resend = %d, want 1", n)
	}

	// Other recipients of the same message still get it.
	send("noreply-dmarc-support@google.com", []string{"postmaster@example.test", "alice@example.test"}, reportMail(doc, "postmaster@example.test"))
	if n := inboxCount(t, e.st, "alice@example.test"); n != 1 {
		t.Fatalf("alice inbox = %d, want 1", n)
	}
	if n := inboxCount(t, e.st, "admin@example.test"); n != 0 {
		t.Fatalf("admin inbox with second recipient = %d, want 0", n)
	}

	// A report that cannot be parsed is delivered so nothing gets lost.
	send("noreply-dmarc-support@google.com", []string{"postmaster@example.test"}, reportMail([]byte("<feedback><kaputt"), "postmaster@example.test"))
	if n := inboxCount(t, e.st, "admin@example.test"); n != 1 {
		t.Fatalf("admin inbox after broken report = %d, want 1", n)
	}

	// A report about a foreign domain is delivered as well.
	foreign := bytes.ReplaceAll(doc, []byte("example.test"), []byte("fremd.test"))
	send("noreply-dmarc-support@google.com", []string{"postmaster@example.test"}, reportMail(foreign, "postmaster@example.test"))
	if n := inboxCount(t, e.st, "admin@example.test"); n != 2 {
		t.Fatalf("admin inbox after foreign report = %d, want 2", n)
	}

	// Ordinary mail to postmaster@ is unaffected.
	send("someone@sender.test", []string{"postmaster@example.test"}, mail("someone@sender.test", "postmaster@example.test", "Frage"))
	if n := inboxCount(t, e.st, "admin@example.test"); n != 3 {
		t.Fatalf("admin inbox after ordinary mail = %d, want 3", n)
	}
}

func TestDMARCReportIngested(t *testing.T) {
	e, doc := setupReports(t)
	if err := smtp.SendMail(e.smtp, nil, "noreply-dmarc-support@google.com", []string{"postmaster@example.test"}, reportMail(doc, "postmaster@example.test")); err != nil {
		t.Fatal(err)
	}
	// The report mail is delivered normally ...
	if n := inboxCount(t, e.st, "admin@example.test"); n != 1 {
		t.Fatalf("admin inbox = %d", n)
	}
	// ... and the report is stored.
	since := time.Unix(1763596800, 0).Add(-time.Hour)
	sum, err := e.st.DMARCSummary(context.Background(), store.ReportFilter{Since: since})
	if err != nil || len(sum) != 1 || sum[0].Domain != "example.test" || sum[0].Messages != 50 || sum[0].Failed != 3 {
		t.Fatalf("summary: %+v %v", sum, err)
	}
}

func TestDualSignedSubmission(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	key, _ := mailauth.GenerateKeyFor(mailauth.AlgEd25519)
	if _, err := e.st.AddDKIMKey(ctx, "example.test", "pw1e", store.DKIMAlgEd25519, key, store.DKIMActive); err != nil {
		t.Fatal(err)
	}
	alice, _ := e.st.GetAccountByEmail(ctx, "alice@example.test")
	data := []byte("From: alice@example.test\r\nTo: x@remote.test\r\nSubject: Doppelt\r\n\r\nText\r\n")
	if _, err := e.svc.Submit(ctx, alice, "alice@example.test", []string{"x@remote.test"}, data, false); err != nil {
		t.Fatal(err)
	}
	blobs := queuedBlobs(t, e.st)
	if len(blobs) != 1 {
		t.Fatalf("queue: %d", len(blobs))
	}
	if n := strings.Count(blobs[0], "DKIM-Signature:"); n != 2 {
		t.Fatalf("signatures = %d", n)
	}
	if !strings.Contains(blobs[0], "a=ed25519-sha256") || !strings.Contains(blobs[0], "s=pw1e") || !strings.Contains(blobs[0], "s=pw1;") {
		t.Fatalf("expected rsa and ed25519 signatures:\n%.800s", blobs[0])
	}
}
