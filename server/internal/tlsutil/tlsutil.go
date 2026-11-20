// Package tlsutil provides certificates for all listeners. The source can be
// switched at runtime: ACME (Let's Encrypt), certificate files (e.g. taken
// over from a reverse proxy, incl. Traefik's acme.json) or a persisted
// self-signed fallback. Certificate files are reloaded when they change.
package tlsutil

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/acme"
	"golang.org/x/crypto/acme/autocert"

	"postwerk/internal/store"
)

// Dynamic serves certificates according to the current settings.
type Dynamic struct {
	DataDir   string
	Settings  func() store.TLSSettings
	Hostname  func() string
	ExtraHost func(ctx context.Context, host string) bool
	// EnvCert/EnvKey force file mode (POSTWERK_TLS_CERT/KEY).
	EnvCert, EnvKey string

	mu      sync.Mutex
	acmeKey string
	acmeMgr *autocert.Manager
	file    *fileCert
	self    *tls.Certificate
}

// Config returns a tls.Config for all listeners.
func (d *Dynamic) Config() *tls.Config {
	return &tls.Config{
		MinVersion:     tls.VersionTLS12,
		GetCertificate: d.GetCertificate,
		NextProtos:     []string{"h2", "http/1.1", acme.ALPNProto},
	}
}

func (d *Dynamic) mode() (string, store.TLSSettings) {
	s := d.Settings()
	if d.EnvCert != "" && d.EnvKey != "" {
		s.Files.CertPath, s.Files.KeyPath = d.EnvCert, d.EnvKey
		return "files", s
	}
	return s.Mode, s
}

func (d *Dynamic) GetCertificate(hello *tls.ClientHelloInfo) (*tls.Certificate, error) {
	if hello.ServerName == "" {
		// Mail clients often connect without SNI.
		hello.ServerName = d.Hostname()
	}
	mode, s := d.mode()
	switch mode {
	case "acme":
		if m := d.manager(s.ACME); m != nil {
			return m.GetCertificate(hello)
		}
	case "files":
		c, err := d.fileCert(s.Files.CertPath, s.Files.KeyPath)
		if err == nil {
			return c, nil
		}
		// Fall back to self-signed rather than refusing every handshake.
	}
	return d.selfSigned()
}

// HTTPHandler answers ACME HTTP-01 challenges in ACME mode and passes
// everything else to next.
func (d *Dynamic) HTTPHandler(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/.well-known/acme-challenge/") {
			if mode, s := d.mode(); mode == "acme" {
				if m := d.manager(s.ACME); m != nil {
					m.HTTPHandler(next).ServeHTTP(w, r)
					return
				}
			}
		}
		next.ServeHTTP(w, r)
	})
}

// Status describes the active certificate source for the UI.
func (d *Dynamic) Status() string {
	mode, s := d.mode()
	switch mode {
	case "acme":
		hosts := uniq(append([]string{d.Hostname()}, s.ACME.Domains...))
		return "Let's Encrypt (ACME) für " + strings.Join(hosts, ", ")
	case "files":
		c, err := d.fileCert(s.Files.CertPath, s.Files.KeyPath)
		if err != nil {
			return "Zertifikatsdatei nicht nutzbar (" + err.Error() + ") – selbstsigniertes Ersatzzertifikat aktiv"
		}
		info := Describe(c)
		src := "Zertifikat aus Datei " + s.Files.CertPath
		if d.EnvCert != "" {
			src = "Zertifikat aus POSTWERK_TLS_CERT"
		}
		return fmt.Sprintf("%s (%s, gültig bis %s)", src, strings.Join(info.Names, ", "), info.NotAfter.Format("02.01.2006"))
	}
	return "Selbstsigniertes Zertifikat (nur für Tests)"
}

func (d *Dynamic) manager(cfg store.ACMESettings) *autocert.Manager {
	key := fmt.Sprint(d.Hostname(), cfg.Email, cfg.DirectoryURL, cfg.Domains)
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.acmeMgr != nil && d.acmeKey == key {
		return d.acmeMgr
	}
	hosts := uniq(append([]string{d.Hostname()}, cfg.Domains...))
	static := autocert.HostWhitelist(hosts...)
	m := &autocert.Manager{
		Prompt: autocert.AcceptTOS,
		Cache:  autocert.DirCache(filepath.Join(d.DataDir, "acme")),
		Email:  cfg.Email,
		HostPolicy: func(ctx context.Context, host string) error {
			if err := static(ctx, host); err == nil {
				return nil
			}
			if d.ExtraHost != nil && d.ExtraHost(ctx, strings.ToLower(host)) {
				return nil
			}
			return fmt.Errorf("acme: host %q not allowed", host)
		},
	}
	if cfg.DirectoryURL != "" {
		m.Client = &acme.Client{DirectoryURL: cfg.DirectoryURL}
	}
	d.acmeMgr, d.acmeKey = m, key
	return m
}

// ---- certificate files ----

type fileCert struct {
	certPath, keyPath string
	modCert, modKey   time.Time
	checked           time.Time
	cert              *tls.Certificate
	err               error
}

