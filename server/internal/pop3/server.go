// Package pop3 implements a POP3 server (RFC 1939, RFC 2449, RFC 2595)
// serving each account's inbox.
package pop3

import (
	"bufio"
	"bytes"
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

type Authenticator func(ctx context.Context, ip, user, pass string) (*store.Account, error)

type Server struct {
	Addr              string
	ImplicitTLS       bool
	TLSConfig         *tls.Config
	Store             *store.Store
	Auth              Authenticator
	Banned            func(ip string) bool
	Hostname          func() string
	Log               *slog.Logger
	AllowInsecureAuth bool
}

func (s *Server) ListenAndServe(ctx context.Context) error {
	ln, err := net.Listen("tcp", s.Addr)
	if err != nil {
		return err
	}
	if s.ImplicitTLS {
		ln = tls.NewListener(ln, s.TLSConfig)
	}
	proto := "pop3"
	if s.ImplicitTLS {
		proto = "pop3s"
	}
	s.Log.Info("listening", "proto", proto, "addr", s.Addr)
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
		metrics.Connections.WithLabelValues(proto).Inc()
		go s.serve(ctx, c)
	}
}

type msgRef struct {
	id      int64
	size    int64
	hash    string
	uid     string
	deleted bool
}

type session struct {
	s     *Server
	conn  net.Conn
	r     *bufio.Reader
	w     *bufio.Writer
	ip    string
	tls   bool
	user  string
	acct  *store.Account
	msgs  []*msgRef
	log   *slog.Logger
	ctx   context.Context
}

func (s *Server) serve(ctx context.Context, c net.Conn) {
	defer c.Close()
	host, _, _ := net.SplitHostPort(c.RemoteAddr().String())
	ss := &session{s: s, conn: c, r: bufio.NewReader(c), w: bufio.NewWriter(c), ip: host, tls: s.ImplicitTLS, ctx: ctx}
	ss.log = s.Log.With("proto", "pop3", "ip", host)
	if s.Banned(host) {
		ss.err("too many failed attempts")
		return
	}
	ss.ok("Postwerk POP3 ready")
	for {
		_ = c.SetReadDeadline(time.Now().Add(10 * time.Minute))
		line, err := ss.r.ReadString('\n')
		if err != nil || len(line) > 1024 {
			return
		}
		line = strings.TrimRight(line, "\r\n")
		cmd, arg, _ := strings.Cut(line, " ")
		if ss.handle(strings.ToUpper(cmd), arg) {
			return
		}
	}
}

func (ss *session) send(s string) {
	ss.w.WriteString(s + "\r\n")
	_ = ss.w.Flush()
}
func (ss *session) ok(s string)  { ss.send("+OK " + s) }
func (ss *session) err(s string) { ss.send("-ERR " + s) }

func (ss *session) handle(cmd, arg string) bool {
	authed := ss.acct != nil
	switch cmd {
	case "CAPA":
		caps := []string{"+OK Capability list follows", "TOP", "UIDL", "RESP-CODES", "PIPELINING", "AUTH-RESP-CODE"}
		if !ss.tls && ss.s.TLSConfig != nil {
			caps = append(caps, "STLS")
		}
		if ss.tls || ss.s.AllowInsecureAuth {
			caps = append(caps, "USER", "SASL PLAIN")
		}
		caps = append(caps, ".")
		ss.send(strings.Join(caps, "\r\n"))
	case "STLS":
		if ss.tls || ss.s.TLSConfig == nil || authed {
			ss.err("STLS not available")
			return false
		}
		ss.ok("Begin TLS negotiation")
		tc := tls.Server(ss.conn, ss.s.TLSConfig)
		if err := tc.Handshake(); err != nil {
			return true
		}
		ss.conn, ss.r, ss.w, ss.tls = tc, bufio.NewReader(tc), bufio.NewWriter(tc), true
	case "USER":
		if authed || !(ss.tls || ss.s.AllowInsecureAuth) {
			ss.err("[AUTH] use STLS first")
			return false
		}
		ss.user = arg
		ss.ok("send PASS")
	case "PASS":
		if authed || ss.user == "" {
			ss.err("send USER first")
			return false
		}
		return ss.login(ss.user, arg)
	case "AUTH":
		if authed || !(ss.tls || ss.s.AllowInsecureAuth) {
			ss.err("[AUTH] use STLS first")
			return false
		}
		mech, initial, _ := strings.Cut(arg, " ")
		if !strings.EqualFold(mech, "PLAIN") {
			ss.err("unsupported mechanism")
			return false
		}
		if initial == "" {
			ss.send("+ ")
			l, err := ss.r.ReadString('\n')
			if err != nil {
				return true
			}
			initial = strings.TrimSpace(l)
		}
		dec, err := base64.StdEncoding.DecodeString(initial)
		parts := strings.Split(string(dec), "\x00")
		if err != nil || len(parts) != 3 {
			ss.err("invalid response")
			return false
		}
		return ss.login(parts[1], parts[2])
	case "QUIT":
		if authed {
			ss.commit()
		}
		ss.ok("bye")
		return true
	case "NOOP":
		if !authed {
			ss.err("not authenticated")
			return false
		}
		ss.ok("")
	default:
		if !authed {
			ss.err("not authenticated")
			return false
		}
		ss.transaction(cmd, arg)
	}
	return false
}

