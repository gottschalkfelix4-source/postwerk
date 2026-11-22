// Package managesieve implements a ManageSieve server (RFC 5804) for
// uploading and managing Sieve scripts from mail clients.
package managesieve

import (
	"bufio"
	"context"
	"crypto/tls"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"postwerk/internal/metrics"
	"postwerk/internal/sieve"
	"postwerk/internal/store"
)

// Authenticator checks credentials for a client address.
type Authenticator func(ctx context.Context, ip, user, pass string) (*store.Account, error)

// Limits.
const (
	maxLine        = 8 << 10                         // command line without literals
	maxLiteral     = store.MaxSieveScriptSize + 1024 // largest accepted literal
	maxDiscard     = 1 << 20                         // oversized literals up to this size are skipped
	maxLiterals    = 3                               // literals per command (RENAMESCRIPT/PUTSCRIPT need two)
	maxPreAuthLit  = 4 << 10                         // before login only AUTHENTICATE sends small literals
	maxAuthFails   = 3
	maxBadCommands = 50
	idleTimeout    = 10 * time.Minute
	sessionLimit   = time.Hour
	defaultMaxConn = 200
	defaultPerIP   = 10
)

type Server struct {
	Addr              string
	TLSConfig         *tls.Config
	Store             *store.Store
	Auth              Authenticator
	Banned            func(ip string) bool
	Log               *slog.Logger
	AllowInsecureAuth bool

	// Connection limits; zero selects the default.
	MaxConns      int
	MaxConnsPerIP int

	mu    sync.Mutex
	total int
	perIP map[string]int
}

func (s *Server) ListenAndServe(ctx context.Context) error {
	ln, err := net.Listen("tcp", s.Addr)
	if err != nil {
		return err
	}
	return s.Serve(ctx, ln)
}

// Serve accepts connections on ln until ctx is cancelled.
func (s *Server) Serve(ctx context.Context, ln net.Listener) error {
	s.Log.Info("listening", "proto", "managesieve", "addr", ln.Addr().String())
	go func() { <-ctx.Done(); ln.Close() }()
	for {
		c, err := ln.Accept()
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			time.Sleep(100 * time.Millisecond)
			continue
		}
		metrics.Connections.WithLabelValues("managesieve").Inc()
		host, _, _ := net.SplitHostPort(c.RemoteAddr().String())
		if !s.acquire(host) {
			go func() {
				defer c.Close()
				_ = c.SetDeadline(time.Now().Add(5 * time.Second))
				_, _ = io.WriteString(c, "BYE \"Too many connections\"\r\n")
			}()
			continue
		}
		go func() {
			defer s.release(host)
			s.serve(ctx, c, host)
		}()
	}
}

func (s *Server) acquire(ip string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.perIP == nil {
		s.perIP = map[string]int{}
	}
	maxTotal, maxIP := s.MaxConns, s.MaxConnsPerIP
	if maxTotal <= 0 {
		maxTotal = defaultMaxConn
	}
	if maxIP <= 0 {
		maxIP = defaultPerIP
	}
	if s.total >= maxTotal || s.perIP[ip] >= maxIP {
		return false
	}
	s.total++
	s.perIP[ip]++
	return true
}

func (s *Server) release(ip string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.total--
	if s.perIP[ip]--; s.perIP[ip] <= 0 {
		delete(s.perIP, ip)
	}
}

type session struct {
	s        *Server
	ctx      context.Context
	conn     net.Conn
	r        *bufio.Reader
	w        *bufio.Writer
	ip       string
	tls      bool
	acct     *store.Account
	log      *slog.Logger
	deadline time.Time
	fails    int
	bad      int
}

