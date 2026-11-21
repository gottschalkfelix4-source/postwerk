package queue

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"mime"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

// MTA-STS (RFC 8461): a recipient domain publishes via DNS and HTTPS which MX
// hosts are valid and whether senders must use verified TLS.

// STSPolicy is a fetched MTA-STS policy.
type STSPolicy struct {
	ID      string    `json:"id"`
	Mode    string    `json:"mode"` // enforce | testing | none
	MX      []string  `json:"mx"`
	MaxAge  int64     `json:"maxAge"` // seconds
	Fetched time.Time `json:"fetched"`
}

const (
	stsMaxAge     = 31557600 // RFC 8461: at most one year
	stsMaxBody    = 64 << 10
	stsHTTPTimout = 20 * time.Second
)

// Expired reports whether the policy is older than its max_age.
func (p *STSPolicy) Expired(now time.Time) bool {
	return now.After(p.Fetched.Add(time.Duration(p.MaxAge) * time.Second))
}

// Matches reports whether an MX host is allowed by the policy. A pattern
// "*.example.com" matches exactly one additional leftmost label.
func (p *STSPolicy) Matches(host string) bool {
	host = strings.ToLower(strings.TrimSuffix(host, "."))
	for _, pat := range p.MX {
		pat = strings.ToLower(strings.TrimSuffix(pat, "."))
		if suffix, ok := strings.CutPrefix(pat, "*."); ok {
			label, rest, found := strings.Cut(host, ".")
			if found && label != "" && rest == suffix {
				return true
			}
			continue
		}
		if host == pat {
			return true
		}
	}
	return false
}

// parseSTSRecord finds the policy ID in the TXT records of _mta-sts.<domain>.
// Exactly one record starting with "v=STSv1" must exist.
func parseSTSRecord(txts []string) (string, bool) {
	var id string
	n := 0
	for _, t := range txts {
		t = strings.TrimSpace(t)
		if !strings.HasPrefix(t, "v=STSv1") {
			continue
		}
		n++
		for _, f := range strings.Split(t, ";") {
			k, v, _ := strings.Cut(strings.TrimSpace(f), "=")
			if strings.TrimSpace(k) == "id" {
				id = strings.TrimSpace(v)
			}
		}
	}
	if n != 1 || id == "" || len(id) > 32 {
		return "", false
	}
	for _, c := range id {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9') {
			return "", false
		}
	}
	return id, true
}

// parseSTSPolicy parses the policy file.
func parseSTSPolicy(body string) (*STSPolicy, error) {
	p := &STSPolicy{MaxAge: -1}
	version := ""
	for _, line := range strings.Split(body, "\n") {
		line = strings.TrimRight(line, "\r")
		k, v, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		k, v = strings.TrimSpace(k), strings.TrimSpace(v)
		switch k {
		case "version":
			version = v
		case "mode":
			p.Mode = v
		case "mx":
			p.MX = append(p.MX, v)
		case "max_age":
			n, err := strconv.ParseInt(v, 10, 64)
			if err != nil || n < 0 {
				return nil, fmt.Errorf("ungültiges max_age %q", v)
			}
			p.MaxAge = min(n, stsMaxAge)
		}
	}
	if version != "STSv1" {
		return nil, errors.New("version STSv1 fehlt")
	}
	switch p.Mode {
	case "enforce", "testing":
		if len(p.MX) == 0 {
			return nil, errors.New("keine mx-Einträge")
		}
	case "none":
	default:
		return nil, fmt.Errorf("ungültiger mode %q", p.Mode)
	}
	if p.MaxAge < 0 {
		return nil, errors.New("max_age fehlt")
	}
	return p, nil
}

// kvStore persists policies across restarts (implemented by *store.Store).
type kvStore interface {
	KV(ctx context.Context, key string) (string, error)
	SetKV(ctx context.Context, key, value string) error
}

// MTASTS looks up and caches MTA-STS policies.
type MTASTS struct {
	LookupTXT func(ctx context.Context, name string) ([]string, error)
	HTTP      *http.Client
	// PolicyURL returns the policy location for a domain.
	PolicyURL func(domain string) string
	Store     kvStore
	Log       *slog.Logger
	Now       func() time.Time

	mu  sync.Mutex
	mem map[string]*STSPolicy
}

