// Package httpport controls the plain HTTP listener (host port 80). It can
// serve the full UI, only answer ACME challenges and redirect to HTTPS, or
// close the port entirely once a valid certificate exists – and reopen it in
// time for the renewal.
package httpport

import (
	"context"
	"crypto/x509"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"postwerk/internal/store"
	"postwerk/internal/tlsutil"
)

// ReopenBefore is how long before expiry the port is reopened so the
// certificate can be renewed (autocert renews 30 days before expiry).
const ReopenBefore = 32 * 24 * time.Hour

type State string

const (
	Full     State = "open"
	Redirect State = "redirect"
	Closed   State = "closed"
)

type Controller struct {
	Addr      string // e.g. ":8080"
	HTTPSAddr string // own HTTPS listener used to probe the certificate
	Handler   http.Handler
	Settings  *store.SettingsCache
	TLS       *tlsutil.Dynamic
	ForceOpen bool // POSTWERK_HTTP_ALWAYS_OPEN
	Log       *slog.Logger

	mu        sync.Mutex
	state     State
	srv       *http.Server
	reason    string
	cert      *x509.Certificate
	certErr   error
	probedAt  time.Time
	probeHost string
	wake      chan struct{}
}

func (c *Controller) init() {
	if c.wake == nil {
		c.wake = make(chan struct{}, 1)
	}
}

// Notify re-evaluates the state soon (e.g. after settings changed).
func (c *Controller) Notify() {
	c.init()
	c.mu.Lock()
	c.probedAt = time.Time{}
	c.mu.Unlock()
	select {
	case c.wake <- struct{}{}:
	default:
	}
}

func (c *Controller) Run(ctx context.Context) {
	c.init()
	for {
		_ = c.Settings.Reload(ctx)
		c.apply(ctx)
		select {
		case <-ctx.Done():
			c.stop()
			return
		case <-c.wake:
		case <-time.After(time.Minute):
		}
	}
}

// Probe returns the certificate currently served for the mail hostname,
// re-checking at most every 10 minutes (force: always).
func (c *Controller) Probe(ctx context.Context, force bool) (*x509.Certificate, error) {
	host := c.Settings.Get().Server.Hostname
	c.mu.Lock()
	fresh := !force && host == c.probeHost && time.Since(c.probedAt) < 10*time.Minute
	cert, err := c.cert, c.certErr
	c.mu.Unlock()
	if fresh {
		return cert, err
	}
	if c.HTTPSAddr == "" {
		return nil, errors.New("kein HTTPS-Port aktiv")
	}
	addr := c.HTTPSAddr
	if strings.HasPrefix(addr, ":") {
		addr = "127.0.0.1" + addr
	}
	pctx, cancel := context.WithTimeout(ctx, 2*time.Minute) // ACME issuance can take a while
	defer cancel()
	cert, err = tlsutil.Probe(pctx, addr, host)
	if err == nil && !tlsutil.IsPublicCA(cert, host) {
		err = errors.New("noch kein gültiges Zertifikat (selbstsigniertes Ersatzzertifikat aktiv)")
	}
	c.mu.Lock()
	c.cert, c.certErr, c.probedAt, c.probeHost = cert, err, time.Now(), host
	c.mu.Unlock()
	return cert, err
}