func modTime(p string) time.Time {
	if st, err := os.Stat(p); err == nil {
		return st.ModTime()
	}
	return time.Time{}
}

// fileCert loads (and periodically reloads) a certificate from disk.
func (d *Dynamic) fileCert(certPath, keyPath string) (*tls.Certificate, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	f := d.file
	if f == nil || f.certPath != certPath || f.keyPath != keyPath {
		f = &fileCert{certPath: certPath, keyPath: keyPath}
		d.file = f
	}
	if f.cert != nil && time.Since(f.checked) < 30*time.Second {
		return f.cert, nil
	}
	f.checked = time.Now()
	mc, mk := modTime(certPath), modTime(keyPath)
	if f.cert != nil && mc.Equal(f.modCert) && mk.Equal(f.modKey) {
		return f.cert, nil
	}
	// For a Traefik acme.json pick the certificate matching the mail hostname.
	c, err := LoadPair(certPath, keyPath, d.Hostname())
	if err != nil && strings.HasSuffix(strings.ToLower(certPath), ".json") {
		c, err = LoadPair(certPath, keyPath, "")
	}
	if err != nil {
		if f.cert != nil {
			return f.cert, nil // keep the last good certificate
		}
		f.err = err
		return nil, err
	}
	f.cert, f.err, f.modCert, f.modKey = c, nil, mc, mk
	return c, nil
}

// LoadPair loads a PEM certificate/key pair or, for *.json, a certificate
// from a Traefik acme.json matching hostname (first match if empty).
func LoadPair(certPath, keyPath, hostname string) (*tls.Certificate, error) {
	if certPath == "" {
		return nil, errors.New("kein Zertifikatspfad angegeben")
	}
	if strings.HasSuffix(strings.ToLower(certPath), ".json") {
		certs, err := ReadTraefik(certPath)
		if err != nil {
			return nil, err
		}
		for _, c := range certs {
			if hostname == "" || Covers(c, hostname) {
				cc := c
				return &cc, nil
			}
		}
		if len(certs) > 0 && hostname == "" {
			return &certs[0], nil
		}
		return nil, fmt.Errorf("kein Zertifikat für %s in %s", hostname, filepath.Base(certPath))
	}
	if keyPath == "" {
		return nil, errors.New("kein Schlüsselpfad angegeben")
	}
	c, err := tls.LoadX509KeyPair(certPath, keyPath)
	if err != nil {
		return nil, err
	}
	if len(c.Certificate) > 0 {
		c.Leaf, _ = x509.ParseCertificate(c.Certificate[0])
	}
	return &c, nil
}

// ReadTraefik extracts all certificates from a Traefik acme.json (v2/v3).
func ReadTraefik(path string) ([]tls.Certificate, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var resolvers map[string]struct {
		Certificates []struct {
			Certificate string `json:"certificate"`
			Key         string `json:"key"`
		} `json:"Certificates"`
	}
	if err := json.Unmarshal(data, &resolvers); err != nil {
		return nil, fmt.Errorf("keine gültige Traefik-acme.json: %w", err)
	}
	var out []tls.Certificate
	for _, r := range resolvers {
		for _, c := range r.Certificates {
			certPEM, err1 := base64.StdEncoding.DecodeString(c.Certificate)
			keyPEM, err2 := base64.StdEncoding.DecodeString(c.Key)
			if err1 != nil || err2 != nil {
				continue
			}
			pair, err := tls.X509KeyPair(certPEM, keyPEM)
			if err != nil {
				continue
			}
			pair.Leaf, _ = x509.ParseCertificate(pair.Certificate[0])
			out = append(out, pair)
		}
	}
	if len(out) == 0 {
		return nil, errors.New("keine Zertifikate in der acme.json gefunden")
	}
	return out, nil
}

type Info struct {
	Names    []string
	Issuer   string
	NotAfter time.Time
}

func leaf(c *tls.Certificate) *x509.Certificate {
	if c.Leaf != nil {
		return c.Leaf
	}
	if len(c.Certificate) == 0 {
		return nil
	}
	l, _ := x509.ParseCertificate(c.Certificate[0])
	return l
}

func Describe(c *tls.Certificate) Info {
	l := leaf(c)
	if l == nil {
		return Info{}
	}
	names := append([]string{}, l.DNSNames...)
	if len(names) == 0 && l.Subject.CommonName != "" {
		names = []string{l.Subject.CommonName}
	}
	issuer := l.Issuer.CommonName
	if len(l.Issuer.Organization) > 0 {
		issuer = l.Issuer.Organization[0] + " " + issuer
	}
	return Info{Names: names, Issuer: strings.TrimSpace(issuer), NotAfter: l.NotAfter}
}

// Covers reports whether the certificate is valid for host (incl. wildcards).
func Covers(c tls.Certificate, host string) bool {
	l := leaf(&c)
	return l != nil && l.VerifyHostname(host) == nil
}

// ---- self-signed fallback ----

