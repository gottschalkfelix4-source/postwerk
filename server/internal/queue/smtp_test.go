package queue

import (
	"bufio"
	"context"
	"crypto/tls"
	"io"
	"log/slog"
	"net"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"postwerk/internal/store"
)

// fakeSMTP is a minimal SMTP server for tests.
type fakeSMTP struct {
	ln       net.Listener
	tls      *tls.Config // nil: no STARTTLS
	rcptCode string      // reply to RCPT, default "250 ok"
	hold     time.Duration

	mu        sync.Mutex
	mailFrom  []string
	delivered int
	cur, peak atomic.Int32
}

func startFake(t *testing.T, cfg *tls.Config) *fakeSMTP {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	f := &fakeSMTP{ln: ln, tls: cfg}
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go f.serve(c)
		}
	}()
	return f
}

func (f *fakeSMTP) addr() string { return f.ln.Addr().String() }

func (f *fakeSMTP) serve(c net.Conn) {
	defer c.Close()
	n := f.cur.Add(1)
	defer f.cur.Add(-1)
	for {
		p := f.peak.Load()
		if n <= p || f.peak.CompareAndSwap(p, n) {
			break
		}
	}
	r := bufio.NewReader(c)
	w := func(s string) { io.WriteString(c, s+"\r\n") }
	w("220 fake ESMTP")
	tlsOn := false
	for {
		line, err := r.ReadString('\n')
		if err != nil {
			return
		}
		cmd := strings.ToUpper(strings.TrimSpace(line))
		switch {
		case strings.HasPrefix(cmd, "EHLO"):
			if f.tls != nil && !tlsOn {
				w("250-fake")
				w("250 STARTTLS")
			} else {
				w("250 fake")
			}
		case cmd == "STARTTLS":
			w("220 go ahead")
			tc := tls.Server(c, f.tls)
			if err := tc.Handshake(); err != nil {
				return
			}
			c, r, tlsOn = tc, bufio.NewReader(tc), true
			w = func(s string) { io.WriteString(c, s+"\r\n") }
		case strings.HasPrefix(cmd, "MAIL FROM:"):
			f.mu.Lock()
			f.mailFrom = append(f.mailFrom, strings.TrimSpace(line))
			f.mu.Unlock()
			w("250 ok")
		case strings.HasPrefix(cmd, "RCPT TO:"):
			if f.rcptCode != "" {
				w(f.rcptCode)
			} else {
				w("250 ok")
			}
		case cmd == "DATA":
			w("354 go")
			for {
				l, err := r.ReadString('\n')
				if err != nil {
					return
				}
				if l == ".\r\n" {
					break
				}
			}
			time.Sleep(f.hold)
			f.mu.Lock()
			f.delivered++
			f.mu.Unlock()
			w("250 queued")
		case cmd == "QUIT":
			w("221 bye")
			return
		default:
			w("250 ok")
		}
	}
}

func TestDialPolicies(t *testing.T) {
	const host = "127.0.0.1"
	pki := newPKI(t, host)
	srv := startFake(t, &tls.Config{Certificates: []tls.Certificate{pki.leafTLS}})
	plain := startFake(t, nil)
	ctx := context.Background()

	// DANE with matching TLSA record succeeds.
	ok := daneTLS(host, []tlsaRecord{rec(3, 1, 1, pki.leaf)})
	c, err := dialSMTP(ctx, srv.addr(), "client.test", nil, ok)
	if err != nil {
		t.Fatalf("dane match: %v", err)
	}
	if st, isTLS := c.TLSConnectionState(); !isTLS || !st.HandshakeComplete {
		t.Fatal("no TLS")
	}
	c.Quit()

	// Mismatching TLSA record fails the handshake.
	bad := daneTLS(host, []tlsaRecord{rec(3, 1, 1, pki.selfLeaf)})
	if _, err := dialSMTP(ctx, srv.addr(), "client.test", nil, bad); err == nil {
		t.Fatal("dane mismatch accepted")
	}

	// MTA-STS enforce verifies against the system roots: the test CA is unknown.
	sts := &hostTLS{label: "mta-sts", required: true, config: &tls.Config{ServerName: host, MinVersion: tls.VersionTLS12}}
	if _, err := dialSMTP(ctx, srv.addr(), "client.test", nil, sts); err == nil {
		t.Fatal("unverified certificate accepted in enforce mode")
	}

	// Required TLS without STARTTLS fails; opportunistic falls back to plaintext.
	if _, err := dialSMTP(ctx, plain.addr(), "client.test", nil, ok); err == nil || !strings.Contains(err.Error(), "STARTTLS") {
		t.Fatalf("plaintext accepted under DANE: %v", err)
	}
	c, err = dialSMTP(ctx, plain.addr(), "client.test", nil, nil)
	if err != nil {
		t.Fatalf("opportunistic plaintext: %v", err)
	}
	c.Quit()

	// Opportunistic TLS accepts any certificate.
	c, err = dialSMTP(ctx, srv.addr(), "client.test", nil, opportunistic())
	if err != nil {
		t.Fatalf("opportunistic tls: %v", err)
	}
	c.Quit()
}