func (ss *session) login(user, pass string) bool {
	acct, err := ss.s.Auth(ss.ctx, ss.ip, user, pass)
	if err != nil {
		metrics.AuthAttempts.WithLabelValues("pop3", "failure").Inc()
		ss.log.Warn("authentication failed", "user", user)
		time.Sleep(time.Second)
		ss.err("[AUTH] invalid credentials")
		return false
	}
	metrics.AuthAttempts.WithLabelValues("pop3", "success").Inc()
	inbox, err := ss.s.Store.MailboxByRole(ss.ctx, acct.ID, "inbox")
	if err != nil {
		ss.err("[SYS/TEMP] mailbox unavailable")
		return true
	}
	list, err := ss.s.Store.ListMailboxMessages(ss.ctx, inbox)
	if err != nil {
		ss.err("[SYS/TEMP] mailbox unavailable")
		return true
	}
	for _, m := range list {
		ss.msgs = append(ss.msgs, &msgRef{id: m.ID, size: m.Size, hash: m.BlobHash, uid: strconv.FormatInt(m.ID, 10)})
	}
	ss.acct = acct
	ss.log.Info("authenticated", "user", acct.Email, "messages", len(ss.msgs))
	ss.ok(fmt.Sprintf("%d messages", len(ss.msgs)))
	return false
}

func (ss *session) get(arg string) (*msgRef, int, error) {
	n, err := strconv.Atoi(strings.TrimSpace(arg))
	if err != nil || n < 1 || n > len(ss.msgs) || ss.msgs[n-1].deleted {
		return nil, 0, errors.New("no such message")
	}
	return ss.msgs[n-1], n, nil
}

func (ss *session) transaction(cmd, arg string) {
	switch cmd {
	case "STAT":
		count, size := 0, int64(0)
		for _, m := range ss.msgs {
			if !m.deleted {
				count++
				size += m.size
			}
		}
		ss.ok(fmt.Sprintf("%d %d", count, size))
	case "LIST", "UIDL":
		val := func(m *msgRef) string {
			if cmd == "LIST" {
				return strconv.FormatInt(m.size, 10)
			}
			return m.uid
		}
		if arg != "" {
			m, n, err := ss.get(arg)
			if err != nil {
				ss.err(err.Error())
				return
			}
			ss.ok(fmt.Sprintf("%d %s", n, val(m)))
			return
		}
		lines := []string{"+OK"}
		for i, m := range ss.msgs {
			if !m.deleted {
				lines = append(lines, fmt.Sprintf("%d %s", i+1, val(m)))
			}
		}
		ss.send(strings.Join(append(lines, "."), "\r\n"))
	case "RETR", "TOP":
		f := strings.Fields(arg)
		if len(f) == 0 {
			ss.err("missing argument")
			return
		}
		m, _, err := ss.get(f[0])
		if err != nil {
			ss.err(err.Error())
			return
		}
		data, err := ss.s.Store.Blobs.Get(m.hash)
		if err != nil {
			ss.err("[SYS/TEMP] cannot read message")
			return
		}
		if cmd == "TOP" {
			n := 0
			if len(f) > 1 {
				n, _ = strconv.Atoi(f[1])
			}
			data = top(data, n)
		} else {
			seen := true
			_ = ss.s.Store.UpdateMessage(ss.ctx, ss.acct.ID, m.id, store.MessagePatch{Seen: &seen})
		}
		ss.w.WriteString("+OK message follows\r\n")
		writeDotted(ss.w, data)
		ss.w.WriteString(".\r\n")
		_ = ss.w.Flush()
	case "DELE":
		m, n, err := ss.get(arg)
		if err != nil {
			ss.err(err.Error())
			return
		}
		m.deleted = true
		ss.ok(fmt.Sprintf("message %d deleted", n))
	case "RSET":
		for _, m := range ss.msgs {
			m.deleted = false
		}
		ss.ok("")
	default:
		ss.err("unknown command")
	}
}

// commit applies DELE marks: messages move to the trash instead of being destroyed.
func (ss *session) commit() {
	trash, err := ss.s.Store.MailboxByRole(ss.ctx, ss.acct.ID, "trash")
	for _, m := range ss.msgs {
		if !m.deleted {
			continue
		}
		if err == nil {
			_ = ss.s.Store.UpdateMessage(ss.ctx, ss.acct.ID, m.id, store.MessagePatch{MailboxID: &trash})
		} else {
			_ = ss.s.Store.DeleteMessage(ss.ctx, ss.acct.ID, m.id)
		}
	}
}

func top(data []byte, n int) []byte {
	sep := bytes.Index(data, []byte("\r\n\r\n"))
	if sep < 0 {
		return data
	}
	head := data[:sep+4]
	body := data[sep+4:]
	lines := bytes.SplitAfter(body, []byte("\n"))
	if n < len(lines) {
		lines = lines[:n]
	}
	return append(append([]byte{}, head...), bytes.Join(lines, nil)...)
}

func writeDotted(w io.Writer, data []byte) {
	for _, line := range bytes.SplitAfter(data, []byte("\n")) {
		if len(line) == 0 {
			continue
		}
		if line[0] == '.' {
			w.Write([]byte("."))
		}
		w.Write(line)
		if !bytes.HasSuffix(line, []byte("\n")) {
			w.Write([]byte("\r\n"))
		}
	}
}