func (d *Dynamic) selfSigned() (*tls.Certificate, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.self != nil {
		return d.self, nil
	}
	c, err := selfSigned(d.DataDir, d.Hostname())
	if err != nil {
		return nil, err
	}
	d.self = &c
	return d.self, nil
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

// ---- scanning a certificate directory ----

type Found struct {
	CertPath string    `json:"certPath"`
	KeyPath  *string   `json:"keyPath"`
	Source   string    `json:"source"`
	Names    []string  `json:"names"`
	Issuer   string    `json:"issuer"`
	NotAfter time.Time `json:"notAfter"`
	Matches  bool      `json:"matchesHostname"`
	Error    *string   `json:"error"`
}

func errText(err error) *string {
	msg := err.Error()
	if os.IsPermission(err) || strings.Contains(msg, "permission denied") {
		msg = "Zugriff verweigert (permission denied): Postwerk darf die Datei nicht lesen"
	}
	return &msg
}

var keyNames = []string{"privkey.pem", "key.pem", "private.key", "privkey.key", "privatekey.key"}

// Scan walks dir (following the letsencrypt "live" symlinks) and lists
// usable certificates, newest first per name.
func Scan(dir, hostname string) []Found {
	var out []Found
	seen := map[string]bool{}
	_ = filepath.WalkDir(dir, func(p string, de os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if de.IsDir() {
			if rel, err := filepath.Rel(dir, p); err == nil && strings.Count(filepath.ToSlash(rel), "/") > 6 {
				return filepath.SkipDir
			}
			return nil
		}
		name := strings.ToLower(de.Name())
		switch {
		case name == "acme.json" || strings.HasSuffix(name, "acme.json"):
			certs, err := ReadTraefik(p)
			if err != nil {
				if os.IsPermission(err) {
					out = append(out, Found{CertPath: p, Source: "traefik", Names: []string{}, Error: errText(err)})
				}
				return nil
			}
			for _, c := range certs {
				info := Describe(&c)
				out = append(out, Found{CertPath: p, Source: "traefik", Names: info.Names, Issuer: info.Issuer, NotAfter: info.NotAfter, Matches: Covers(c, hostname)})
			}
		case name == "fullchain.pem" || name == "cert.pem" || strings.HasSuffix(name, ".crt"):
			if name == "cert.pem" {
				if _, err := os.Stat(filepath.Join(filepath.Dir(p), "fullchain.pem")); err == nil {
					return nil // prefer fullchain.pem in the same directory
				}
			}
			key := findKey(p)
			if key == "" {
				return nil
			}
			real, _ := filepath.EvalSymlinks(p)
			if seen[real+"|"+key] {
				return nil
			}
			seen[real+"|"+key] = true
			k := key
			c, err := LoadPair(p, key, "")
			if err != nil {
				// Certificate readable but key not (typical root-only 0600 keys): report it.
				if leafCert, lerr := readLeaf(p); lerr == nil {
					info := Describe(&tls.Certificate{Certificate: [][]byte{leafCert.Raw}, Leaf: leafCert})
					out = append(out, Found{CertPath: p, KeyPath: &k, Source: "files", Names: info.Names, Issuer: info.Issuer, NotAfter: info.NotAfter,
						Matches: leafCert.VerifyHostname(hostname) == nil, Error: errText(err)})
				}
				return nil
			}
			info := Describe(c)
			out = append(out, Found{CertPath: p, KeyPath: &k, Source: "files", Names: info.Names, Issuer: info.Issuer, NotAfter: info.NotAfter, Matches: Covers(*c, hostname)})
		}
		return nil
	})
	// Archive copies (letsencrypt/archive) duplicate live certs; drop them if a live one exists.
	var filtered []Found
	for _, f := range out {
		if strings.Contains(filepath.ToSlash(f.CertPath), "/archive/") {
			dup := false
			for _, g := range out {
				if !strings.Contains(filepath.ToSlash(g.CertPath), "/archive/") && strings.Join(g.Names, ",") == strings.Join(f.Names, ",") {
					dup = true
				}
			}
			if dup {
				continue
			}
		}
		filtered = append(filtered, f)
	}
	return filtered
}

func findKey(certPath string) string {
	dir := filepath.Dir(certPath)
	for _, k := range keyNames {
		if _, err := os.Stat(filepath.Join(dir, k)); err == nil {
			return filepath.Join(dir, k)
		}
	}
	if strings.HasSuffix(certPath, ".crt") {
		base := strings.TrimSuffix(filepath.Base(certPath), ".crt")
		for _, k := range []string{
			strings.TrimSuffix(certPath, ".crt") + ".key",            // Caddy: <host>.crt next to <host>.key
			filepath.Join(filepath.Dir(dir), "private", base+".key"), // traefik-certs-dumper: certs/<d>.crt, private/<d>.key
		} {
			if _, err := os.Stat(k); err == nil {
				return k
			}
		}
	}
	return ""
}

// readLeaf parses the first certificate of a PEM file.
func readLeaf(path string) (*x509.Certificate, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	blk, _ := pem.Decode(data)
	if blk == nil {
		return nil, errors.New("kein PEM-Zertifikat")
	}
	return x509.ParseCertificate(blk.Bytes)
}