func TestNullSenderEnvelope(t *testing.T) {
	for in, want := range map[string]string{"": "", "<>": "", " <> ": "", "a@b.test": "a@b.test", "<a@b.test>": "a@b.test"} {
		if got := envelopeFrom(in); got != want {
			t.Errorf("envelopeFrom(%q) = %q, want %q", in, got, want)
		}
	}
	srv := startFake(t, nil)
	q := &Queue{Hostname: func() string { return "mx.local.test" }, Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	relay := &Relay{Host: srv.addr()}
	for _, from := range []string{"", "<>"} {
		if errs := q.send(context.Background(), srv.addr(), "", from, []string{"x@remote.test"}, []byte("Subject: x\r\n\r\nx\r\n"), relay, nil); len(errs) != 0 {
			t.Fatalf("send: %+v", errs)
		}
	}
	for _, l := range srv.mailFrom {
		if !strings.HasPrefix(strings.ToUpper(l), "MAIL FROM:<>") || strings.Contains(l, "<<") {
			t.Errorf("bad null sender: %q", l)
		}
	}
}

func newTestQueue(t *testing.T, relayAddr string) (*Queue, *store.Store) {
	t.Helper()
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	q := New(st, func() string { return "mx.local.test" }, func() Relay { return Relay{Host: relayAddr} },
		slog.New(slog.NewTextHandler(io.Discard, nil)))
	return q, st
}

func enqueue(t *testing.T, st *store.Store, from string, rcpts ...string) int64 {
	t.Helper()
	data := []byte("Subject: test " + time.Now().String() + "\r\n\r\nx\r\n")
	h, err := st.Blobs.Put(data)
	if err != nil {
		t.Fatal(err)
	}
	id, err := st.Enqueue(context.Background(), from, rcpts, h, int64(len(data)))
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func TestConcurrentDeliveryPerDomainLimit(t *testing.T) {
	srv := startFake(t, nil)
	srv.hold = 150 * time.Millisecond
	q, st := newTestQueue(t, srv.addr())
	for range 6 {
		enqueue(t, st, "a@local.test", "b@remote.test")
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { q.Run(ctx); close(done) }()
	deadline := time.Now().Add(10 * time.Second)
	for st.QueueSize(context.Background()) > 0 && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	cancel()
	<-done
	if n := st.QueueSize(context.Background()); n != 0 {
		t.Fatalf("queue not drained: %d left", n)
	}
	srv.mu.Lock()
	delivered := srv.delivered
	srv.mu.Unlock()
	if delivered != 6 {
		t.Fatalf("delivered = %d", delivered)
	}
	if p := srv.peak.Load(); p != 2 {
		t.Fatalf("peak concurrency towards one destination = %d, want 2", p)
	}
}

func TestDelayNoticeOnce(t *testing.T) {
	srv := startFake(t, nil)
	srv.rcptCode = "451 try later"
	q, st := newTestQueue(t, srv.addr())
	var notices []string
	q.Delay = func(_ context.Context, from string, rcpts []string, reason string, _ []byte) {
		notices = append(notices, from+"|"+strings.Join(rcpts, ","))
	}
	ctx := context.Background()
	id := enqueue(t, st, "a@local.test", "b@remote.test")
	relay := q.Relay()
	it, _ := st.GetQueueItem(ctx, id)
	q.attempt(ctx, it, relay)
	if len(notices) != 0 {
		t.Fatal("notice sent for a fresh message")
	}
	// Pretend the message is 5 hours old.
	if _, err := st.DB.Exec(`UPDATE queue SET created_at = ? WHERE id = ?`, time.Now().Add(-5*time.Hour).Unix(), id); err != nil {
		t.Fatal(err)
	}
	for range 3 {
		it, _ = st.GetQueueItem(ctx, id)
		q.attempt(ctx, it, relay)
	}
	if len(notices) != 1 || notices[0] != "a@local.test|b@remote.test" {
		t.Fatalf("notices = %v", notices)
	}
	// Bounces (null sender) never get a delay notice.
	id2 := enqueue(t, st, "", "c@remote.test")
	st.DB.Exec(`UPDATE queue SET created_at = ? WHERE id = ?`, time.Now().Add(-5*time.Hour).Unix(), id2)
	it, _ = st.GetQueueItem(ctx, id2)
	q.attempt(ctx, it, relay)
	if len(notices) != 1 {
		t.Fatalf("delay notice for a bounce: %v", notices)
	}
	// A marker from an older message with the same ID does not suppress the notice.
	it, _ = st.GetQueueItem(ctx, id)
	st.SetKV(ctx, delayKey(id), "12345")
	q.attempt(ctx, it, relay)
	if len(notices) != 2 {
		t.Fatalf("stale marker suppressed the notice: %v", notices)
	}
	// Removing the item clears its marker.
	q.remove(ctx, it)
	if v, _ := st.KV(ctx, delayKey(id)); v != "" {
		t.Fatal("marker not cleared on delete")
	}
}
