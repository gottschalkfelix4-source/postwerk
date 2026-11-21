// Package smtp implements the SMTP server (RFC 5321) used for inbound mail
// (port 25) and authenticated submission (ports 587/465).
package smtp

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
	"time"

	"postwerk/internal/metrics"
	"postwerk/internal/store"
)

type Mode int

const (
	ModeInbound Mode = iota
	ModeSubmission
)

type Reply struct {
	Code     int
	Enhanced string
	Text     string
}

func (r *Reply) Error() string { return fmt.Sprintf("%d %s %s", r.Code, r.Enhanced, r.Text) }

func NewReply(code int, enh, text string) *Reply { return &Reply{code, enh, text} }

type Envelope struct {
	ID       string
	Helo     string
	RemoteIP net.IP
	TLS      bool
	Mode     Mode
	Account  *store.Account
	From     string
	Rcpts    []string
}

// Backend implements the policy and delivery side of the server.
type Backend interface {
	Authenticate(ctx context.Context, ip, user, pass string) (*store.Account, error)
	Banned(ip string) bool
	CheckSender(ctx context.Context, env *Envelope, from string) *Reply
	CheckRecipient(ctx context.Context, env *Envelope, rcpt string) *Reply
	Deliver(ctx context.Context, env *Envelope, data []byte) *Reply
}

type Server struct {
	Addr              string
	Mode              Mode
	ImplicitTLS       bool
	TLSConfig         *tls.Config
	Hostname          func() string
	MaxSize           func() int64
	Backend           Backend
	Log               *slog.Logger
	AllowInsecureAuth bool
	MaxRcpts          int

	// Resource limits; zero selects the default.
	MaxConns       int           // concurrent connections in total (default 500)
	MaxConnsPerIP  int           // concurrent connections per client address (default 20)
	MaxLineLength  int           // maximum message line length without CRLF (default 4096)
	MaxSessionTime time.Duration // maximum lifetime of a session (default 30 minutes)
	MaxJunkCmds    int           // NOOP/RSET/VRFY/HELO commands without a delivered message (default 100)

	limiter connLimiter
}

func orDefault(v, def int) int {
	if v > 0 {
		return v
	}
	return def
}

func (s *Server) proto() string {
	switch {
	case s.Mode == ModeInbound:
		return "smtp"
	case s.ImplicitTLS:
		return "submissions"
	}
	return "submission"
}

func (s *Server) ListenAndServe(ctx context.Context) error {
	ln, err := net.Listen("tcp", s.Addr)
	if err != nil {
		return err
	}
	s.Log.Info("listening", "proto", s.proto(), "addr", s.Addr)
	return s.Serve(ctx, ln)
}

// Serve accepts connections on ln until ctx is cancelled.
func (s *Server) Serve(ctx context.Context, ln net.Listener) error {
	if s.ImplicitTLS {
		ln = tls.NewListener(ln, s.TLSConfig)
	}
	go func() { <-ctx.Done(); ln.Close() }()
	for {
		c, err := ln.Accept()
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			var ne net.Error
			if errors.As(err, &ne) && ne.Timeout() {
				time.Sleep(100 * time.Millisecond)
				continue
			}
			return err
		}
		metrics.Connections.WithLabelValues(s.proto()).Inc()
		key := ipKey(remoteIP(c))
		if !s.limiter.acquire(key, orDefault(s.MaxConns, defaultMaxConns), orDefault(s.MaxConnsPerIP, defaultMaxConnsPerIP)) {
			go s.rejectConn(c)
			continue
		}
		go func() {
			defer s.limiter.release(key)
			s.serve(ctx, c)
		}()
	}
}

// rejectConn tells a client that exceeds the connection limits to come back
// later and closes the connection.
func (s *Server) rejectConn(c net.Conn) {
	defer c.Close()
	_ = c.SetDeadline(time.Now().Add(5 * time.Second))
	_, _ = io.WriteString(c, "421 4.7.0 Too many connections, try again later\r\n")
	s.Log.Debug("connection limit reached", "proto", s.proto(), "ip", remoteIP(c).String())
}

type session struct {
	s      *Server
	conn   net.Conn
	r      *bufio.Reader
	w      *bufio.Writer
	env    *Envelope
	log    *slog.Logger
	ctx    context.Context
	helo   bool
	errors int
	// mailGiven is set after a successful MAIL command. It is tracked
	// separately because the null sender is stored as an empty env.From.
	mailGiven bool
	junk      int
	abort     bool // the connection must be closed (e.g. failed read in AUTH)
	deadline  time.Time
}

