package managesieve

import (
	"bufio"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math/big"
	"net"
	"strconv"
	"strings"
	"testing"
	"time"

	"postwerk/internal/store"
)

func selfSigned(t *testing.T) *tls.Config {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "localhost"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour), DNSNames: []string{"localhost"}}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	return &tls.Config{Certificates: []tls.Certificate{{Certificate: [][]byte{der}, PrivateKey: key}}}
}

type testEnv struct {
	addr string
	st   *store.Store
	acct *store.Account
}

func start(t *testing.T, insecure bool) *testEnv {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	acct := &store.Account{Email: "alice@example.test", PasswordHash: "x", Role: "user"}
	if err := st.CreateAccount(ctx, acct); err != nil {
		t.Fatal(err)
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	srv := &Server{TLSConfig: selfSigned(t), Store: st, Log: slog.New(slog.NewTextHandler(io.Discard, nil)), AllowInsecureAuth: insecure,
		Auth: func(_ context.Context, _, user, pass string) (*store.Account, error) {
			if user == "alice@example.test" && pass == "geheim" {
				return acct, nil
			}
			return nil, errors.New("invalid")
		}}
	go srv.Serve(ctx, ln)
	t.Cleanup(func() { cancel(); st.Close() })
	return &testEnv{addr: ln.Addr().String(), st: st, acct: acct}
}

type client struct {
	t    *testing.T
	conn net.Conn
	r    *bufio.Reader
}

func dial(t *testing.T, addr string) *client {
	t.Helper()
	conn, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })
	_ = conn.SetDeadline(time.Now().Add(10 * time.Second))
	return &client{t: t, conn: conn, r: bufio.NewReader(conn)}
}

func (c *client) write(s string) {
	c.t.Helper()
	if _, err := io.WriteString(c.conn, s); err != nil {
		c.t.Fatal(err)
	}
}

// response reads lines up to and including the final OK/NO/BYE line and
// returns them; literal payloads are inlined.
func (c *client) response() []string {
	c.t.Helper()
	var lines []string
	for {
		line, err := c.r.ReadString('\n')
		if err != nil {
			c.t.Fatalf("read: %v (so far %q)", err, lines)
		}
		line = strings.TrimRight(line, "\r\n")
		if strings.HasPrefix(line, "{") && strings.HasSuffix(line, "}") {
			n, _ := strconv.Atoi(strings.Trim(line, "{}+"))
			buf := make([]byte, n)
			if _, err := io.ReadFull(c.r, buf); err != nil {
				c.t.Fatal(err)
			}
			lines = append(lines, string(buf))
			// The rest of the line after the literal (normally empty).
			if rest, _ := c.r.ReadString('\n'); strings.TrimRight(rest, "\r\n") != "" {
				lines[len(lines)-1] += strings.TrimRight(rest, "\r\n")
			}
			continue
		}
		lines = append(lines, line)
		if strings.HasPrefix(line, "OK") || strings.HasPrefix(line, "NO") || strings.HasPrefix(line, "BYE") {
			return lines
		}
	}
}

func (c *client) cmd(s string) []string {
	c.t.Helper()
	c.write(s + "\r\n")
	return c.response()
}

func last(lines []string) string { return lines[len(lines)-1] }

func plain(user, pass string) string {
	return base64.StdEncoding.EncodeToString([]byte("\x00" + user + "\x00" + pass))
}

func contains(lines []string, s string) bool {
	for _, l := range lines {
		if strings.Contains(l, s) {
			return true
		}
	}
	return false
}

