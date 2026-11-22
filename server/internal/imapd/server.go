// Package imapd is the IMAP4rev1/IMAP4rev2 server (RFC 3501, RFC 9051) with
// IDLE, MOVE, UIDPLUS, SPECIAL-USE and LIST-STATUS. Protocol handling comes
// from go-imap's imapserver; this package maps it onto the Postwerk store.
package imapd

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"log/slog"
	"net"

	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapserver"

	"postwerk/internal/delivery"
	"postwerk/internal/store"
)

// Delim is the hierarchy delimiter for mailbox names.
const Delim = '/'

type Server struct {
	Addr        string
	ImplicitTLS bool // IMAPS (993) instead of STARTTLS (143)
	TLSConfig   *tls.Config
	Store       *store.Store
	Delivery    *delivery.Service
	Auth        func(ctx context.Context, ip, user, pass string) (*store.Account, error)
	Banned      func(ip string) bool
	MaxSize     func() int64
	// AllowInsecureAuth permits LOGIN on unencrypted connections (tests only).
	AllowInsecureAuth bool
	// MaxConns and MaxConnsPerIP limit concurrent connections (0 = default).
	MaxConns, MaxConnsPerIP int
	Log                     *slog.Logger
}

var caps = imap.CapSet{
	imap.CapIMAP4rev1:    {},
	imap.CapIMAP4rev2:    {},
	imap.CapNamespace:    {},
	imap.CapUIDPlus:      {},
	imap.CapESearch:      {},
	imap.CapSearchRes:    {},
	imap.CapListExtended: {},
	imap.CapListStatus:   {},
	imap.CapMove:         {},
	imap.CapStatusSize:   {},
	imap.CapBinary:       {},
	imap.CapChildren:     {},
	imap.CapSpecialUse:   {},
	imap.CapLiteralPlus:  {},
}

type logAdapter struct{ l *slog.Logger }

func (a logAdapter) Printf(format string, args ...any) { a.l.Warn(fmt.Sprintf(format, args...)) }

func (s *Server) ListenAndServe(ctx context.Context) error {
	ln, err := net.Listen("tcp", s.Addr)
	if err != nil {
		return err
	}
	ln = newLimitListener(ln, s.MaxConns, s.MaxConnsPerIP)
	proto := "imap"
	opts := &imapserver.Options{
		NewSession:   func(c *imapserver.Conn) (imapserver.Session, *imapserver.GreetingData, error) { return s.newSession(ctx, c) },
		Caps:         caps,
		Logger:       logAdapter{s.Log},
		InsecureAuth: s.AllowInsecureAuth,
	}
	if s.ImplicitTLS {
		ln = tls.NewListener(ln, s.TLSConfig)
		proto = "imaps"
	} else {
		opts.TLSConfig = s.TLSConfig
	}
	srv := imapserver.New(opts)
	s.Log.Info("listening", "proto", proto, "addr", s.Addr)
	go func() {
		<-ctx.Done()
		_ = srv.Close()
	}()
	err = srv.Serve(ln)
	if ctx.Err() != nil || errors.Is(err, net.ErrClosed) {
		return nil
	}
	return err
}

func (s *Server) newSession(ctx context.Context, c *imapserver.Conn) (imapserver.Session, *imapserver.GreetingData, error) {
	ip, _, _ := net.SplitHostPort(c.NetConn().RemoteAddr().String())
	if s.Banned != nil && s.Banned(ip) {
		return nil, nil, &imap.Error{Type: imap.StatusResponseTypeBye, Text: "too many failed attempts"}
	}
	return &session{srv: s, conn: c, ip: ip, ctx: ctx, log: s.Log.With("proto", "imap", "ip", ip)}, nil, nil
}
