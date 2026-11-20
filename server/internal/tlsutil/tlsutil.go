// Package tlsutil provides certificates for all listeners: ACME (Let's
// Encrypt) via autocert, static files, or a persisted self-signed fallback.
package tlsutil

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"math/big"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"golang.org/x/crypto/acme"
	"golang.org/x/crypto/acme/autocert"

	"postwerk/internal/store"
)

type Provider struct {
	Config      *tls.Config
	HTTPHandler func(http.Handler) http.Handler // wraps port-80 handler for HTTP-01
	Status      string
}

// New builds the TLS configuration according to the precedence
// ACME > certificate files > self-signed.
//
// extraHost, if set, may allow additional hostnames for ACME at runtime
// (e.g. mta-sts.<domain> for every local domain).
func New(dataDir, hostname string, acmeCfg store.ACMESettings, certFile, keyFile string, extraHost func(ctx context.Context, host string) bool) (*Provider, error) {
	p := &Provider{HTTPHandler: func(h http.Handler) http.Handler { return h }}
	if acmeCfg.Enabled {
		hosts := append([]string{hostname}, acmeCfg.Domains...)
		static := autocert.HostWhitelist(uniq(hosts)...)
		m := &autocert.Manager{
			Prompt: autocert.AcceptTOS,
			Cache:  autocert.DirCache(filepath.Join(dataDir, "acme")),
			HostPolicy: func(ctx context.Context, host string) error {
				if err := static(ctx, host); err == nil {
					return nil
				}
				if extraHost != nil && extraHost(ctx, strings.ToLower(host)) {
					return nil
				}
				return fmt.Errorf("acme: host %q not allowed", host)
			},
			Email: acmeCfg.Email,
		}
		if acmeCfg.DirectoryURL != "" {
			m.Client = &acme.Client{DirectoryURL: acmeCfg.DirectoryURL}
		}
		cfg := m.TLSConfig()
		cfg.MinVersion = tls.VersionTLS12
		// Mail clients often connect without SNI; default to the main hostname.
		get := cfg.GetCertificate
		cfg.GetCertificate = func(h *tls.ClientHelloInfo) (*tls.Certificate, error) {
			if h.ServerName == "" {
				h.ServerName = hostname
			}
			return get(h)
		}
		p.Config = cfg
		p.HTTPHandler = m.HTTPHandler
		p.Status = "ACME aktiv für " + strings.Join(uniq(hosts), ", ")
		return p, nil
	}
	if certFile != "" && keyFile != "" {
		cert, err := tls.LoadX509KeyPair(certFile, keyFile)
		if err != nil {
			return nil, fmt.Errorf("load certificate: %w", err)
		}
		p.Config = &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS12}
		p.Status = "Zertifikat aus Datei " + certFile
		return p, nil
	}
	cert, err := selfSigned(dataDir, hostname)
	if err != nil {
		return nil, err
	}
	p.Config = &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS12}
	p.Status = "Selbstsigniertes Zertifikat (nur für Tests)"
	return p, nil
}

func uniq(in []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, s := range in {
		s = strings.ToLower(strings.TrimSpace(s))
		if s != "" && !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	return out
}

func selfSigned(dataDir, hostname string) (tls.Certificate, error) {
	dir := filepath.Join(dataDir, "tls")
	certPath, keyPath := filepath.Join(dir, "selfsigned.crt"), filepath.Join(dir, "selfsigned.key")
	if c, err := tls.LoadX509KeyPair(certPath, keyPath); err == nil {
		return c, nil
	}
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return tls.Certificate{}, err
	}
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return tls.Certificate{}, err
	}
	serial, _ := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 120))
	tmpl := &x509.Certificate{
		SerialNumber: serial,
		Subject:      pkix.Name{CommonName: hostname, Organization: []string{"Postwerk"}},
		DNSNames:     []string{hostname},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().AddDate(5, 0, 0),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		return tls.Certificate{}, err
	}
	kb, _ := x509.MarshalECPrivateKey(key)
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: kb})
	if err := os.WriteFile(certPath, certPEM, 0o640); err != nil {
		return tls.Certificate{}, err
	}
	if err := os.WriteFile(keyPath, keyPEM, 0o600); err != nil {
		return tls.Certificate{}, err
	}
	return tls.X509KeyPair(certPEM, keyPEM)
}