func (s *Server) serve(ctx context.Context, c net.Conn) {
	defer c.Close()
	ip := remoteIP(c)
	ss := &session{s: s, conn: c, r: bufio.NewReaderSize(c, 4096), w: bufio.NewWriter(c), ctx: ctx}
	ss.env = &Envelope{RemoteIP: ip, Mode: s.Mode, TLS: s.ImplicitTLS, ID: newID()}
	ss.log = s.Log.With("proto", s.proto(), "session", ss.env.ID, "ip", ip.String())
	defer func() {
		if r := recover(); r != nil {
			ss.log.Error("panic in smtp session", "err", r)
		}
	}()
	if s.Backend.Banned(ip.String()) {
		ss.reply(421, "4.7.0", "Too many failed attempts, try again later")
		return
	}
	ss.log.Debug("connection opened")
	maxSession := s.MaxSessionTime
	if maxSession <= 0 {
		maxSession = defaultSessionMinutes * time.Minute
	}
	ss.deadline = time.Now().Add(maxSession)
	ss.reply(220, "", s.Hostname()+" ESMTP Postwerk ready")
	for {
		line, err := ss.readLine()
		if err != nil {
			switch {
			case errors.Is(err, errLineTooLong):
				ss.reply(500, "5.5.2", "Line too long")
			case time.Now().After(ss.deadline):
				ss.reply(421, "4.4.2", "Session time limit exceeded")
			case !errors.Is(err, io.EOF):
				ss.log.Debug("read error", "err", err)
			}
			return
		}
		cmd, arg := splitCmd(line)
		if quit := ss.handle(cmd, arg); quit {
			return
		}
		if ss.errors > 10 {
			ss.reply(421, "4.7.0", "Too many errors")
			return
		}
		if ss.junk > orDefault(s.MaxJunkCmds, defaultMaxJunkCmds) {
			ss.reply(421, "4.7.0", "Too many commands without progress")
			return
		}
	}
}

// readLine reads a command line with a hard length limit. The read deadline
// is the idle timeout, capped by the session lifetime.
func (ss *session) readLine() (string, error) {
	dl := time.Now().Add(5 * time.Minute)
	if dl.After(ss.deadline) {
		dl = ss.deadline
	}
	_ = ss.conn.SetReadDeadline(dl)
	return readLimitedLine(ss.r, maxCommandLine)
}

func (ss *session) reply(code int, enh, text string) {
	lines := strings.Split(text, "\n")
	for i, l := range lines {
		sep := "-"
		if i == len(lines)-1 {
			sep = " "
		}
		if enh != "" {
			fmt.Fprintf(ss.w, "%d%s%s %s\r\n", code, sep, enh, l)
		} else {
			fmt.Fprintf(ss.w, "%d%s%s\r\n", code, sep, l)
		}
	}
	_ = ss.conn.SetWriteDeadline(time.Now().Add(time.Minute))
	_ = ss.w.Flush()
}

func (ss *session) replyR(r *Reply) {
	if r.Code >= 400 {
		ss.errors++
	}
	ss.reply(r.Code, r.Enhanced, r.Text)
}

func splitCmd(line string) (string, string) {
	if i := strings.IndexByte(line, ' '); i >= 0 {
		return strings.ToUpper(line[:i]), strings.TrimSpace(line[i+1:])
	}
	return strings.ToUpper(line), ""
}

func (ss *session) resetTxn() {
	ss.env.From = ""
	ss.env.Rcpts = nil
	ss.mailGiven = false
}

func (ss *session) authAllowed() bool {
	return ss.s.Mode == ModeSubmission && (ss.env.TLS || ss.s.AllowInsecureAuth)
}