func (s *Server) serve(ctx context.Context, c net.Conn, ip string) {
	defer c.Close()
	ss := &session{s: s, ctx: ctx, conn: c, r: bufio.NewReader(c), w: bufio.NewWriter(c), ip: ip,
		log: s.Log.With("proto", "managesieve", "ip", ip), deadline: time.Now().Add(sessionLimit)}
	if s.Banned != nil && s.Banned(ip) {
		ss.send(`BYE "Too many failed attempts"`)
		return
	}
	ss.capabilities()
	ss.send(`OK "Postwerk ManageSieve ready"`)
	for {
		dl := time.Now().Add(idleTimeout)
		if dl.After(ss.deadline) {
			dl = ss.deadline
		}
		_ = ss.conn.SetReadDeadline(dl)
		cmd, args, err := ss.readCommand()
		if err != nil {
			var pe *protoError
			if errors.As(err, &pe) {
				if pe.fatal {
					ss.send("BYE " + quote(pe.msg))
					return
				}
				ss.no("", pe.msg)
				if ss.bad++; ss.bad > maxBadCommands {
					ss.send(`BYE "Too many errors"`)
					return
				}
				continue
			}
			return
		}
		if ss.handle(strings.ToUpper(cmd), args) {
			return
		}
		if ss.bad > maxBadCommands {
			ss.send(`BYE "Too many errors"`)
			return
		}
	}
}

// ---- output ----

func (ss *session) send(line string) {
	ss.w.WriteString(line + "\r\n")
	_ = ss.w.Flush()
}

// quote renders a string as a quoted string, or as a literal if it cannot
// be quoted (line breaks, long values).
func quote(v string) string {
	if strings.ContainsAny(v, "\r\n\x00") || len(v) > 1024 {
		return "{" + strconv.Itoa(len(v)) + "}\r\n" + v
	}
	return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(v) + `"`
}

func (ss *session) ok(msg string) {
	if msg == "" {
		ss.send("OK")
		return
	}
	ss.send("OK " + quote(msg))
}

func (ss *session) no(code, msg string) {
	line := "NO"
	if code != "" {
		line += " (" + code + ")"
	}
	ss.send(line + " " + quote(msg))
}

func (ss *session) capabilities() {
	caps := []string{
		`"IMPLEMENTATION" "Postwerk"`,
		`"SIEVE" ` + quote(strings.Join(sieve.Capabilities(), " ")),
		`"MAXREDIRECTS" "` + strconv.Itoa(sieve.MaxRedirects) + `"`,
		`"VERSION" "1.0"`,
	}
	if ss.tls || ss.s.AllowInsecureAuth {
		caps = append(caps, `"SASL" "PLAIN"`)
	} else {
		caps = append(caps, `"SASL" ""`)
	}
	if !ss.tls && ss.s.TLSConfig != nil {
		caps = append(caps, `"STARTTLS"`)
	}
	if ss.acct != nil {
		caps = append(caps, `"OWNER" `+quote(ss.acct.Email))
	}
	for _, c := range caps {
		ss.send(c)
	}
}

// ---- input ----

type protoError struct {
	msg   string
	fatal bool
}

func (e *protoError) Error() string { return e.msg }

// readCommand reads one command with its arguments (atoms, quoted strings
// and literals, RFC 5804 section 4).
func (ss *session) readCommand() (string, []string, error) {
	var cmd string
	var args []string
	lineLen := 0
	literals := 0
	// All literals of one command together stay within one script plus a
	// name, so a single command line cannot buffer unbounded memory.
	budget := maxLiteral + maxLine
	if ss.acct == nil {
		budget = maxPreAuthLit
	}
	for {
		c, err := ss.r.ReadByte()
		if err != nil {
			return "", nil, err
		}
		lineLen++
		if lineLen > maxLine {
			return "", nil, &protoError{msg: "Line too long", fatal: true}
		}
		switch {
		case c == ' ' || c == '\t':
			continue
		case c == '\r':
			continue
		case c == '\n':
			if cmd == "" && len(args) == 0 {
				return "", nil, &protoError{msg: "Empty command"}
			}
			return cmd, args, nil
		case c == '"':
			s, n, err := ss.readQuoted()
			if err != nil {
				return "", nil, err
			}
			lineLen += n
			if cmd == "" {
				return "", nil, ss.skipLine(&protoError{msg: "Command name expected"})
			}
			args = append(args, s)
		case c == '{':
			if literals++; literals > maxLiterals {
				return "", nil, &protoError{msg: "Too many literals", fatal: true}
			}
			s, err := ss.readLiteral(budget)
			if err != nil {
				return "", nil, err
			}
			budget -= len(s)
			if cmd == "" {
				return "", nil, ss.skipLine(&protoError{msg: "Command name expected"})
			}
			args = append(args, s)
		default:
			_ = ss.r.UnreadByte()
			atom, err := ss.readAtom()
			if err != nil {
				return "", nil, err
			}
			lineLen += len(atom)
			if cmd == "" {
				cmd = atom
			} else {
				args = append(args, atom)
			}
		}
	}
}

