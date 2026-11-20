package imapd

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"

	"postwerk/internal/auth"
	"postwerk/internal/delivery"
	"postwerk/internal/message"
	"postwerk/internal/store"
)

type fixture struct {
	st   *store.Store
	svc  *delivery.Service
	acct *store.Account
	addr string
}

func setup(t *testing.T) *fixture {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	hash, _ := auth.HashPassword("geheim12345")
	acct := &store.Account{Email: "anna@example.test", PasswordHash: hash, Role: "user"}
	if err := st.CreateAccount(ctx, acct); err != nil {
		t.Fatal(err)
	}
	svc := &delivery.Service{Store: st}
	l, _ := net.Listen("tcp", "127.0.0.1:0")
	addr := l.Addr().String()
	l.Close()
	srv := &Server{
		Addr: addr, Store: st, Delivery: svc, AllowInsecureAuth: true,
		Auth: func(ctx context.Context, ip, user, pass string) (*store.Account, error) {
			a, err := st.GetAccountByEmail(ctx, user)
			if err != nil || !auth.CheckPassword(a.PasswordHash, pass) {
				return nil, errors.New("invalid")
			}
			return a, nil
		},
		MaxSize: func() int64 { return 10 << 20 },
		Log:     slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
	go srv.ListenAndServe(ctx)
	for i := 0; i < 50; i++ {
		if c, err := net.Dial("tcp", addr); err == nil {
			c.Close()
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	return &fixture{st: st, svc: svc, acct: acct, addr: addr}
}

func (f *fixture) deliver(t *testing.T, subject, body string) {
	t.Helper()
	inbox, _ := f.st.MailboxByRole(context.Background(), f.acct.ID, "inbox")
	raw := message.Build(message.Draft{From: store.Address{Name: "Bernd", Email: "bernd@example.test"}, To: []store.Address{{Email: f.acct.Email}},
		Subject: subject, Text: body, MessageID: strings.ReplaceAll(subject, " ", "") + "@example.test"})
	if _, err := f.svc.ImportMessage(context.Background(), f.acct, inbox, raw, delivery.ImportOptions{SkipAI: true}); err != nil {
		t.Fatal(err)
	}
}

func (f *fixture) dial(t *testing.T, opts *imapclient.Options) *imapclient.Client {
	t.Helper()
	c, err := imapclient.DialInsecure(f.addr, opts)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })
	if err := c.Login(f.acct.Email, "geheim12345").Wait(); err != nil {
		t.Fatal(err)
	}
	return c
}

func TestLoginRejectsWrongPassword(t *testing.T) {
	f := setup(t)
	c, err := imapclient.DialInsecure(f.addr, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if err := c.Login(f.acct.Email, "falsch").Wait(); err == nil {
		t.Fatal("login with wrong password must fail")
	}
}

func TestMailboxesAndFetch(t *testing.T) {
	f := setup(t)
	f.deliver(t, "Hallo Anna", "Erste Nachricht")
	f.deliver(t, "Rechnung 42", "Bitte bezahlen")
	c := f.dial(t, nil)

	list, err := c.List("", "*", &imap.ListOptions{ReturnStatus: &imap.StatusOptions{NumMessages: true}}).Collect()
	if err != nil {
		t.Fatal(err)
	}
	attrs := map[string][]imap.MailboxAttr{}
	for _, l := range list {
		attrs[l.Mailbox] = l.Attrs
	}
	if _, ok := attrs["INBOX"]; !ok {
		t.Fatalf("INBOX missing: %v", attrs)
	}
	if !hasAttr(attrs["Gesendet"], imap.MailboxAttrSent) || !hasAttr(attrs["Papierkorb"], imap.MailboxAttrTrash) || !hasAttr(attrs["Entwürfe"], imap.MailboxAttrDrafts) {
		t.Errorf("special-use attributes missing: %v", attrs)
	}

	sel, err := c.Select("INBOX", nil).Wait()
	if err != nil || sel.NumMessages != 2 || sel.UIDValidity == 0 {
		t.Fatalf("select = %+v, %v", sel, err)
	}
	section := &imap.FetchItemBodySection{}
	msgs, err := c.Fetch(imap.SeqSetNum(1, 2), &imap.FetchOptions{Envelope: true, Flags: true, UID: true, RFC822Size: true,
		BodyStructure: &imap.FetchItemBodyStructure{}, BodySection: []*imap.FetchItemBodySection{section}}).Collect()
	if err != nil || len(msgs) != 2 {
		t.Fatalf("fetch = %v, %v", len(msgs), err)
	}
	if msgs[0].Envelope.Subject != "Hallo Anna" || !strings.Contains(string(msgs[0].FindBodySection(section)), "Erste Nachricht") {
		t.Errorf("message 1 = %q", msgs[0].Envelope.Subject)
	}
	// BODY[] without PEEK marks the message as seen.
	st, _ := c.Status("INBOX", &imap.StatusOptions{NumUnseen: true}).Wait()
	if *st.NumUnseen != 0 {
		t.Errorf("unseen after fetch = %d", *st.NumUnseen)
	}

	// Flags round-trip, including custom keywords.
	if _, err := c.Store(imap.SeqSetNum(2), &imap.StoreFlags{Op: imap.StoreFlagsAdd, Flags: []imap.Flag{imap.FlagFlagged, "$Forwarded"}}, nil).Collect(); err != nil {
		t.Fatal(err)
	}
	got, _ := c.Fetch(imap.SeqSetNum(2), &imap.FetchOptions{Flags: true}).Collect()
	if !hasFlag(flagsFromIMAP(got[0].Flags), imap.FlagFlagged) || !hasFlag(flagsFromIMAP(got[0].Flags), "$Forwarded") {
		t.Errorf("flags = %v", got[0].Flags)
	}

	// Search.
	res, err := c.UIDSearch(&imap.SearchCriteria{Header: []imap.SearchCriteriaHeaderField{{Key: "Subject", Value: "rechnung"}}}, nil).Wait()
	if err != nil || len(res.AllUIDs()) != 1 {
		t.Errorf("search = %v, %v", res, err)
	}
}

func TestAppendMoveExpunge(t *testing.T) {
	f := setup(t)
	f.deliver(t, "Eins", "a")
	f.deliver(t, "Zwei", "b")
	c := f.dial(t, nil)

	// APPEND to Sent, twice: the duplicate is detected.
	raw := message.Build(message.Draft{From: store.Address{Email: f.acct.Email}, To: []store.Address{{Email: "x@example.test"}}, Subject: "Gesendet", Text: "hi", MessageID: "sent1@example.test"})
	for i := 0; i < 2; i++ {
		cmd := c.Append("Gesendet", int64(len(raw)), &imap.AppendOptions{Flags: []imap.Flag{imap.FlagSeen}})
		cmd.Write(raw)
		cmd.Close()
		if _, err := cmd.Wait(); err != nil {
			t.Fatal(err)
		}
	}
	st, _ := c.Status("Gesendet", &imap.StatusOptions{NumMessages: true}).Wait()
	if *st.NumMessages != 1 {
		t.Errorf("sent messages = %d, want 1", *st.NumMessages)
	}

	if _, err := c.Select("INBOX", nil).Wait(); err != nil {
		t.Fatal(err)
	}
	// MOVE message 1 to the archive.
	mv, err := c.Move(imap.SeqSetNum(1), "Archiv").Wait()
	if err != nil || mv == nil || mv.DestUIDs == nil {
		t.Fatalf("move = %+v, %v", mv, err)
	}
	st, _ = c.Status("Archiv", &imap.StatusOptions{NumMessages: true}).Wait()
	if *st.NumMessages != 1 {
		t.Errorf("archive = %d", *st.NumMessages)
	}
	// \Deleted + EXPUNGE removes the remaining message.
	if _, err := c.Store(imap.SeqSetNum(1), &imap.StoreFlags{Op: imap.StoreFlagsAdd, Silent: true, Flags: []imap.Flag{imap.FlagDeleted}}, nil).Collect(); err != nil {
		t.Fatal(err)
	}
	exp, err := c.Expunge().Collect()
	if err != nil || len(exp) != 1 {
		t.Fatalf("expunge = %v, %v", exp, err)
	}
	st, _ = c.Status("INBOX", &imap.StatusOptions{NumMessages: true}).Wait()
	if *st.NumMessages != 0 {
		t.Errorf("inbox after expunge = %d", *st.NumMessages)
	}

	// Folder management.
	if err := c.Create("Projekte/Alpha", nil).Wait(); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Select("Projekte/Alpha", nil).Wait(); err != nil {
		t.Fatal(err)
	}
}

func TestIdlePushesNewMail(t *testing.T) {
	f := setup(t)
	var exists atomic.Uint32
	got := make(chan struct{}, 4)
	c := f.dial(t, &imapclient.Options{UnilateralDataHandler: &imapclient.UnilateralDataHandler{
		Mailbox: func(d *imapclient.UnilateralDataMailbox) {
			if d.NumMessages != nil {
				exists.Store(*d.NumMessages)
				got <- struct{}{}
			}
		},
	}})
	if _, err := c.Select("INBOX", nil).Wait(); err != nil {
		t.Fatal(err)
	}
	idle, err := c.Idle()
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(100 * time.Millisecond)
	f.deliver(t, "Neu", "frisch")
	select {
	case <-got:
	case <-time.After(5 * time.Second):
		t.Fatal("no EXISTS during IDLE")
	}
	if exists.Load() != 1 {
		t.Errorf("EXISTS = %d", exists.Load())
	}
	if err := idle.Close(); err != nil {
		t.Fatal(err)
	}
	if err := idle.Wait(); err != nil {
		t.Fatal(err)
	}
}

func hasAttr(l []imap.MailboxAttr, a imap.MailboxAttr) bool {
	for _, x := range l {
		if x == a {
			return true
		}
	}
	return false
}