// desired decides the listener state from settings and certificate.
func (c *Controller) desired(ctx context.Context) (State, string) {
	s := c.Settings.Get()
	if c.ForceOpen {
		return Full, "POSTWERK_HTTP_ALWAYS_OPEN ist gesetzt – Port 80 bleibt offen"
	}
	switch s.TLS.HTTPPort {
	case "redirect":
		return Redirect, "Port 80 leitet auf HTTPS um und beantwortet nur Let's-Encrypt-Prüfungen"
	case "auto-close":
	default:
		return Full, "Port 80 ist offen (Weboberfläche auch über HTTP)"
	}
	// auto-close: only when it is safe.
	switch {
	case s.TLS.Mode != "acme":
		return Redirect, "Port 80 leitet um – automatisches Schließen gibt es nur mit „Let's Encrypt direkt“"
	case s.Web.BehindProxy:
		return Redirect, "Port 80 leitet um – hinter einem Reverse-Proxy wird er nicht geschlossen"
	case c.HTTPSAddr == "":
		return Redirect, "Port 80 leitet um – ohne aktiven HTTPS-Port wird er nicht geschlossen"
	}
	cert, err := c.Probe(ctx, false)
	if err != nil {
		return Redirect, "Port 80 offen für Let's Encrypt – noch kein gültiges Zertifikat (" + err.Error() + ")"
	}
	reopen := cert.NotAfter.Add(-ReopenBefore)
	if time.Now().After(reopen) {
		return Redirect, fmt.Sprintf("Port 80 vorübergehend offen zur Verlängerung (Zertifikat gültig bis %s)", cert.NotAfter.Local().Format("02.01.2006"))
	}
	return Closed, fmt.Sprintf("Port 80 geschlossen – öffnet sich am %s automatisch zur Verlängerung (Zertifikat gültig bis %s)",
		reopen.Local().Format("02.01.2006"), cert.NotAfter.Local().Format("02.01.2006"))
}

func (c *Controller) apply(ctx context.Context) {
	state, reason := c.desired(ctx)
	c.mu.Lock()
	prev := c.state
	c.state, c.reason = state, reason
	running := c.srv != nil
	c.mu.Unlock()
	if prev != state {
		c.Log.Info("http port state changed", "component", "http", "state", string(state), "reason", reason)
	}
	switch {
	case state == Closed && running:
		c.stop()
	case state != Closed && !running:
		c.start()
	}
}

func (c *Controller) start() {
	srv := &http.Server{Addr: c.Addr, Handler: c.handler(), ReadHeaderTimeout: 10 * time.Second}
	ln, err := net.Listen("tcp", c.Addr)
	if err != nil {
		c.Log.Error("http listener failed", "component", "http", "addr", c.Addr, "err", err)
		return
	}
	c.mu.Lock()
	c.srv = srv
	c.mu.Unlock()
	c.Log.Info("listening", "component", "http", "proto", "http", "addr", c.Addr)
	go func() {
		if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			c.Log.Error("http listener stopped", "component", "http", "err", err)
		}
	}()
}

func (c *Controller) stop() {
	c.mu.Lock()
	srv := c.srv
	c.srv = nil
	c.mu.Unlock()
	if srv == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_ = srv.Shutdown(ctx)
	c.Log.Info("http listener closed", "component", "http", "addr", c.Addr)
}

// handler serves the UI or only ACME + redirect, depending on the state.
func (c *Controller) handler() http.Handler {
	full := c.TLS.HTTPHandler(c.Handler)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c.mu.Lock()
		st := c.state
		c.mu.Unlock()
		// Behind a reverse proxy the request already arrived via HTTPS: serving
		// it normally avoids a redirect loop.
		viaHTTPS := strings.EqualFold(strings.TrimSpace(strings.Split(r.Header.Get("X-Forwarded-Proto"), ",")[0]), "https")
		if st == Full || viaHTTPS || strings.HasPrefix(r.URL.Path, "/.well-known/acme-challenge/") || r.URL.Path == "/healthz" {
			full.ServeHTTP(w, r)
			return
		}
		host := r.Host
		if h, _, err := net.SplitHostPort(host); err == nil {
			host = h
		}
		if host == "" {
			host = c.Settings.Get().Server.Hostname
		}
		http.Redirect(w, r, "https://"+host+r.URL.RequestURI(), http.StatusMovedPermanently)
	})
}

// Refresh re-evaluates and applies the state immediately and returns it.
func (c *Controller) Refresh(ctx context.Context) (State, string) {
	c.init()
	c.apply(ctx)
	return c.Status()
}

// Status describes the current state for the UI.
func (c *Controller) Status() (State, string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.state == "" {
		return Full, "Port 80 ist offen"
	}
	return c.state, c.reason
}