// skipLine discards the rest of the current line and returns err.
func (ss *session) skipLine(err error) error {
	n := 0
	for {
		c, rerr := ss.r.ReadByte()
		if rerr != nil {
			return rerr
		}
		if c == '\n' {
			return err
		}
		if n++; n > maxLine {
			return &protoError{msg: "Line too long", fatal: true}
		}
	}
}

func (ss *session) readAtom() (string, error) {
	var b strings.Builder
	for {
		c, err := ss.r.ReadByte()
		if err != nil {
			return "", err
		}
		if c == ' ' || c == '\r' || c == '\n' || c == '"' || c == '{' {
			_ = ss.r.UnreadByte()
			return b.String(), nil
		}
		b.WriteByte(c)
		if b.Len() > maxLine {
			return "", &protoError{msg: "Line too long", fatal: true}
		}
	}
}

func (ss *session) readQuoted() (string, int, error) {
	var b strings.Builder
	for {
		c, err := ss.r.ReadByte()
		if err != nil {
			return "", 0, err
		}
		switch c {
		case '"':
			return b.String(), b.Len(), nil
		case '\\':
			n, err := ss.r.ReadByte()
			if err != nil {
				return "", 0, err
			}
			b.WriteByte(n)
		case '\r', '\n':
			if c == '\r' {
				_, _ = ss.r.ReadByte()
			}
			return "", 0, &protoError{msg: "Line break in quoted string"}
		default:
			b.WriteByte(c)
		}
		if b.Len() > maxLine {
			return "", 0, &protoError{msg: "Line too long", fatal: true}
		}
	}
}

// readLiteral reads "{n}" or "{n+}" CRLF followed by n octets. The opening
// brace has been consumed.
func (ss *session) readLiteral(limit int) (string, error) {
	var num strings.Builder
	for {
		c, err := ss.r.ReadByte()
		if err != nil {
			return "", err
		}
		if c == '}' {
			break
		}
		if (c < '0' || c > '9') && c != '+' || num.Len() > 10 {
			return "", ss.skipLine(&protoError{msg: "Invalid literal"})
		}
		num.WriteByte(c)
	}
	n, err := strconv.Atoi(strings.TrimSuffix(num.String(), "+"))
	if err != nil || n < 0 {
		return "", ss.skipLine(&protoError{msg: "Invalid literal"})
	}
	// Line end after the literal header.
	c, err := ss.r.ReadByte()
	if err == nil && c == '\r' {
		c, err = ss.r.ReadByte()
	}
	if err != nil {
		return "", err
	}
	if c != '\n' {
		return "", ss.skipLine(&protoError{msg: "Invalid literal"})
	}
	if n > maxLiteral || n > limit {
		if n > maxDiscard || ss.acct == nil {
			return "", &protoError{msg: "Literal too large", fatal: true}
		}
		if _, err := io.CopyN(io.Discard, ss.r, int64(n)); err != nil {
			return "", err
		}
		return "", ss.skipLine(&protoError{msg: "Script too large"})
	}
	buf := make([]byte, n)
	if _, err := io.ReadFull(ss.r, buf); err != nil {
		return "", err
	}
	return string(buf), nil
}

// ---- commands ----

