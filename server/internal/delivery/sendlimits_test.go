package delivery_test

import (
	"context"
	"errors"
	"fmt"
	"net/smtp"
	"strings"
	"testing"
	"time"

	"postwerk/internal/delivery"
	"postwerk/internal/store"
)

func setLimits(t *testing.T, e *env, l store.SendLimitSettings) {
	t.Helper()
	s := e.svc.Settings.Get()
	s.Limits = l
	if err := e.svc.Settings.Save(context.Background(), s); err != nil {
		t.Fatal(err)
	}
}

func account(t *testing.T, st *store.Store, email string) *store.Account {
	t.Helper()
	a, err := st.GetAccountByEmail(context.Background(), email)
	if err != nil {
		t.Fatal(err)
	}
	return a
}

func submit(e *env, acct *store.Account, rcpts ...string) error {
	data := fmt.Sprintf("From: %s\r\nTo: %s\r\nSubject: Limit\r\n\r\nHallo\r\n", acct.Email, strings.Join(rcpts, ", "))
	_, err := e.svc.Submit(context.Background(), acct, acct.Email, rcpts, []byte(data), false)
	return err
}

func limitKind(err error) string {
	var le *delivery.SendLimitError
	if errors.As(err, &le) {
		return le.Kind
	}
	return ""
}

func TestSendLimitRecipientsPerMessageSMTP(t *testing.T) {
	e := setup(t)
	setLimits(t, e, store.SendLimitSettings{MaxRecipientsPerMessage: 2, MaxMessagesPerHour: 100, MaxRecipientsPerDay: 100})
	c, err := smtp.Dial(e.sub)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if err := c.Hello("client.test"); err != nil {
		t.Fatal(err)
	}
	if err := c.Auth(smtp.PlainAuth("", "alice@example.test", e.userPw, "127.0.0.1")); err != nil {
		if strings.Contains(err.Error(), "unencrypted") {
			t.Skip("plain auth over unencrypted connection rejected by client")
		}
		t.Fatalf("auth: %v", err)
	}
	if err := c.Mail("alice@example.test"); err != nil {
		t.Fatal(err)
	}
	if err := c.Rcpt("bob@example.test"); err != nil {
		t.Fatal(err)
	}
	if err := c.Rcpt("one@remote.test"); err != nil {
		t.Fatal(err)
	}
	err = c.Rcpt("two@remote.test")
	if err == nil || !strings.Contains(err.Error(), "452") || !strings.Contains(err.Error(), "4.5.3") {
		t.Fatalf("third recipient: want 452 4.5.3, got %v", err)
	}
	// The accepted recipients still get the message.
	w, err := c.Data()
	if err != nil {
		t.Fatal(err)
	}
	fmt.Fprintf(w, "From: alice@example.test\r\nTo: bob@example.test\r\nSubject: Zwei\r\n\r\nTest\r\n")
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	c.Quit()
	if n := inboxCount(t, e.st, "bob@example.test"); n != 1 {
		t.Fatalf("bob inbox = %d, want 1", n)
	}
	hits, _ := e.st.SendLimitHitsSince(context.Background(), time.Now().Add(-time.Hour))
	if len(hits) != 1 || hits[0].Email != "alice@example.test" || hits[0].LastKind != store.SendLimitRecipients {
		t.Fatalf("hit not recorded: %+v", hits)
	}
}

func TestSendLimitHourlyOverSMTP(t *testing.T) {
	e := setup(t)
	setLimits(t, e, store.SendLimitSettings{MaxRecipientsPerMessage: 10, MaxMessagesPerHour: 1, MaxRecipientsPerDay: 100})
	alice := account(t, e.st, "alice@example.test")
	if err := submit(e, alice, "x@remote.test"); err != nil {
		t.Fatal(err)
	}
	c, err := smtp.Dial(e.sub)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	_ = c.Hello("client.test")
	if err := c.Auth(smtp.PlainAuth("", "alice@example.test", e.userPw, "127.0.0.1")); err != nil {
		t.Skip("auth not possible: ", err)
	}
	if err := c.Mail("alice@example.test"); err != nil {
		t.Fatal(err)
	}
	// Local recipients are still accepted, remote ones are deferred.
	if err := c.Rcpt("bob@example.test"); err != nil {
		t.Fatalf("local recipient refused: %v", err)
	}
	if err := c.Rcpt("y@remote.test"); err == nil || !strings.Contains(err.Error(), "450") || !strings.Contains(err.Error(), "4.7.1") {
		t.Fatalf("remote recipient over hourly limit: want 450 4.7.1, got %v", err)
	}
}