func TestGreetingAndStartTLS(t *testing.T) {
	e := start(t, false)
	c := dial(t, e.addr)
	greet := c.response()
	if !contains(greet, `"STARTTLS"`) || !contains(greet, `"SASL" ""`) || !contains(greet, `"IMPLEMENTATION" "Postwerk"`) ||
		!contains(greet, "fileinto") || !strings.HasPrefix(last(greet), "OK") {
		t.Fatalf("greeting = %q", greet)
	}
	// No authentication before TLS.
	if r := c.cmd(`AUTHENTICATE "PLAIN" "` + plain("alice@example.test", "geheim") + `"`); !strings.HasPrefix(last(r), "NO (ENCRYPT-NEEDED)") {
		t.Fatalf("auth without tls: %q", r)
	}
	if r := c.cmd("LISTSCRIPTS"); !strings.HasPrefix(last(r), "NO") {
		t.Fatalf("listscripts without auth: %q", r)
	}
	if r := c.cmd("STARTTLS"); !strings.HasPrefix(last(r), "OK") {
		t.Fatalf("starttls: %q", r)
	}
	tc := tls.Client(c.conn, &tls.Config{InsecureSkipVerify: true})
	if err := tc.Handshake(); err != nil {
		t.Fatal(err)
	}
	c.conn, c.r = tc, bufio.NewReader(tc)
	caps := c.response()
	if !contains(caps, `"SASL" "PLAIN"`) || contains(caps, `"STARTTLS"`) {
		t.Fatalf("capabilities after tls: %q", caps)
	}
	if r := c.cmd(`AUTHENTICATE "PLAIN" "` + plain("alice@example.test", "falsch") + `"`); !strings.HasPrefix(last(r), "NO") {
		t.Fatalf("wrong password: %q", r)
	}
	// Without initial response: empty challenge, then the response.
	c.write(`AUTHENTICATE "PLAIN"` + "\r\n")
	if line, _ := c.r.ReadString('\n'); line != "\"\"\r\n" {
		t.Fatalf("challenge = %q", line)
	}
	if r := c.cmd(`"` + plain("alice@example.test", "geheim") + `"`); !strings.HasPrefix(last(r), "OK") {
		t.Fatalf("auth: %q", r)
	}
	if r := c.cmd("CAPABILITY"); !contains(r, `"OWNER" "alice@example.test"`) {
		t.Fatalf("capability after auth: %q", r)
	}
	if r := c.cmd("LOGOUT"); !strings.HasPrefix(last(r), "OK") {
		t.Fatalf("logout: %q", r)
	}
}

func login(t *testing.T, e *testEnv) *client {
	t.Helper()
	c := dial(t, e.addr)
	c.response()
	if r := c.cmd(`AUTHENTICATE "PLAIN" {` + strconv.Itoa(len(plain("alice@example.test", "geheim"))) + "+}\r\n" + plain("alice@example.test", "geheim")); !strings.HasPrefix(last(r), "OK") {
		t.Fatalf("auth: %q", r)
	}
	return c
}

func TestScriptCommands(t *testing.T) {
	e := start(t, true)
	c := login(t, e)
	script := "require \"fileinto\";\r\nif header :contains \"subject\" \"x\" {\r\n  fileinto \"X\";\r\n}\r\n"
	lit := func(s string) string { return "{" + strconv.Itoa(len(s)) + "+}\r\n" + s }

	if r := c.cmd(`HAVESPACE "regeln" 1000`); !strings.HasPrefix(last(r), "OK") {
		t.Fatalf("havespace: %q", r)
	}
	if r := c.cmd(`HAVESPACE "regeln" 999999`); !strings.HasPrefix(last(r), "NO (QUOTA/MAXSIZE)") {
		t.Fatalf("havespace too large: %q", r)
	}
	if r := c.cmd(`CHECKSCRIPT ` + lit(script)); !strings.HasPrefix(last(r), "OK") {
		t.Fatalf("checkscript: %q", r)
	}
	r := c.cmd(`CHECKSCRIPT ` + lit("fileinto \"x\";"))
	if !strings.HasPrefix(last(r), "NO") || !strings.Contains(last(r), "Zeile 1, Spalte 1") {
		t.Fatalf("checkscript invalid: %q", r)
	}
	if r := c.cmd(`PUTSCRIPT "kaputt" ` + lit("if true {")); !strings.HasPrefix(last(r), "NO") {
		t.Fatalf("putscript invalid: %q", r)
	}
	if r := c.cmd(`PUTSCRIPT "regeln" ` + lit(script)); !strings.HasPrefix(last(r), "OK") {
		t.Fatalf("putscript: %q", r)
	}
	if r := c.cmd(`PUTSCRIPT "zwei" "keep;"`); !strings.HasPrefix(last(r), "OK") {
		t.Fatalf("putscript quoted: %q", r)
	}
	if r := c.cmd(`SETACTIVE "regeln"`); !strings.HasPrefix(last(r), "OK") {
		t.Fatalf("setactive: %q", r)
	}
	r = c.cmd("LISTSCRIPTS")
	if len(r) != 3 || r[0] != `"regeln" ACTIVE` || r[1] != `"zwei"` {
		t.Fatalf("listscripts: %q", r)
	}
	r = c.cmd(`GETSCRIPT "regeln"`)
	if len(r) != 2 || r[0] != script+"\r\n" && r[0] != script {
		t.Fatalf("getscript: %q", r)
	}
	if r := c.cmd(`GETSCRIPT "fehlt"`); !strings.HasPrefix(last(r), "NO (NONEXISTENT)") {
		t.Fatalf("getscript missing: %q", r)
	}
	if r := c.cmd(`DELETESCRIPT "regeln"`); !strings.HasPrefix(last(r), "NO (ACTIVE)") {
		t.Fatalf("delete active: %q", r)
	}
	if r := c.cmd(`RENAMESCRIPT "zwei" "regeln"`); !strings.HasPrefix(last(r), "NO (ALREADYEXISTS)") {
		t.Fatalf("rename onto existing: %q", r)
	}
	if r := c.cmd(`RENAMESCRIPT "zwei" "drei"`); !strings.HasPrefix(last(r), "OK") {
		t.Fatalf("rename: %q", r)
	}
	if r := c.cmd(`DELETESCRIPT "drei"`); !strings.HasPrefix(last(r), "OK") {
		t.Fatalf("delete: %q", r)
	}
	if r := c.cmd(`SETACTIVE ""`); !strings.HasPrefix(last(r), "OK") {
		t.Fatalf("deactivate: %q", r)
	}
	if _, err := e.st.ActiveSieveScript(context.Background(), e.acct.ID); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("still active: %v", err)
	}
	if r := c.cmd(`NOOP "abc"`); last(r) != `OK (TAG "abc") "Done"` {
		t.Fatalf("noop: %q", r)
	}
	if r := c.cmd(`FOO`); !strings.HasPrefix(last(r), "NO") {
		t.Fatalf("unknown: %q", r)
	}
	if r := c.cmd(`PUTSCRIPT "x"`); !strings.HasPrefix(last(r), "NO") {
		t.Fatalf("missing argument: %q", r)
	}
	// Still in sync after errors.
	if r := c.cmd("LISTSCRIPTS"); len(r) != 2 || !strings.HasPrefix(last(r), "OK") {
		t.Fatalf("listscripts: %q", r)
	}
}