func (ss *session) handle(cmd, arg string) bool {
	switch cmd {
	case "HELO", "EHLO":
		if arg == "" {
			ss.replyR(NewReply(501, "5.5.4", "Syntax: "+cmd+" hostname"))
			return false
		}
		ss.junk++
		ss.env.Helo = arg
		ss.helo = true
		ss.resetTxn()
		if cmd == "HELO" {
			ss.reply(250, "", ss.s.Hostname())
			return false
		}
		ext := []string{ss.s.Hostname() + " greets " + arg, "PIPELINING", "8BITMIME", "SMTPUTF8", "ENHANCEDSTATUSCODES",
			"SIZE " + strconv.FormatInt(ss.s.MaxSize(), 10)}
		if !ss.env.TLS && ss.s.TLSConfig != nil {
			ext = append(ext, "STARTTLS")
		}
		if ss.authAllowed() && ss.env.Account == nil {
			ext = append(ext, "AUTH PLAIN LOGIN")
		}
		ss.reply(250, "", strings.Join(ext, "\n"))
	case "STARTTLS":
		if ss.env.TLS || ss.s.TLSConfig == nil {
			ss.replyR(NewReply(503, "5.5.1", "TLS not available"))
			return false
		}
		ss.reply(220, "2.0.0", "Ready to start TLS")
		tc := tls.Server(ss.conn, ss.s.TLSConfig)
		_ = tc.SetDeadline(time.Now().Add(30 * time.Second))
		if err := tc.Handshake(); err != nil {
			ss.log.Debug("tls handshake failed", "err", err)
			return true
		}
		_ = tc.SetDeadline(time.Time{})
		ss.conn = tc
		ss.r = bufio.NewReaderSize(tc, 4096)
		ss.w = bufio.NewWriter(tc)
		ss.env.TLS = true
		ss.helo = false
		ss.resetTxn()
	case "AUTH":
		ss.handleAuth(arg)
		return ss.abort
	case "MAIL":
		ss.handleMail(arg)
	case "RCPT":
		ss.handleRcpt(arg)
	case "DATA":
		return ss.handleData()
	case "RSET":
		ss.junk++
		ss.resetTxn()
		ss.reply(250, "2.0.0", "OK")
	case "NOOP":
		ss.junk++
		ss.reply(250, "2.0.0", "OK")
	case "VRFY":
		ss.junk++
		ss.reply(252, "2.5.0", "Cannot VRFY user, but will accept message")
	case "QUIT":
		ss.reply(221, "2.0.0", "Bye")
		return true
	case "BDAT":
		ss.replyR(NewReply(502, "5.5.1", "CHUNKING not supported"))
	default:
		ss.replyR(NewReply(500, "5.5.2", "Command not recognized"))
	}
	return false
}

func (ss *session) handleAuth(arg string) {
	if !ss.authAllowed() {
		ss.replyR(NewReply(503, "5.5.1", "AUTH not available, use STARTTLS first"))
		return
	}
	if ss.env.Account != nil {
		ss.replyR(NewReply(503, "5.5.1", "Already authenticated"))
		return
	}
	mech, initial := splitCmd(arg)
	var user, pass string
	switch mech {
	case "PLAIN":
		resp := initial
		if resp == "" {
			ss.reply(334, "", "")
			l, err := ss.readLine()
			if err != nil {
				ss.abort = true
				return
			}
			resp = l
		}
		if resp == "*" {
			ss.replyR(NewReply(501, "5.0.0", "Authentication cancelled"))
			return
		}
		dec, err := base64.StdEncoding.DecodeString(resp)
		if err != nil {
			ss.replyR(NewReply(501, "5.5.2", "Invalid base64"))
			return
		}
		parts := strings.Split(string(dec), "\x00")
		if len(parts) != 3 {
			ss.replyR(NewReply(501, "5.5.2", "Invalid PLAIN response"))
			return
		}
		user, pass = parts[1], parts[2]
	case "LOGIN":
		ss.reply(334, "", "VXNlcm5hbWU6")
		l, err := ss.readLine()
		if err != nil {
			ss.abort = true
			return
		}
		u, err := base64.StdEncoding.DecodeString(l)
		if err != nil {
			ss.replyR(NewReply(501, "5.5.2", "Invalid base64"))
			return
		}
		ss.reply(334, "", "UGFzc3dvcmQ6")
		l, err = ss.readLine()
		if err != nil {
			ss.abort = true
			return
		}
		p, err := base64.StdEncoding.DecodeString(l)
		if err != nil {
			ss.replyR(NewReply(501, "5.5.2", "Invalid base64"))
			return
		}
		user, pass = string(u), string(p)
	default:
		ss.replyR(NewReply(504, "5.5.4", "Unrecognized authentication type"))
		return
	}
	acct, err := ss.s.Backend.Authenticate(ss.ctx, ss.env.RemoteIP.String(), user, pass)
	if err != nil {
		metrics.AuthAttempts.WithLabelValues(ss.s.proto(), "failure").Inc()
		ss.log.Warn("authentication failed", "user", user, "err", err)
		time.Sleep(time.Second)
		ss.replyR(NewReply(535, "5.7.8", "Authentication credentials invalid"))
		return
	}
	metrics.AuthAttempts.WithLabelValues(ss.s.proto(), "success").Inc()
	ss.env.Account = acct
	ss.log.Info("authenticated", "user", acct.Email)
	ss.reply(235, "2.7.0", "Authentication successful")
}

// parsePath extracts the address from "FROM:<addr> PARAMS" style arguments.
func parsePath(arg, prefix string) (string, map[string]string, bool) {
	if len(arg) < len(prefix) || !strings.EqualFold(arg[:len(prefix)], prefix) {
		return "", nil, false
	}
	rest := strings.TrimSpace(arg[len(prefix):])
	if !strings.HasPrefix(rest, "<") {
		// Be lenient with clients that omit brackets.
		f := strings.Fields(rest)
		if len(f) == 0 {
			return "", nil, false
		}
		return strings.ToLower(f[0]), parseParams(f[1:]), true
	}
	end := strings.IndexByte(rest, '>')
	if end < 0 {
		return "", nil, false
	}
	addr := rest[1:end]
	// Strip source routes (@a,@b:user@host).
	if i := strings.IndexByte(addr, ':'); i >= 0 && strings.HasPrefix(addr, "@") {
		addr = addr[i+1:]
	}
	return strings.ToLower(addr), parseParams(strings.Fields(rest[end+1:])), true
}