// NewMTASTS returns a resolver using the system DNS and verified HTTPS.
func NewMTASTS(st kvStore, log *slog.Logger) *MTASTS {
	tr := &http.Transport{
		Proxy:               http.ProxyFromEnvironment,
		DialContext:         (&net.Dialer{Timeout: 10 * time.Second}).DialContext,
		TLSClientConfig:     &tls.Config{MinVersion: tls.VersionTLS12},
		TLSHandshakeTimeout: 10 * time.Second,
		MaxIdleConns:        10,
		IdleConnTimeout:     30 * time.Second,
	}
	return &MTASTS{
		LookupTXT: net.DefaultResolver.LookupTXT,
		HTTP: &http.Client{Transport: tr, Timeout: stsHTTPTimout,
			// RFC 8461 3.3: redirects must not be followed.
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }},
		Store: st,
		Log:   log,
	}
}

func (m *MTASTS) now() time.Time {
	if m.Now != nil {
		return m.Now()
	}
	return time.Now()
}

func (m *MTASTS) policyURL(domain string) string {
	if m.PolicyURL != nil {
		return m.PolicyURL(domain)
	}
	return "https://mta-sts." + domain + "/.well-known/mta-sts.txt"
}

func (m *MTASTS) cached(ctx context.Context, domain string) *STSPolicy {
	m.mu.Lock()
	p := m.mem[domain]
	m.mu.Unlock()
	if p == nil && m.Store != nil {
		if raw, err := m.Store.KV(ctx, "mtasts:"+domain); err == nil && raw != "" {
			var sp STSPolicy
			if json.Unmarshal([]byte(raw), &sp) == nil {
				p = &sp
				m.remember(domain, p)
			}
		}
	}
	if p != nil && p.Expired(m.now()) {
		return nil
	}
	return p
}

func (m *MTASTS) remember(domain string, p *STSPolicy) {
	m.mu.Lock()
	if m.mem == nil {
		m.mem = map[string]*STSPolicy{}
	}
	m.mem[domain] = p
	m.mu.Unlock()
}

func (m *MTASTS) save(ctx context.Context, domain string, p *STSPolicy) {
	m.remember(domain, p)
	if m.Store != nil {
		b, _ := json.Marshal(p)
		_ = m.Store.SetKV(ctx, "mtasts:"+domain, string(b))
	}
}

// Policy returns the valid policy for domain or nil if it has none. A cached
// policy stays in force until it expires, even if DNS or HTTPS fail.
func (m *MTASTS) Policy(ctx context.Context, domain string) *STSPolicy {
	domain = strings.ToLower(strings.TrimSuffix(domain, "."))
	cached := m.cached(ctx, domain)
	lctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	txts, err := m.LookupTXT(lctx, "_mta-sts."+domain)
	cancel()
	id, ok := parseSTSRecord(txts)
	if err != nil || !ok {
		return cached
	}
	if cached != nil && cached.ID == id {
		return cached
	}
	p, err := m.fetch(ctx, domain)
	if err != nil {
		if m.Log != nil {
			m.Log.Warn("mta-sts policy fetch failed", "domain", domain, "err", err)
		}
		return cached
	}
	p.ID, p.Fetched = id, m.now()
	m.save(ctx, domain, p)
	return p
}

func (m *MTASTS) fetch(ctx context.Context, domain string) (*STSPolicy, error) {
	ctx, cancel := context.WithTimeout(ctx, stsHTTPTimout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, m.policyURL(domain), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "Postwerk-MTA-STS/1.0")
	resp, err := m.HTTP.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	if mt, _, err := mime.ParseMediaType(resp.Header.Get("Content-Type")); err != nil || mt != "text/plain" {
		return nil, fmt.Errorf("unerwarteter Content-Type %q", resp.Header.Get("Content-Type"))
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, stsMaxBody+1))
	if err != nil {
		return nil, err
	}
	if len(body) > stsMaxBody {
		return nil, errors.New("policy größer als 64 KB")
	}
	return parseSTSPolicy(string(body))
}
