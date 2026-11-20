package delivery_test

import (
	"bufio"
	"context"
	"fmt"
	"log/slog"
	"net"
	"net/smtp"
	"os"
	"strings"
	"testing"
	"time"

	"postwerk/internal/auth"
	"postwerk/internal/delivery"
	"postwerk/internal/mailauth"
	"postwerk/internal/pop3"
	smtpd "postwerk/internal/smtp"
	"postwerk/internal/spam"
	"postwerk/internal/store"
)

type noopQueue struct{ n int }

func (q *noopQueue) Notify() { q.n++ }

type env struct {
	st     *store.Store
	svc    *delivery.Service
	smtp   string
	sub    string
	pop    string
	queue  *noopQueue
	userPw string
	cancel context.CancelFunc
}

func freeAddr(t *testing.T) string {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	a := l.Addr().String()
	l.Close()
	return a
}

func setup(t *testing.T) *env {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	settings, err := st.LoadSettings(ctx, "mail.example.test")
	if err != nil {
		t.Fatal(err)
	}
	s := settings.Get()
	s.Spam.DNSBL.Enabled = false
	if err := settings.Save(ctx, s); err != nil {
		t.Fatal(err)
	}
	key, _ := mailauth.GenerateKey()
	if err := st.CreateDomain(ctx, "example.test", "pw1", key); err != nil {
		t.Fatal(err)
	}
	h, _ := auth.HashPassword("geheim-passwort")
	for _, e := range []string{"alice@example.test", "bob@example.test"} {
		if err := st.CreateAccount(ctx, &store.Account{Email: e, Name: strings.Split(e, "@")[0], PasswordHash: h, Role: "user"}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := st.CreateAlias(ctx, "team@example.test", []string{"alice@example.test", "bob@example.test", "extern@other.test"}); err != nil {
		t.Fatal(err)
	}
	log := slog.New(slog.NewTextHandler(os.Stderr, nil))
	guard := auth.NewGuard(func() auth.Limits { return auth.Limits{MaxAttempts: 3, Window: time.Minute, BanFor: time.Minute} })
	authn := &auth.Authenticator{Store: st, Guard: guard}
	q := &noopQueue{}
	svc := &delivery.Service{Store: st, Settings: settings, Auth: authn, Spam: &spam.Filter{Store: st, Settings: settings}, Queue: q, Log: log}
	e := &env{st: st, svc: svc, smtp: freeAddr(t), sub: freeAddr(t), pop: freeAddr(t), queue: q, userPw: "geheim-passwort", cancel: cancel}
	host := func() string { return "mail.example.test" }
	size := func() int64 { return 10 << 20 }
	go (&smtpd.Server{Addr: e.smtp, Mode: smtpd.ModeInbound, Hostname: host, MaxSize: size, Backend: svc, Log: log}).ListenAndServe(ctx)
	go (&smtpd.Server{Addr: e.sub, Mode: smtpd.ModeSubmission, Hostname: host, MaxSize: size, Backend: svc, Log: log, AllowInsecureAuth: true}).ListenAndServe(ctx)
	go (&pop3.Server{Addr: e.pop, Store: st, Banned: guard.Banned, Hostname: host, Log: log, AllowInsecureAuth: true,
		Auth: func(ctx context.Context, ip, u, p string) (*store.Account, error) {
			return authn.Protocol(ctx, ip, u, p, "pop3")
		}}).ListenAndServe(ctx)
	time.Sleep(100 * time.Millisecond)
	t.Cleanup(func() { cancel(); st.Close() })
	return e
}

func inboxCount(t *testing.T, st *store.Store, email string) int {
	a, err := st.GetAccountByEmail(context.Background(), email)
	if err != nil {
		t.Fatal(err)
	}
	mb, _ := st.MailboxByRole(context.Background(), a.ID, "inbox")
	msgs, err := st.ListMailboxMessages(context.Background(), mb)
	if err != nil {
		t.Fatal(err)
	}
	return len(msgs)
}

const testMail = "From: Sender <sender@sender.test>\r\nTo: team@example.test\r\nSubject: Hallo Team\r\nMessage-ID: <abc@sender.test>\r\nDate: Fri, 20 Nov 2026 10:00:00 +0100\r\n\r\nHallo zusammen,\r\n.punkt am Anfang\r\nGruss\r\n"

func TestInboundAliasDelivery(t *testing.T) {
	e := setup(t)
	if err := smtp.SendMail(e.smtp, nil, "sender@sender.test", []string{"team@example.test"}, []byte(testMail)); err != nil {
		t.Fatalf("send: %v", err)
	}
	if n := inboxCount(t, e.st, "alice@example.test"); n != 1 {
		t.Fatalf("alice inbox = %d, want 1", n)
	}
	if n := inboxCount(t, e.st, "bob@example.test"); n != 1 {
		t.Fatalf("bob inbox = %d, want 1", n)
	}
	items, _ := e.st.ListQueue(context.Background())
	if len(items) != 1 || items[0].To[0] != "extern@other.test" {
		t.Fatalf("expected external forward in queue, got %+v", items)
	}
	// Message stored with dot-unstuffing and metadata.
	a, _ := e.st.GetAccountByEmail(context.Background(), "alice@example.test")
	list, _, _ := e.st.ListMessages(context.Background(), store.MessageQuery{AccountID: a.ID, Limit: 10})
	if list[0].Subject != "Hallo Team" || list[0].From.Email != "sender@sender.test" {
		t.Fatalf("unexpected metadata %+v", list[0])
	}
	raw, _ := e.st.Blobs.Get(list[0].BlobHash)
	if !strings.Contains(string(raw), "\r\n.punkt am Anfang") || !strings.HasPrefix(string(raw), "Received:") {
		t.Fatalf("raw message not stored correctly:\n%s", raw)
	}
}

func TestRelayDeniedAndUnknownUser(t *testing.T) {
	e := setup(t)
	err := smtp.SendMail(e.smtp, nil, "x@sender.test", []string{"someone@elsewhere.test"}, []byte(testMail))
	if err == nil || !strings.Contains(err.Error(), "554") {
		t.Fatalf("expected relay denied, got %v", err)
	}
	err = smtp.SendMail(e.smtp, nil, "x@sender.test", []string{"nobody@example.test"}, []byte(testMail))
	if err == nil || !strings.Contains(err.Error(), "550") {
		t.Fatalf("expected unknown user, got %v", err)
	}
}

func TestCatchAll(t *testing.T) {
	e := setup(t)
	target := "bob@example.test"
	if err := e.st.SetCatchAll(context.Background(), "example.test", &target); err != nil {
		t.Fatal(err)
	}
	if err := smtp.SendMail(e.smtp, nil, "x@sender.test", []string{"irgendwas@example.test"}, []byte(testMail)); err != nil {
		t.Fatalf("send: %v", err)
	}
	if n := inboxCount(t, e.st, "bob@example.test"); n != 1 {
		t.Fatalf("bob inbox = %d, want 1", n)
	}
}

func TestSubmissionRequiresAuthAndSigns(t *testing.T) {
	e := setup(t)
	err := smtp.SendMail(e.sub, nil, "alice@example.test", []string{"bob@example.test"}, []byte(testMail))
	if err == nil {
		t.Fatal("expected auth required")
	}
	c, err := smtp.Dial(e.sub)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if err := c.Hello("client.test"); err != nil {
		t.Fatal(err)
	}
	// net/smtp refuses PLAIN without TLS unless the host is localhost.
	if err := c.Auth(smtp.PlainAuth("", "alice@example.test", e.userPw, "127.0.0.1")); err != nil {
		if strings.Contains(err.Error(), "unencrypted") {
			t.Skip("plain auth over unencrypted connection rejected by client")
		}
		t.Fatalf("auth: %v", err)
	}
	if err := c.Mail("bob@example.test"); err == nil {
		t.Fatal("sending as another user must be rejected")
	}
	c.Reset()
	if err := c.Mail("alice@example.test"); err != nil {
		t.Fatal(err)
	}
	c.Rcpt("bob@example.test")
	c.Rcpt("someone@remote.test")
	w, _ := c.Data()
	fmt.Fprintf(w, "From: alice@example.test\r\nTo: bob@example.test\r\nSubject: Signiert\r\n\r\nTest\r\n")
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	c.Quit()
	bob, _ := e.st.GetAccountByEmail(context.Background(), "bob@example.test")
	list, _, _ := e.st.ListMessages(context.Background(), store.MessageQuery{AccountID: bob.ID, Limit: 10})
	if len(list) != 1 {
		t.Fatalf("bob got %d messages", len(list))
	}
	raw, _ := e.st.Blobs.Get(list[0].BlobHash)
	if !strings.Contains(string(raw), "DKIM-Signature:") || !strings.Contains(string(raw), "Message-ID:") {
		t.Fatalf("message not signed / missing Message-ID:\n%s", raw)
	}
	if items, _ := e.st.ListQueue(context.Background()); len(items) != 1 {
		t.Fatalf("remote recipient not queued: %d", len(items))
	}
}

func TestPOP3(t *testing.T) {
	e := setup(t)
	if err := smtp.SendMail(e.smtp, nil, "x@sender.test", []string{"alice@example.test"}, []byte(testMail)); err != nil {
		t.Fatal(err)
	}
	conn, err := net.Dial("tcp", e.pop)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	r := bufio.NewReader(conn)
	cmd := func(s string) string {
		if s != "" {
			fmt.Fprintf(conn, "%s\r\n", s)
		}
		l, _ := r.ReadString('\n')
		return l
	}
	cmd("")
	cmd("USER alice@example.test")
	if l := cmd("PASS " + e.userPw); !strings.HasPrefix(l, "+OK 1 messages") {
		t.Fatalf("login: %q", l)
	}
	if l := cmd("STAT"); !strings.HasPrefix(l, "+OK 1 ") {
		t.Fatalf("stat: %q", l)
	}
	fmt.Fprintf(conn, "RETR 1\r\n")
	var body strings.Builder
	for {
		l, err := r.ReadString('\n')
		if err != nil || l == ".\r\n" {
			break
		}
		body.WriteString(l)
	}
	if !strings.Contains(body.String(), "..punkt am Anfang") {
		t.Fatalf("dot stuffing missing in RETR:\n%s", body.String())
	}
	cmd("DELE 1")
	cmd("QUIT")
	time.Sleep(50 * time.Millisecond)
	if n := inboxCount(t, e.st, "alice@example.test"); n != 0 {
		t.Fatalf("message should have moved to trash, inbox has %d", n)
	}
}

func TestBruteForceBan(t *testing.T) {
	e := setup(t)
	for i := 0; i < 3; i++ {
		_, _ = e.svc.Auth.Protocol(context.Background(), "203.0.113.9", "alice@example.test", "falsch", "smtp")
	}
	if !e.svc.Auth.Guard.Banned("203.0.113.9") {
		t.Fatal("ip should be banned")
	}
	if _, err := e.svc.Auth.Protocol(context.Background(), "203.0.113.9", "alice@example.test", e.userPw, "smtp"); err != auth.ErrBanned {
		t.Fatalf("expected ErrBanned, got %v", err)
	}
}