func TestOversizedLiteral(t *testing.T) {
	e := start(t, true)
	c := login(t, e)
	big := strings.Repeat("#", store.MaxSieveScriptSize+2000)
	if r := c.cmd(`PUTSCRIPT "gross" {` + strconv.Itoa(len(big)) + "+}\r\n" + big); !strings.HasPrefix(last(r), "NO") {
		t.Fatalf("oversized: %q", r)
	}
	if r := c.cmd("NOOP"); !strings.HasPrefix(last(r), "OK") {
		t.Fatalf("not in sync after oversized literal: %q", r)
	}
	c.write(fmt.Sprintf("PUTSCRIPT \"riesig\" {%d+}\r\n", 50<<20))
	if r := c.response(); !strings.HasPrefix(last(r), "BYE") {
		t.Fatalf("huge literal: %q", r)
	}
}

func TestAuthFailuresDisconnect(t *testing.T) {
	e := start(t, true)
	c := dial(t, e.addr)
	c.response()
	for i := 0; i < maxAuthFails-1; i++ {
		if r := c.cmd(`AUTHENTICATE "PLAIN" "` + plain("alice@example.test", "nein") + `"`); !strings.HasPrefix(last(r), "NO") {
			t.Fatalf("attempt %d: %q", i, r)
		}
	}
	if r := c.cmd(`AUTHENTICATE "PLAIN" "` + plain("alice@example.test", "nein") + `"`); !strings.HasPrefix(last(r), "BYE") {
		t.Fatalf("expected BYE, got %q", r)
	}
}

func TestQuoting(t *testing.T) {
	if quote(`a"b\c`) != `"a\"b\\c"` {
		t.Fatal(quote(`a"b\c`))
	}
	if quote("a\r\nb") != "{4}\r\na\r\nb" {
		t.Fatal(quote("a\r\nb"))
	}
}

// Literals are read before the command is checked, so without a budget one
// unauthenticated line could buffer hundreds of megabytes.
func TestLiteralBudget(t *testing.T) {
	e := start(t, true)
	c := dial(t, e.addr)
	c.response() // greeting
	chunk := strings.Repeat("x", 5000)
	c.write("NOOP {5000+}\r\n" + chunk + "\r\n")
	if r := c.response(); !strings.HasPrefix(last(r), "BYE") {
		t.Fatalf("large literal before login: %q", r)
	}

	c = login(t, e)
	lit := strings.Repeat("y", 100)
	c.write("NOOP" + strings.Repeat(" {100+}\r\n"+lit, maxLiterals+1) + "\r\n")
	if r := c.response(); !strings.HasPrefix(last(r), "BYE") {
		t.Fatalf("too many literals: %q", r)
	}
}