func (ss *session) handle(cmd string, args []string) bool {
	authed := ss.acct != nil
	switch cmd {
	case "CAPABILITY":
		ss.capabilities()
		ss.ok("")
		return false
	case "LOGOUT":
		ss.ok("Logout")
		return true
	case "NOOP":
		if len(args) > 0 {
			ss.send("OK (TAG " + quote(args[0]) + `) "Done"`)
		} else {
			ss.ok("Done")
		}
		return false
	case "STARTTLS":
		if ss.tls || ss.s.TLSConfig == nil || authed {
			ss.no("", "STARTTLS not available")
			return false
		}
		ss.ok("Begin TLS negotiation")
		tc := tls.Server(ss.conn, ss.s.TLSConfig)
		_ = tc.SetDeadline(time.Now().Add(30 * time.Second))
		if err := tc.Handshake(); err != nil {
			ss.log.Debug("tls handshake failed", "err", err)
			return true
		}
		_ = tc.SetDeadline(time.Time{})
		ss.conn, ss.r, ss.w, ss.tls = tc, bufio.NewReader(tc), bufio.NewWriter(tc), true
		// RFC 5804 2.2: capabilities are sent again after STARTTLS.
		ss.capabilities()
		ss.ok("TLS active")
		return false
	case "AUTHENTICATE":
		return ss.authenticate(args)
	}
	known := map[string]bool{"LISTSCRIPTS": true, "GETSCRIPT": true, "PUTSCRIPT": true, "SETACTIVE": true,
		"DELETESCRIPT": true, "RENAMESCRIPT": true, "CHECKSCRIPT": true, "HAVESPACE": true}
	if !known[cmd] {
		ss.bad++
		ss.no("", "Unknown command")
		return false
	}
	if !authed {
		ss.bad++
		ss.no("", "Authenticate first")
		return false
	}
	ctx, id := ss.ctx, ss.acct.ID
	want := func(n int) bool {
		if len(args) != n {
			ss.no("", fmt.Sprintf("%s expects %d argument(s)", cmd, n))
			return false
		}
		return true
	}
	switch cmd {
	case "LISTSCRIPTS":
		list, err := ss.s.Store.ListSieveScripts(ctx, id)
		if err != nil {
			ss.fail(err)
			return false
		}
		for _, sc := range list {
			line := quote(sc.Name)
			if sc.Active {
				line += " ACTIVE"
			}
			ss.send(line)
		}
		ss.ok("Listscripts completed")
	case "GETSCRIPT":
		if !want(1) {
			return false
		}
		sc, err := ss.s.Store.GetSieveScript(ctx, id, args[0])
		if err != nil {
			ss.fail(err)
			return false
		}
		ss.send("{" + strconv.Itoa(len(sc.Content)) + "}\r\n" + sc.Content)
		ss.ok("Getscript completed")
	case "PUTSCRIPT":
		if !want(2) {
			return false
		}
		if !store.ValidSieveName(args[0]) {
			ss.no("", "Invalid script name")
			return false
		}
		if !ss.check(args[1]) {
			return false
		}
		if err := ss.s.Store.PutSieveScript(ctx, id, args[0], args[1]); err != nil {
			ss.fail(err)
			return false
		}
		ss.log.Info("sieve script stored", "user", ss.acct.Email, "script", args[0])
		ss.ok("Putscript completed")
	case "CHECKSCRIPT":
		if !want(1) {
			return false
		}
		if ss.check(args[0]) {
			ss.ok("Script is valid")
		}
	case "SETACTIVE":
		if !want(1) {
			return false
		}
		if err := ss.s.Store.SetActiveSieveScript(ctx, id, args[0]); err != nil {
			ss.fail(err)
			return false
		}
		ss.log.Info("sieve script activated", "user", ss.acct.Email, "script", args[0])
		ss.ok("Setactive completed")
	case "DELETESCRIPT":
		if !want(1) {
			return false
		}
		if err := ss.s.Store.DeleteSieveScript(ctx, id, args[0]); err != nil {
			ss.fail(err)
			return false
		}
		ss.ok("Deletescript completed")
	case "RENAMESCRIPT":
		if !want(2) {
			return false
		}
		if err := ss.s.Store.RenameSieveScript(ctx, id, args[0], args[1]); err != nil {
			ss.fail(err)
			return false
		}
		ss.ok("Renamescript completed")
	case "HAVESPACE":
		if !want(2) {
			return false
		}
		size, err := strconv.Atoi(args[1])
		if err != nil || size < 0 {
			ss.no("", "Invalid size")
			return false
		}
		if !store.ValidSieveName(args[0]) {
			ss.no("", "Invalid script name")
			return false
		}
		if err := ss.s.Store.SieveHaveSpace(ctx, id, args[0], size); err != nil {
			if errors.Is(err, store.ErrSieveQuota) && size > store.MaxSieveScriptSize {
				ss.no("QUOTA/MAXSIZE", "Script too large")
				return false
			}
			ss.fail(err)
			return false
		}
		ss.ok("Putscript would succeed")
	}
	return false
}