func TestSendLimitHourlyWindow(t *testing.T) {
	e := setup(t)
	setLimits(t, e, store.SendLimitSettings{MaxRecipientsPerMessage: 10, MaxMessagesPerHour: 2, MaxRecipientsPerDay: 100})
	alice := account(t, e.st, "alice@example.test")
	for i := 0; i < 2; i++ {
		if err := submit(e, alice, fmt.Sprintf("r%d@remote.test", i)); err != nil {
			t.Fatalf("message %d: %v", i, err)
		}
	}
	err := submit(e, alice, "r3@remote.test")
	if limitKind(err) != store.SendLimitHourly {
		t.Fatalf("third message: want hourly limit, got %v", err)
	}
	var le *delivery.SendLimitError
	errors.As(err, &le)
	if le.Reply.Code != 450 || !strings.Contains(le.Message(), "2 Nachrichten pro Stunde") {
		t.Fatalf("unexpected reply %v / %q", le.Reply, le.Message())
	}
	if items := queued(t, e.st); len(items) != 2 {
		t.Fatalf("queue = %d, want 2", len(items))
	}

	// Sliding window: an hour later there is room again (store level, with
	// explicit times); the limit survives a restart because it lives in SQLite.
	lim := store.SendLimits{MaxMessagesPerHour: 2, MaxRecipientsPerDay: 100}
	ctx := context.Background()
	if err := e.st.ReserveSend(ctx, alice.ID, 1, lim, time.Now()); err == nil {
		t.Fatal("window should still be full")
	}
	later := time.Now().Add(61 * time.Minute)
	if err := e.st.ReserveSend(ctx, alice.ID, 1, lim, later); err != nil {
		t.Fatalf("window did not slide: %v", err)
	}
	u, _ := e.st.SendUsage(ctx, alice.ID, later)
	if u.MessagesLastHour != 1 || u.MessagesLastDay != 3 || u.RecipientsLastDay != 3 {
		t.Fatalf("usage = %+v", u)
	}
	// Pruning removes entries older than 24 hours.
	if err := e.st.PruneSendLog(ctx, time.Now().Add(25*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if u, _ := e.st.SendUsage(ctx, alice.ID, later); u.MessagesLastDay != 1 {
		t.Fatalf("prune left %+v", u)
	}
}

func TestSendLimitCountsOnlyRemoteVolume(t *testing.T) {
	e := setup(t)
	setLimits(t, e, store.SendLimitSettings{MaxRecipientsPerMessage: 3, MaxMessagesPerHour: 100, MaxRecipientsPerDay: 2})
	alice := account(t, e.st, "alice@example.test")
	ctx := context.Background()
	// Local recipients never count against the daily volume.
	for i := 0; i < 5; i++ {
		if err := submit(e, alice, "bob@example.test"); err != nil {
			t.Fatalf("local message %d: %v", i, err)
		}
	}
	if u, _ := e.st.SendUsage(ctx, alice.ID, time.Now()); u.MessagesLastDay != 0 || u.RecipientsLastDay != 0 {
		t.Fatalf("local mail counted: %+v", u)
	}
	// Mixed message: one local, two remote recipients.
	if err := submit(e, alice, "bob@example.test", "a@remote.test", "b@remote.test"); err != nil {
		t.Fatal(err)
	}
	if u, _ := e.st.SendUsage(ctx, alice.ID, time.Now()); u.RecipientsLastDay != 2 || u.MessagesLastHour != 1 {
		t.Fatalf("usage = %+v", u)
	}
	if err := submit(e, alice, "c@remote.test"); limitKind(err) != store.SendLimitDaily {
		t.Fatalf("want daily limit, got %v", err)
	}
	// Nothing was delivered for the refused message, but local mail still works.
	if err := submit(e, alice, "bob@example.test"); err != nil {
		t.Fatalf("local mail after daily limit: %v", err)
	}
	// Local recipients do count against the per-message maximum.
	if err := submit(e, alice, "bob@example.test", "alice@example.test", "team@example.test", "bob+x@example.test"); limitKind(err) != store.SendLimitRecipients {
		t.Fatalf("want recipient limit, got %v", err)
	}
	if n := inboxCount(t, e.st, "bob@example.test"); n != 7 {
		t.Fatalf("bob inbox = %d, want 7", n)
	}
}

func TestSendLimitOverride(t *testing.T) {
	e := setup(t)
	setLimits(t, e, store.SendLimitSettings{MaxRecipientsPerMessage: 10, MaxMessagesPerHour: 1, MaxRecipientsPerDay: 100})
	alice := account(t, e.st, "alice@example.test")
	bob := account(t, e.st, "bob@example.test")
	ctx := context.Background()
	three := 3
	if err := e.st.SetSendLimitOverride(ctx, alice.ID, store.SendLimitOverride{MaxMessagesPerHour: &three}); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		if err := submit(e, alice, fmt.Sprintf("a%d@remote.test", i)); err != nil {
			t.Fatalf("alice message %d: %v", i, err)
		}
	}
	if err := submit(e, alice, "a9@remote.test"); limitKind(err) != store.SendLimitHourly {
		t.Fatalf("alice: want hourly limit after 3, got %v", err)
	}
	// Bob keeps the default of one message per hour.
	if err := submit(e, bob, "b1@remote.test"); err != nil {
		t.Fatal(err)
	}
	if err := submit(e, bob, "b2@remote.test"); limitKind(err) != store.SendLimitHourly {
		t.Fatalf("bob: want hourly limit, got %v", err)
	}
	// Exempt accounts have no limits at all.
	if err := e.st.SetSendLimitOverride(ctx, bob.ID, store.SendLimitOverride{Exempt: true}); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 5; i++ {
		if err := submit(e, bob, fmt.Sprintf("x%d@remote.test", i)); err != nil {
			t.Fatalf("exempt bob message %d: %v", i, err)
		}
	}
	// A zero override is removed again.
	if err := e.st.SetSendLimitOverride(ctx, bob.ID, store.SendLimitOverride{}); err != nil {
		t.Fatal(err)
	}
	if o, err := e.st.GetSendLimitOverride(ctx, bob.ID); err != nil || o != nil {
		t.Fatalf("override not removed: %+v %v", o, err)
	}
}

func TestSendLimitHitCallback(t *testing.T) {
	e := setup(t)
	setLimits(t, e, store.SendLimitSettings{MaxRecipientsPerMessage: 1, MaxMessagesPerHour: 10, MaxRecipientsPerDay: 10})
	var calls []string
	e.svc.OnSendLimit = func(a *store.Account, kind string, limit int) { calls = append(calls, a.Email+":"+kind) }
	alice := account(t, e.st, "alice@example.test")
	for i := 0; i < 3; i++ {
		_ = submit(e, alice, "a@remote.test", "b@remote.test")
	}
	// Repeated hits within a minute are reported once.
	if len(calls) != 1 || calls[0] != "alice@example.test:recipients" {
		t.Fatalf("callbacks = %v", calls)
	}
}

func TestSieveRedirectCountsAgainstSendLimit(t *testing.T) {
	e := sieveSetup(t)
	setLimits(t, e, store.SendLimitSettings{MaxRecipientsPerMessage: 10, MaxMessagesPerHour: 1, MaxRecipientsPerDay: 100})
	activate(t, e.st, "alice@example.test", `redirect "ziel@other.test";`)
	for i := 0; i < 2; i++ {
		if err := send(t, e, "orig@sender.test", []string{"alice@example.test"}, mail("orig@sender.test", "alice@example.test", fmt.Sprintf("Nr %d", i))); err != nil {
			t.Fatal(err)
		}
	}
	if items := queued(t, e.st); len(items) != 1 {
		t.Fatalf("queue = %d, want 1 redirect", len(items))
	}
	// The second message is kept instead of being lost.
	if n := inboxCount(t, e.st, "alice@example.test"); n != 1 {
		t.Fatalf("alice inbox = %d, want 1", n)
	}
	alice := account(t, e.st, "alice@example.test")
	if u, _ := e.st.SendUsage(context.Background(), alice.ID, time.Now()); u.MessagesLastHour != 1 {
		t.Fatalf("redirect not counted: %+v", u)
	}
}
