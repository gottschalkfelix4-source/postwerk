package smtp

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	"postwerk/internal/store"
)

type delivered struct {
	from  string
	rcpts []string
	data  []byte
}

type fakeBackend struct {
	mu   sync.Mutex
	msgs []delivered
}

func (b *fakeBackend) Authenticate(ctx context.Context, ip, user, pass string) (*store.Account, error) {
	return nil, errors.New("no auth in tests")
}
func (b *fakeBackend) Banned(ip string) bool { return false }
func (b *fakeBackend) CheckSender(ctx context.Context, env *Envelope, from string) *Reply {
	return nil
}
func (b *fakeBackend) CheckRecipient(ctx context.Context, env *Envelope, rcpt string) *Reply {
	return nil
}
func (b *fakeBackend) Deliver(ctx context.Context, env *Envelope, data []byte) *Reply {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.msgs = append(b.msgs, delivered{env.From, append([]string(nil), env.Rcpts...), append([]byte(nil), data...)})
	return nil
}
func (b *fakeBackend) delivered() []delivered {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]delivered(nil), b.msgs...)
}

func startServer(t *testing.T, configure func(*Server)) (*Server, *fakeBackend, string) {
	t.Helper()
	be := &fakeBackend{}
	s := &Server{
		Mode:     ModeInbound,
		Hostname: func() string { return "mx.test" },
		MaxSize:  func() int64 { return 1 << 20 },
		Backend:  be,
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
	return s, be, ln.Addr().String()
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

// reply reads a complete (possibly multi-line) reply and returns its last line.
func (c *client) reply() (string, error) {
	for {
		l, err := c.r.ReadString('\n')
		if err != nil {
			return "", err
		}
		if len(l) < 4 || l[3] != '-' {
			return strings.TrimRight(l, "\r\n"), nil
		}
	}
}

func (c *client) expect(code string) string {
	c.t.Helper()
	l, err := c.reply()
	if err != nil {
		c.t.Fatalf("waiting for %s: %v", code, err)
	}
	if !strings.HasPrefix(l, code) {
		c.t.Fatalf("expected %s, got %q", code, l)
	}
	return l
}

func (c *client) send(s string) {
	c.t.Helper()
	if _, err := io.WriteString(c.conn, s); err != nil {
		c.t.Fatal(err)
	}
}

func (c *client) cmd(line, code string) string {
	c.t.Helper()
	c.send(line + "\r\n")
	return c.expect(code)
}

// startData opens a session and gets to the DATA prompt.
func startData(t *testing.T, addr string) *client {
	c := dial(t, addr)
	c.expect("220")
	c.cmd("EHLO client.test", "250")
	c.cmd("MAIL FROM:<sender@example.org>", "250")
	c.cmd("RCPT TO:<user@example.com>", "250")
	c.cmd("DATA", "354")
	return c
}

func TestDeliverNormal(t *testing.T) {
	_, be, addr := startServer(t, nil)
	c := startData(t, addr)
	c.send("Subject: hi\r\n\r\n..leading dot\r\nbody\r\n.\r\n")
	c.expect("250")
	c.cmd("QUIT", "221")
	msgs := be.delivered()
	if len(msgs) != 1 {
		t.Fatalf("expected 1 message, got %d", len(msgs))
	}
	if want := "Subject: hi\r\n\r\n.leading dot\r\nbody\r\n"; string(msgs[0].data) != want {
		t.Fatalf("data = %q, want %q", msgs[0].data, want)
	}
	if msgs[0].from != "sender@example.org" {
		t.Fatalf("from = %q", msgs[0].from)
	}
}

func TestNullSenderIsEmpty(t *testing.T) {
	_, be, addr := startServer(t, nil)
	c := dial(t, addr)
	c.expect("220")
	c.cmd("EHLO client.test", "250")
	c.cmd("MAIL FROM:<>", "250")
	c.cmd("MAIL FROM:<>", "503") // nested MAIL must still be detected
	c.cmd("RCPT TO:<user@example.com>", "250")
	c.cmd("DATA", "354")
	c.send("Subject: bounce\r\n\r\nx\r\n.\r\n")
	c.expect("250")
	msgs := be.delivered()
	if len(msgs) != 1 || msgs[0].from != "" {
		t.Fatalf("expected one message with empty sender, got %+v", msgs)
	}
}

const smuggled = "MAIL FROM:<evil@attacker.test>\r\nRCPT TO:<victim@example.com>\r\nDATA\r\nsmuggled\r\n"

func TestSmugglingBareLFNormalized(t *testing.T) {
	// Bare LF line endings are normalized to CRLF, but a "." line that is
	// not enclosed in CRLF never ends the data; it is kept as content.
	cases := map[string]struct{ payload, stored string }{
		"LF.LF":    {"hello\n.\n", "hello\r\n.\r\n"},
		"CRLF.LF":  {"hello\r\n.\n", "hello\r\n.\r\n"},
		"LF.CRLF":  {"hello\n.\r\n", "hello\r\n.\r\n"},
		"bare-LF":  {"hello\nworld\r\n", "hello\r\nworld\r\n"},
		"stuffed":  {"hello\n..x\n", "hello\r\n.x\r\n"},
		"LF-start": {".\n", ".\r\n"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			_, be, addr := startServer(t, nil)
			c := startData(t, addr)
			// If the server treated the bogus sequence as end of data, the
			// following lines would be executed as commands.
			c.send(tc.payload + smuggled + ".\r\n")
			c.expect("250")
			// Exactly one transaction happened: no second 250/354 is pending.
			c.cmd("NOOP", "250")
			c.cmd("QUIT", "221")
			msgs := be.delivered()
			if len(msgs) != 1 {
				t.Fatalf("expected exactly 1 message, got %d", len(msgs))
			}
			if msgs[0].from != "sender@example.org" || len(msgs[0].rcpts) != 1 || msgs[0].rcpts[0] != "user@example.com" {
				t.Fatalf("envelope changed: %+v", msgs[0])
			}
			if want := tc.stored + smuggled; string(msgs[0].data) != want {
				t.Fatalf("data = %q, want %q", msgs[0].data, want)
			}
		})
	}
}

func TestSmugglingBareCRRejected(t *testing.T) {
	cases := map[string]string{
		"CR.CR":    "hello\r.\r",
		"dot-CRCR": "hello\r\n.\r\r\n",
		"bare-CR":  "hel\rlo\r\n",
	}
	for name, payload := range cases {
		t.Run(name, func(t *testing.T) {
			_, be, addr := startServer(t, nil)
			c := startData(t, addr)
			c.send(payload + smuggled + ".\r\n")
			c.expect("554 5.5.2")
			// The transaction is reset and the session is usable.
			c.cmd("RCPT TO:<user@example.com>", "503")
			c.cmd("NOOP", "250")
			c.cmd("QUIT", "221")
			if n := len(be.delivered()); n != 0 {
				t.Fatalf("expected no delivery, got %d", n)
			}
		})
	}
}

func TestBufferBoundaryIsNotLineStart(t *testing.T) {
	t.Run("default line limit", func(t *testing.T) {
		_, be, addr := startServer(t, nil)
		c := startData(t, addr)
		c.send(strings.Repeat("a", 4096) + ".\r\n" + smuggled + ".\r\n")
		c.expect("552")
		c.cmd("NOOP", "250")
		if n := len(be.delivered()); n != 0 {
			t.Fatalf("expected no delivery, got %d", n)
		}
	})

	t.Run("raised line limit", func(t *testing.T) {
		_, be, addr := startServer(t, func(s *Server) { s.MaxLineLength = 10000 })
		c := startData(t, addr)
		long := strings.Repeat("a", 4096)
		c.send(long + ".\r\n" + smuggled + ".\r\n")
		c.expect("250")
		c.cmd("QUIT", "221")
		msgs := be.delivered()
		if len(msgs) != 1 {
			t.Fatalf("expected exactly 1 message, got %d", len(msgs))
		}
		if want := long + ".\r\n" + smuggled; string(msgs[0].data) != want {
			t.Fatalf("smuggled commands not kept as message content")
		}
		if msgs[0].from != "sender@example.org" {
			t.Fatalf("from = %q", msgs[0].from)
		}
	})

	t.Run("CRLF split across buffer", func(t *testing.T) {
		_, be, addr := startServer(t, nil)
		c := startData(t, addr)
		c.send(strings.Repeat("b", 4095) + "\r\n.\r\n")
		c.expect("250")
		msgs := be.delivered()
		if len(msgs) != 1 || string(msgs[0].data) != strings.Repeat("b", 4095)+"\r\n" {
			t.Fatalf("unexpected delivery %d", len(msgs))
		}
	})
}

func TestOverlongCommandCloses(t *testing.T) {
	_, _, addr := startServer(t, nil)
	c := dial(t, addr)
	c.expect("220")
	go func() {
		chunk := bytes.Repeat([]byte("A"), 64*1024)
		for i := 0; i < 16; i++ {
			if _, err := c.conn.Write(chunk); err != nil {
				return
			}
		}
	}()
	// The server either answers 500 or the connection is reset before the
	// reply arrives; in both cases it must be closed.
	for {
		l, err := c.reply()
		if err != nil {
			return
		}
		if !strings.HasPrefix(l, "500") {
			t.Fatalf("unexpected reply %q", l)
		}
	}
}

func TestReadLimitedLineBoundsMemory(t *testing.T) {
	cr := &countingReader{r: io.LimitReader(neverLF{}, 10<<20)}
	_, err := readLimitedLine(bufio.NewReaderSize(cr, 4096), maxCommandLine)
	if !errors.Is(err, errLineTooLong) {
		t.Fatalf("err = %v", err)
	}
	if cr.n > 2*4096 {
		t.Fatalf("read %d bytes for an overlong line", cr.n)
	}
}

func TestReadDataBoundsMemory(t *testing.T) {
	// 10 MB of short lines with a 1 KB limit: drained, then rejected.
	var in bytes.Buffer
	line := strings.Repeat("x", 78) + "\r\n"
	for in.Len() < 10<<20 {
		in.WriteString(line)
	}
	in.WriteString(".\r\n")
	data, res, err := readData(bufio.NewReader(&in), 1024, defaultMaxLineLength)
	if err != nil || res != dataTooBig || data != nil {
		t.Fatalf("res=%v err=%v len=%d", res, err, len(data))
	}
}

func TestDataTooBig(t *testing.T) {
	_, be, addr := startServer(t, func(s *Server) { s.MaxSize = func() int64 { return 1000 } })
	c := startData(t, addr)
	c.send(strings.Repeat(strings.Repeat("y", 98)+"\r\n", 50) + ".\r\n")
	c.expect("552")
	c.cmd("NOOP", "250")
	c.cmd("QUIT", "221")
	if n := len(be.delivered()); n != 0 {
		t.Fatalf("expected no delivery, got %d", n)
	}
}

func TestPerIPConnectionLimit(t *testing.T) {
	_, _, addr := startServer(t, func(s *Server) { s.MaxConnsPerIP = 2 })
	a := dial(t, addr)
	a.expect("220")
	b := dial(t, addr)
	b.expect("220")
	c := dial(t, addr)
	c.expect("421 4.7.0")

	a.cmd("QUIT", "221")
	// The slot is released once the first session has ended.
	deadline := time.Now().Add(2 * time.Second)
	for {
		d := dial(t, addr)
		l, err := d.reply()
		if err == nil && strings.HasPrefix(l, "220") {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("slot not released, last reply %q err %v", l, err)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestJunkCommandLimit(t *testing.T) {
	_, _, addr := startServer(t, func(s *Server) { s.MaxJunkCmds = 5 })
	c := dial(t, addr)
	c.expect("220")
	for i := 0; i < 5; i++ {
		c.cmd("NOOP", "250")
	}
	c.send("NOOP\r\n")
	c.expect("250")
	c.expect("421")
}

func TestSessionTimeLimit(t *testing.T) {
	_, _, addr := startServer(t, func(s *Server) { s.MaxSessionTime = 200 * time.Millisecond })
	c := dial(t, addr)
	c.expect("220")
	c.expect("421 4.4.2")
}

func TestAddressValidation(t *testing.T) {
	_, _, addr := startServer(t, nil)
	c := dial(t, addr)
	c.expect("220")
	c.cmd("EHLO client.test", "250")
	c.cmd("MAIL FROM:<a\x01b@example.org>", "501")
	c.cmd("MAIL FROM:<"+strings.Repeat("a", 330)+"@example.org>", "501")
	c.cmd("MAIL FROM:<sender@example.org>", "250")
	c.cmd("RCPT TO:<us\x00er@example.com>", "501")
	c.cmd("RCPT TO:<"+strings.Repeat("r", 400)+"@example.com>", "501")
	c.cmd("RCPT TO:<user@example.com>", "250")
}

type neverLF struct{}

func (neverLF) Read(p []byte) (int, error) {
	for i := range p {
		p[i] = 'A'
	}
	return len(p), nil
}

type countingReader struct {
	r io.Reader
	n int
}

func (c *countingReader) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	c.n += n
	return n, err
}