// check validates a script and reports errors to the client.
func (ss *session) check(content string) bool {
	if !utf8.ValidString(content) {
		ss.no("", "Script is not valid UTF-8")
		return false
	}
	if len(content) > store.MaxSieveScriptSize {
		ss.no("QUOTA/MAXSIZE", "Script too large")
		return false
	}
	if err := sieve.Check(content); err != nil {
		ss.no("", err.Error())
		return false
	}
	return true
}

func (ss *session) fail(err error) {
	switch {
	case errors.Is(err, store.ErrNotFound):
		ss.no("NONEXISTENT", "Script does not exist")
	case errors.Is(err, store.ErrConflict):
		ss.no("ALREADYEXISTS", "Script already exists")
	case errors.Is(err, store.ErrSieveActive):
		ss.no("ACTIVE", "Script is active")
	case errors.Is(err, store.ErrSieveQuota):
		ss.no("QUOTA/MAXSCRIPTS", "Too many scripts")
	case errors.Is(err, store.ErrSieveName):
		ss.no("", "Invalid script name")
	default:
		ss.log.Error("managesieve command failed", "err", err)
		ss.no("TRYLATER", "Internal error")
	}
}

func (ss *session) authenticate(args []string) bool {
	if ss.acct != nil {
		ss.no("", "Already authenticated")
		return false
	}
	if !ss.tls && !ss.s.AllowInsecureAuth {
		ss.no("ENCRYPT-NEEDED", "Use STARTTLS first")
		return false
	}
	if len(args) == 0 || !strings.EqualFold(args[0], "PLAIN") {
		ss.no("", "Unsupported mechanism")
		return false
	}
	var resp string
	switch len(args) {
	case 1:
		ss.send(`""`)
		_ = ss.conn.SetReadDeadline(time.Now().Add(2 * time.Minute))
		_, rargs, err := ss.readContinuation()
		if err != nil {
			return true
		}
		if len(rargs) != 1 || rargs[0] == "*" {
			ss.no("", "Authentication cancelled")
			return false
		}
		resp = rargs[0]
	case 2:
		resp = args[1]
	default:
		ss.no("", "Invalid arguments")
		return false
	}
	dec, err := base64.StdEncoding.DecodeString(strings.TrimSpace(resp))
	parts := strings.Split(string(dec), "\x00")
	if err != nil || len(parts) != 3 {
		ss.no("", "Invalid response")
		return false
	}
	if parts[0] != "" && !strings.EqualFold(parts[0], parts[1]) {
		ss.no("", "Authorization identity not permitted")
		return false
	}
	acct, err := ss.s.Auth(ss.ctx, ss.ip, parts[1], parts[2])
	if err != nil {
		metrics.AuthAttempts.WithLabelValues("managesieve", "failure").Inc()
		ss.log.Warn("authentication failed", "user", parts[1])
		time.Sleep(time.Second)
		ss.fails++
		if ss.fails >= maxAuthFails {
			ss.send(`BYE "Too many failed attempts"`)
			return true
		}
		ss.no("", "Authentication failed")
		return false
	}
	metrics.AuthAttempts.WithLabelValues("managesieve", "success").Inc()
	ss.acct = acct
	ss.log = ss.log.With("user", acct.Email)
	ss.log.Info("authenticated")
	ss.ok("Authenticated")
	return false
}

// readContinuation reads a client response line consisting of one string
// (quoted or literal) during SASL authentication.
func (ss *session) readContinuation() (string, []string, error) {
	c, err := ss.r.ReadByte()
	if err != nil {
		return "", nil, err
	}
	switch c {
	case '"':
		s, _, err := ss.readQuoted()
		if err != nil {
			return "", nil, err
		}
		return "", []string{s}, ss.skipLine(nil)
	case '{':
		s, err := ss.readLiteral(maxPreAuthLit)
		if err != nil {
			return "", nil, err
		}
		return "", []string{s}, ss.skipLine(nil)
	}
	_ = ss.r.UnreadByte()
	atom, err := ss.readAtom()
	if err != nil {
		return "", nil, err
	}
	return "", []string{atom}, ss.skipLine(nil)
}