func parseParams(f []string) map[string]string {
	m := map[string]string{}
	for _, p := range f {
		k, v, _ := strings.Cut(p, "=")
		m[strings.ToUpper(k)] = v
	}
	return m
}

func (ss *session) handleMail(arg string) {
	if !ss.helo {
		ss.replyR(NewReply(503, "5.5.1", "Send EHLO first"))
		return
	}
	if ss.mailGiven {
		ss.replyR(NewReply(503, "5.5.1", "Nested MAIL command"))
		return
	}
	if ss.s.Mode == ModeSubmission && ss.env.Account == nil {
		ss.replyR(NewReply(530, "5.7.0", "Authentication required"))
		return
	}
	from, params, ok := parsePath(arg, "FROM:")
	if !ok || !validAddress(from) {
		ss.replyR(NewReply(501, "5.5.4", "Syntax: MAIL FROM:<address>"))
		return
	}
	if sz, ok := params["SIZE"]; ok {
		if n, err := strconv.ParseInt(sz, 10, 64); err == nil && n > ss.s.MaxSize() {
			ss.replyR(NewReply(552, "5.3.4", "Message size exceeds limit"))
			return
		}
	}
	if r := ss.s.Backend.CheckSender(ss.ctx, ss.env, from); r != nil {
		ss.replyR(r)
		return
	}
	// The null sender (MAIL FROM:<>) is kept as an empty string.
	ss.env.From = from
	ss.mailGiven = true
	ss.reply(250, "2.1.0", "Sender OK")
}

func (ss *session) handleRcpt(arg string) {
	if !ss.mailGiven {
		ss.replyR(NewReply(503, "5.5.1", "Need MAIL command first"))
		return
	}
	max := ss.s.MaxRcpts
	if max == 0 {
		max = 100
	}
	if len(ss.env.Rcpts) >= max {
		ss.replyR(NewReply(452, "4.5.3", "Too many recipients"))
		return
	}
	rcpt, _, ok := parsePath(arg, "TO:")
	if !ok || !strings.Contains(rcpt, "@") || !validAddress(rcpt) {
		ss.replyR(NewReply(501, "5.1.3", "Syntax: RCPT TO:<address>"))
		return
	}
	if r := ss.s.Backend.CheckRecipient(ss.ctx, ss.env, rcpt); r != nil {
		ss.replyR(r)
		return
	}
	ss.env.Rcpts = append(ss.env.Rcpts, rcpt)
	ss.reply(250, "2.1.5", "Recipient OK")
}

func (ss *session) handleData() bool {
	if len(ss.env.Rcpts) == 0 {
		ss.replyR(NewReply(503, "5.5.1", "Need RCPT command first"))
		return false
	}
	ss.reply(354, "", "Start mail input; end with <CRLF>.<CRLF>")
	_ = ss.conn.SetReadDeadline(time.Now().Add(10 * time.Minute))
	buf, res, err := readData(ss.r, ss.s.MaxSize(), orDefault(ss.s.MaxLineLength, defaultMaxLineLength))
	if err != nil {
		return true
	}
	switch res {
	case dataBareCR:
		ss.resetTxn()
		ss.log.Info("message rejected: bare CR")
		ss.replyR(NewReply(554, "5.5.2", "Bare CR not allowed, use CRLF line endings"))
		return false
	case dataLineTooLong:
		ss.resetTxn()
		ss.replyR(NewReply(552, "5.3.4", "Message line too long"))
		return false
	case dataTooBig:
		ss.resetTxn()
		ss.replyR(NewReply(552, "5.3.4", "Message size exceeds limit"))
		return false
	}
	ctx, cancel := context.WithTimeout(ss.ctx, 5*time.Minute)
	r := ss.s.Backend.Deliver(ctx, ss.env, buf)
	cancel()
	ss.resetTxn()
	if r != nil {
		ss.replyR(r)
		return false
	}
	ss.reply(250, "2.0.0", "OK queued as "+ss.env.ID)
	ss.env.ID = newID()
	ss.junk = 0
	return false
}

func remoteIP(c net.Conn) net.IP {
	if a, ok := c.RemoteAddr().(*net.TCPAddr); ok {
		return a.IP
	}
	host, _, _ := net.SplitHostPort(c.RemoteAddr().String())
	return net.ParseIP(host)
}

func newID() string {
	return strconv.FormatInt(time.Now().UnixNano(), 36)
}
