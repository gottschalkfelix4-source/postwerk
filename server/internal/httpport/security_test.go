package httpport

import (
	"context"
	"crypto/tls"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"postwerk/internal/store"
	"postwerk/internal/tlsutil"
)

func TestOpenUpgradesToRedirectWithValidCert(t *testing.T) {
	ctx := context.Background()
	host := "mail.example.test"
	var current tls.Certificate
	httpsLn, err := tls.Listen("tcp", "127.0.0.1:0", &tls.Config{GetCertificate: func(*tls.ClientHelloInfo) (*tls.Certificate, error) { return &current, nil }})
	if err != nil {
		t.Fatal(err)
	}
	defer httpsLn.Close()
	go http.Serve(httpsLn, http.NotFoundHandler())

	st, _ := store.Open(t.TempDir())
	defer st.Close()
	settings, _ := st.LoadSettings(ctx, host)
	cfg := settings.Get()
	if cfg.TLS.HTTPPort != "open" {
		t.Fatalf("default = %s", cfg.TLS.HTTPPort)
	}
	cfg.TLS.Mode = "acme"
	settings.Save(ctx, cfg)
	setupDone := false
	c := &Controller{Addr: freePort(t), HTTPSAddr: httpsLn.Addr().String(), Handler: http.NotFoundHandler(), Settings: settings,
		TLS:       &tlsutil.Dynamic{DataDir: t.TempDir(), Settings: func() store.TLSSettings { return settings.Get().TLS }, Hostname: func() string { return host }},
		SetupDone: func() bool { return setupDone }, Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	c.init()
	defer c.stop()

	check := func(want State, wantValid bool, msg string) {
		t.Helper()
		c.Notify()
		c.apply(ctx)
		if s, reason := c.Status(); s != want || c.ValidCert() != wantValid {
			t.Fatalf("%s: state=%s valid=%v (%s)", msg, s, c.ValidCert(), reason)
		}
	}
	current = caSigned(t, host, time.Now().AddDate(0, 2, 0), false)
	check(Full, true, "setup not completed: wizard must stay reachable via HTTP")
	setupDone = true
	check(Redirect, true, "valid certificate after setup")
	if !c.ForcesHTTPS() {
		t.Fatal("ForcesHTTPS must be true")
	}
	current = caSigned(t, host, time.Now().AddDate(0, 2, 0), true)
	check(Full, false, "self-signed certificate only")
	if c.ForcesHTTPS() {
		t.Fatal("ForcesHTTPS must be false without a valid certificate")
	}
	// Certificate files (e.g. from a reverse proxy) never trigger the redirect.
	current = caSigned(t, host, time.Now().AddDate(0, 2, 0), false)
	cfg.TLS.Mode = "files"
	settings.Save(ctx, cfg)
	check(Full, true, "certificate files")
}

func TestForwardedProtoNeedsTrustedProxy(t *testing.T) {
	ctx := context.Background()
	st, _ := store.Open(t.TempDir())
	defer st.Close()
	settings, _ := st.LoadSettings(ctx, "mail.example.test")
	cfg := settings.Get()
	cfg.TLS.HTTPPort = "redirect"
	cfg.Web.BehindProxy = true
	cfg.Web.TrustedProxies = []string{"192.0.2.10"}
	settings.Save(ctx, cfg)
	c := &Controller{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, "ui") }), Settings: settings,
		TLS: &tlsutil.Dynamic{DataDir: t.TempDir(), Settings: func() store.TLSSettings { return settings.Get().TLS }, Hostname: func() string { return "mail.example.test" }},
		Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	c.init()
	c.state = Redirect
	h := c.handler()
	serve := func(remote string) int {
		r := httptest.NewRequest("GET", "http://mail.example.test/admin", nil)
		r.RemoteAddr = remote
		r.Header.Set("X-Forwarded-Proto", "https")
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w.Code
	}
	if code := serve("203.0.113.5:4000"); code != http.StatusMovedPermanently {
		t.Fatalf("untrusted X-Forwarded-Proto honoured: %d", code)
	}
	if code := serve("192.0.2.10:4000"); code != 200 {
		t.Fatalf("trusted proxy request not served: %d", code)
	}
}

func TestTrustedProxyDefaultsToLoopback(t *testing.T) {
	cases := []struct {
		ip   string
		list []string
		want bool
	}{
		{"127.0.0.1", nil, true},
		{"::1", nil, true},
		{"172.18.0.2", nil, false},
		{"10.0.0.1", nil, false},
		{"172.18.0.2", []string{"172.18.0.0/16"}, true},
		{"127.0.0.1", []string{"172.18.0.0/16"}, false},
		{"192.0.2.1", []string{" 192.0.2.1 "}, true},
	}
	for _, c := range cases {
		if got := TrustedProxy(net.ParseIP(c.ip), c.list); got != c.want {
			t.Errorf("TrustedProxy(%s, %v) = %v, want %v", c.ip, c.list, got, c.want)
		}
	}
}
