package pop3

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"net"
	"strings"
	"testing"
	"time"

	"postwerk/internal/store"
)

func startServer(t *testing.T, configure func(*Server)) string {
	t.Helper()
	s := &Server{
		Auth: func(ctx context.Context, ip, user, pass string) (*store.Account, error) {
			return nil, errors.New("no auth in tests")
		},
		Banned:   func(string) bool { return false },
		Hostname: func() string { return "mx.test" },
		Log:      slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
	if configure != nil {
		configure(s)
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { _ = s.Serve(ctx, ln); close(done) }()
	t.Cleanup(func() { cancel(); <-done })
	return ln.Addr().String()
}

type client struct {
	t    *testing.T
	conn net.Conn
	r    *bufio.Reader
}

func dial(t *testing.T, addr string) *client {
	t.Helper()
	c, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	_ = c.SetDeadline(time.Now().Add(5 * time.Second))
	t.Cleanup(func() { c.Close() })
	return &client{t: t, conn: c, r: bufio.NewReader(c)}
}

func (c *client) line() (string, error) {
	l, err := c.r.ReadString('\n')
	return strings.TrimRight(l, "\r\n"), err
}

func (c *client) expect(prefix string) {
	c.t.Helper()
	l, err := c.line()
	if err != nil {
		c.t.Fatalf("waiting for %q: %v", prefix, err)
	}
	if !strings.HasPrefix(l, prefix) {
		c.t.Fatalf("expected %q, got %q", prefix, l)
	}
}

func TestOverlongLineCloses(t *testing.T) {
	addr := startServer(t, nil)
	c := dial(t, addr)
	c.expect("+OK")
	go func() {
		chunk := bytes.Repeat([]byte("A"), 64*1024)
		for i := 0; i < 16; i++ {
			if _, err := c.conn.Write(chunk); err != nil {
				return
			}
		}
	}()
	for {
		l, err := c.line()
		if err != nil {
			return // closed (possibly reset before the reply arrived)
		}
		if !strings.HasPrefix(l, "-ERR") {
			t.Fatalf("unexpected reply %q", l)
		}
	}
}

func TestReadLimitedLine(t *testing.T) {
	r := bufio.NewReader(strings.NewReader("USER bob\r\n" + strings.Repeat("x", 5000) + "\r\n"))
	if l, err := readLimitedLine(r, maxCommandLine); err != nil || l != "USER bob" {
		t.Fatalf("got %q, %v", l, err)
	}
	if _, err := readLimitedLine(r, maxCommandLine); !errors.Is(err, errLineTooLong) {
		t.Fatalf("expected errLineTooLong, got %v", err)
	}
}

func TestPerIPConnectionLimit(t *testing.T) {
	addr := startServer(t, func(s *Server) { s.MaxConnsPerIP = 2 })
	a := dial(t, addr)
	a.expect("+OK")
	b := dial(t, addr)
	b.expect("+OK")
	c := dial(t, addr)
	c.expect("-ERR")
	if _, err := c.line(); err == nil {
		t.Fatal("rejected connection was not closed")
	}
}

func TestIdleCommandLimit(t *testing.T) {
	addr := startServer(t, func(s *Server) { s.MaxIdleCmds = 3 })
	c := dial(t, addr)
	c.expect("+OK")
	for i := 0; i < 4; i++ {
		io.WriteString(c.conn, "NOOP\r\n")
		c.expect("-ERR not authenticated")
	}
	c.expect("-ERR too many commands")
}

func TestSessionTimeLimit(t *testing.T) {
	addr := startServer(t, func(s *Server) { s.MaxSessionTime = 200 * time.Millisecond })
	c := dial(t, addr)
	c.expect("+OK")
	c.expect("-ERR session time limit")
}
