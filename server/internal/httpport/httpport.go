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
	// TrustProxy treats the listener as behind a reverse proxy even if the
	// setting is off (POSTWERK_TRUST_PROXY).
	TrustProxy bool
	// SetupDone reports whether the setup assistant was completed. While it
	// is not, "open" never redirects so the wizard stays reachable via HTTP.
	SetupDone func() bool
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

// ValidCert reports whether the last probe found a valid certificate from a
// public CA on the own HTTPS listener. It never blocks.
func (c *Controller) ValidCert() bool {
	if c == nil {
		return false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.cert != nil && c.certErr == nil && time.Now().Before(c.cert.NotAfter)
}

// ForcesHTTPS reports whether browsers only reach the web interface via
// HTTPS with a valid certificate (port 80 redirects or is closed).
func (c *Controller) ForcesHTTPS() bool {
	if c == nil {
		return false
	}
	st, _ := c.Status()
	return st != Full && c.ValidCert()
}

func (c *Controller) behindProxy() bool {
	return c.TrustProxy || c.Settings.Get().Web.BehindProxy
}

func (c *Controller) setupDone() bool { return c.SetupDone == nil || c.SetupDone() }

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
		// "open": plain HTTP only as long as there is no alternative. Once the
		// setup is completed and Postwerk itself serves a valid certificate
		// (Let's Encrypt or certificate files), browsers are sent to HTTPS, so
		// passwords and session cookies never travel unencrypted. Behind a
		// reverse proxy the proxy is responsible – redirecting there could
		// cause loops. Self-signed certificates fail the probe and keep HTTP.
		mode := s.TLS.Mode
		if c.TLS != nil && c.TLS.EnvCert != "" {
			mode = "files"
		}
		switch {
		case !c.setupDone():
			return Full, "Port 80 ist offen – der Setup-Assistent ist noch nicht abgeschlossen"
		case c.behindProxy():
			return Full, "Port 80 ist offen (hinter einem Reverse-Proxy kümmert sich der Proxy um HTTPS)"
		case mode != "acme" && mode != "files":
			return Full, "Port 80 ist offen (Weboberfläche auch über HTTP)"
		case c.HTTPSAddr == "":
			return Full, "Port 80 ist offen (kein HTTPS-Port aktiv)"
		}
		if _, err := c.Probe(ctx, false); err != nil {
			return Full, "Port 80 ist offen – noch kein gültiges Zertifikat, daher ist die Weboberfläche auch über HTTP erreichbar"
		}
		return Redirect, "Port 80 leitet auf HTTPS um, da ein gültiges Zertifikat vorliegt (Let's-Encrypt-Prüfungen werden weiter beantwortet)"
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
	if c.HTTPSAddr != "" {
		// Keep the certificate status fresh for HSTS and Secure cookies.
		_, _ = c.Probe(ctx, false)
	}
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
	srv := &http.Server{Addr: c.Addr, Handler: c.handler(), ReadHeaderTimeout: 10 * time.Second, IdleTimeout: 120 * time.Second}
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
		// it normally avoids a redirect loop. Only trusted proxies are believed.
		viaHTTPS := c.forwardedHTTPS(r)
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

// forwardedHTTPS reports whether a trusted reverse proxy says the original
// request used HTTPS.
func (c *Controller) forwardedHTTPS(r *http.Request) bool {
	if !c.behindProxy() {
		return false
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	return TrustedProxy(net.ParseIP(host), c.Settings.Get().Web.TrustedProxies) && ForwardedHTTPS(r)
}

// TrustedProxy decides whether X-Forwarded-* headers from ip may be believed.
// Without an explicit list only loopback addresses are trusted: in Docker
// setups every container shares the private network, so "all private
// networks" would let any neighbour spoof client addresses.
func TrustedProxy(ip net.IP, list []string) bool {
	if ip == nil {
		return false
	}
	if len(list) == 0 {
		return ip.IsLoopback()
	}
	for _, e := range list {
		e = strings.TrimSpace(e)
		if _, n, err := net.ParseCIDR(e); err == nil && n.Contains(ip) {
			return true
		}
		if p := net.ParseIP(e); p != nil && p.Equal(ip) {
			return true
		}
	}
	return false
}

// ForwardedHTTPS reports whether X-Forwarded-Proto names https (first value,
// i.e. what the client-facing proxy saw). Only meaningful for requests from
// a trusted proxy.
func ForwardedHTTPS(r *http.Request) bool {
	for _, v := range r.Header.Values("X-Forwarded-Proto") {
		first, _, _ := strings.Cut(v, ",")
		return strings.EqualFold(strings.TrimSpace(first), "https")
	}
	return false
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
