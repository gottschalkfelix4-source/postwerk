package tlsutil

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"postwerk/internal/store"
)

func makeCert(t *testing.T, names []string, notAfter time.Time) (certPEM, keyPEM []byte) {
	t.Helper()
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	tmpl := &x509.Certificate{SerialNumber: big.NewInt(time.Now().UnixNano()), Subject: pkix.Name{CommonName: names[0]},
		DNSNames: names, NotBefore: time.Now().Add(-time.Hour), NotAfter: notAfter}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	kb, _ := x509.MarshalECPrivateKey(key)
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: kb})
}

func write(t *testing.T, p string, b []byte) {
	t.Helper()
	os.MkdirAll(filepath.Dir(p), 0o755)
	if err := os.WriteFile(p, b, 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestScanLetsencryptCaddyAndTraefik(t *testing.T) {
	dir := t.TempDir()
	c1, k1 := makeCert(t, []string{"mail.example.com"}, time.Now().AddDate(0, 2, 0))
	write(t, filepath.Join(dir, "npm/live/npm-3/fullchain.pem"), c1)
	write(t, filepath.Join(dir, "npm/live/npm-3/privkey.pem"), k1)
	c2, k2 := makeCert(t, []string{"*.example.com"}, time.Now().AddDate(0, 1, 0))
	write(t, filepath.Join(dir, "caddy/certificates/acme/wild/wild.crt"), c2)
	write(t, filepath.Join(dir, "caddy/certificates/acme/wild/wild.key"), k2)
	c3, k3 := makeCert(t, []string{"other.example.org"}, time.Now().AddDate(0, 1, 0))
	acme := map[string]any{"letsencrypt": map[string]any{"Certificates": []map[string]any{{
		"domain":      map[string]any{"main": "other.example.org"},
		"certificate": base64.StdEncoding.EncodeToString(c3), "key": base64.StdEncoding.EncodeToString(k3),
	}}}}
	b, _ := json.Marshal(acme)
	write(t, filepath.Join(dir, "traefik/acme.json"), b)
	c4, k4 := makeCert(t, []string{"dump.example.com"}, time.Now().AddDate(0, 1, 0))
	write(t, filepath.Join(dir, "dumper/certs/dump.example.com.crt"), c4)
	write(t, filepath.Join(dir, "dumper/private/dump.example.com.key"), k4)

	found := Scan(dir, "mail.example.com")
	bySrc := map[string]Found{}
	for _, f := range found {
		bySrc[f.Names[0]] = f
	}
	if f := bySrc["mail.example.com"]; !f.Matches || f.KeyPath == nil || f.Error != nil {
		t.Errorf("npm cert = %+v", f)
	}
	if f := bySrc["*.example.com"]; !f.Matches {
		t.Errorf("wildcard should match mail.example.com: %+v", f)
	}
	if f := bySrc["other.example.org"]; f.Source != "traefik" || f.Matches {
		t.Errorf("traefik cert = %+v", f)
	}
	if f, ok := bySrc["dump.example.com"]; !ok || f.KeyPath == nil {
		t.Errorf("certs-dumper layout not found: %+v", found)
	}
	// Traefik acme.json selects the cert for the requested hostname.
	if _, err := LoadPair(filepath.Join(dir, "traefik/acme.json"), "", "other.example.org"); err != nil {
		t.Errorf("acme.json load: %v", err)
	}
	if _, err := LoadPair(filepath.Join(dir, "traefik/acme.json"), "", "mail.example.com"); err == nil {
		t.Errorf("acme.json must not return a non-matching cert")
	}
}

func TestDynamicFilesReloadAndFallback(t *testing.T) {
	dir := t.TempDir()
	certP, keyP := filepath.Join(dir, "c.pem"), filepath.Join(dir, "k.pem")
	c1, k1 := makeCert(t, []string{"mail.example.com"}, time.Now().AddDate(0, 1, 0))
	write(t, certP, c1)
	write(t, keyP, k1)
	settings := store.TLSSettings{Mode: "files", Files: store.TLSFilesSettings{CertPath: certP, KeyPath: keyP}}
	d := &Dynamic{DataDir: dir, Settings: func() store.TLSSettings { return settings }, Hostname: func() string { return "mail.example.com" }}
	got, err := d.GetCertificate(&tls.ClientHelloInfo{ServerName: "mail.example.com"})
	if err != nil || Describe(got).NotAfter.Month() != time.Now().AddDate(0, 1, 0).Month() {
		t.Fatalf("first load: %v", err)
	}
	// Renewal: new file content, later mtime.
	c2, k2 := makeCert(t, []string{"mail.example.com"}, time.Now().AddDate(0, 3, 0))
	write(t, certP, c2)
	write(t, keyP, k2)
	later := time.Now().Add(time.Minute)
	os.Chtimes(certP, later, later)
	os.Chtimes(keyP, later, later)
	d.file.checked = time.Time{} // skip the 30 s throttle
	got, _ = d.GetCertificate(&tls.ClientHelloInfo{ServerName: "mail.example.com"})
	if Describe(got).NotAfter.Before(time.Now().AddDate(0, 2, 0)) {
		t.Fatalf("certificate was not reloaded after renewal")
	}
	// Broken path: falls back to self-signed instead of failing handshakes.
	settings.Files.CertPath = filepath.Join(dir, "fehlt.pem")
	got, err = d.GetCertificate(&tls.ClientHelloInfo{ServerName: "mail.example.com"})
	if err != nil || got == nil {
		t.Fatalf("expected self-signed fallback, got %v", err)
	}
	if s := d.Status(); s == "" {
		t.Fatal("empty status")
	}
	// Switch to self-signed at runtime.
	settings.Mode = "selfsigned"
	if _, err := d.GetCertificate(&tls.ClientHelloInfo{}); err != nil {
		t.Fatal(err)
	}
}

// Apple Mail offers ALPN "imap" (or "smtp", "pop3"); the handshake must not
// fail just because the server otherwise speaks HTTP protocols.
func TestMailConfigAcceptsProtocolALPN(t *testing.T) {
	d := &Dynamic{DataDir: t.TempDir(), Settings: func() store.TLSSettings { return store.TLSSettings{Mode: "selfsigned"} }, Hostname: func() string { return "mail.example.com" }}
	handshake := func(server *tls.Config, offer string) (string, error) {
		a, b := net.Pipe()
		defer a.Close()
		defer b.Close()
		srvErr := make(chan error, 1)
		go func() { srvErr <- tls.Server(a, server).Handshake() }()
		c := tls.Client(b, &tls.Config{InsecureSkipVerify: true, NextProtos: []string{offer}})
		err := c.Handshake()
		if e := <-srvErr; err == nil {
			err = e
		}
		return c.ConnectionState().NegotiatedProtocol, err
	}
	for _, p := range []string{"imap", "pop3", "smtp"} {
		got, err := handshake(d.MailConfig(p), p)
		if err != nil || got != p {
			t.Errorf("%s: negotiated %q, err %v", p, got, err)
		}
	}
	if _, err := handshake(d.Config(), "imap"); err == nil {
		t.Error("HTTP config unexpectedly accepted ALPN imap")
	}
}
