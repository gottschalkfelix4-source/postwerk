package httpport

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"io"
	"log/slog"
	"math/big"
	"net"
	"net/http"
	"testing"
	"time"

	"postwerk/internal/store"
	"postwerk/internal/tlsutil"
)

// caSigned returns a leaf for host signed by a throwaway CA (like Let's Encrypt).
func caSigned(t *testing.T, host string, notAfter time.Time, selfSigned bool) tls.Certificate {
	t.Helper()
	caKey, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	caTmpl := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "Test CA", Organization: []string{"Test"}},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().AddDate(1, 0, 0), IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign}
	caDER, _ := x509.CreateCertificate(rand.Reader, caTmpl, caTmpl, &caKey.PublicKey, caKey)
	ca, _ := x509.ParseCertificate(caDER)
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	tmpl := &x509.Certificate{SerialNumber: big.NewInt(2), Subject: pkix.Name{CommonName: host}, DNSNames: []string{host},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: notAfter}
	parent, signer := ca, any(caKey)
	if selfSigned {
		tmpl.Subject.Organization = []string{"Postwerk"}
		parent, signer = tmpl, key
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, parent, &key.PublicKey, signer)
	if err != nil {
		t.Fatal(err)
	}
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}
}

func freePort(t *testing.T) string {
	l, _ := net.Listen("tcp", "127.0.0.1:0")
	a := l.Addr().String()
	l.Close()
	return a
}

func TestAutoCloseLifecycle(t *testing.T) {
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
	cfg.TLS.Mode = "acme"
	cfg.TLS.HTTPPort = "auto-close"
	settings.Save(ctx, cfg)

	addr := freePort(t)
	c := &Controller{Addr: addr, HTTPSAddr: httpsLn.Addr().String(), Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, "ui") }),
		Settings: settings, TLS: &tlsutil.Dynamic{DataDir: t.TempDir(), Settings: func() store.TLSSettings { return settings.Get().TLS }, Hostname: func() string { return host }},
		Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	c.init()

	reachable := func() bool {
		conn, err := net.DialTimeout("tcp", addr, time.Second)
		if err == nil {
			conn.Close()
		}
		return err == nil
	}

	// 1. Only the self-signed fallback: port must stay open (redirect) for ACME.
	current = caSigned(t, host, time.Now().AddDate(0, 3, 0), true)
	c.Notify()
	c.apply(ctx)
	if s, _ := c.Status(); s != Redirect || !reachable() {
		t.Fatalf("self-signed: state=%s reachable=%v", s, reachable())
	}
	// Redirect answers ACME paths and redirects the rest.
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := client.Get("http://" + addr + "/admin")
	if err != nil || resp.StatusCode != http.StatusMovedPermanently {
		t.Fatalf("expected redirect, got %v %v", resp, err)
	}

	// 2. Valid CA certificate with plenty of time: port closes.
	current = caSigned(t, host, time.Now().AddDate(0, 2, 0), false)
	c.Notify()
	c.apply(ctx)
	if s, reason := c.Status(); s != Closed || reachable() {
		t.Fatalf("valid cert: state=%s reason=%s reachable=%v", s, reason, reachable())
	}

	// 3. Renewal window (expires in 20 days): port reopens.
	current = caSigned(t, host, time.Now().Add(20*24*time.Hour), false)
	c.Notify()
	c.apply(ctx)
	if s, _ := c.Status(); s != Redirect || !reachable() {
		t.Fatalf("renewal window: state=%s reachable=%v", s, reachable())
	}

	// 4. Behind a reverse proxy the port is never closed.
	current = caSigned(t, host, time.Now().AddDate(0, 2, 0), false)
	cfg = settings.Get()
	cfg.Web.BehindProxy = true
	settings.Save(ctx, cfg)
	c.Notify()
	c.apply(ctx)
	if s, _ := c.Status(); s == Closed {
		t.Fatal("must not close behind a reverse proxy")
	}
	// Proxy forwards HTTPS traffic over HTTP: served normally, no loop.
	req, _ := http.NewRequest("GET", "http://"+addr+"/admin", nil)
	req.Header.Set("X-Forwarded-Proto", "https")
	resp, err = client.Do(req)
	if err != nil || resp.StatusCode != 200 {
		t.Fatalf("proxied https request must be served, got %v %v", resp, err)
	}
	c.stop()
}
