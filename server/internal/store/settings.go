package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"sync"
)

type Settings struct {
	Server    ServerSettings    `json:"server"`
	Web       WebSettings       `json:"web"`
	TLS       TLSSettings       `json:"tls"`
	Spam      SpamSettings      `json:"spam"`
	Antivirus AntivirusSettings `json:"antivirus"`
	AI        AISettings        `json:"ai"`
	DMS       DMSSettings       `json:"dms"`
	Security  SecuritySettings  `json:"security"`
	DynDNS    DynDNSSettings    `json:"dyndns"`
	Relay     RelaySettings     `json:"relay"`
}

// DynDNSSettings keep the mail host's A record current on dynamic IPs.
type DynDNSSettings struct {
	Enabled         bool     `json:"enabled"`
	Preset          string   `json:"preset"`
	UpdateURL       string   `json:"updateUrl"`
	Hostnames       []string `json:"hostnames"`
	Username        string   `json:"username"`
	Password        string   `json:"password"`
	IPCheckURL      string   `json:"ipCheckUrl"`
	IntervalMinutes int      `json:"intervalMinutes"`
}

// RelaySettings configure a smarthost for outbound mail.
type RelaySettings struct {
	Enabled    bool   `json:"enabled"`
	Preset     string `json:"preset"`
	Host       string `json:"host"`
	Port       int    `json:"port"`
	Security   string `json:"security"`
	Username   string `json:"username"`
	Password   string `json:"password"`
	SPFInclude string `json:"spfInclude"`
}

type ServerSettings struct {
	Hostname       string `json:"hostname"`
	MaxMessageSize int64  `json:"maxMessageSize"`
}

// WebSettings describe how the web interface is reached (reverse proxy).
type WebSettings struct {
	PublicURL      string   `json:"publicUrl"`
	BehindProxy    bool     `json:"behindProxy"`
	TrustedProxies []string `json:"trustedProxies"`
	SecureCookies  bool     `json:"secureCookies"`
}

type TLSSettings struct {
	// Mode selects the certificate source: selfsigned, acme or files.
	Mode  string           `json:"mode"`
	ACME  ACMESettings     `json:"acme"`
	Files TLSFilesSettings `json:"files"`
	// HTTPPort controls the plain HTTP listener (host port 80):
	// "open" (full UI), "redirect" (ACME + redirect to HTTPS) or
	// "auto-close" (closed while a valid certificate exists, reopened for renewal).
	HTTPPort   string `json:"httpPort"`
	Status     string `json:"status"`
	HTTPStatus string `json:"httpStatus"`
}

type TLSFilesSettings struct {
	CertPath string `json:"certPath"`
	KeyPath  string `json:"keyPath"`
}

type ACMESettings struct {
	Enabled      bool     `json:"enabled"`
	Email        string   `json:"email"`
	DirectoryURL string   `json:"directoryUrl"`
	Domains      []string `json:"domains"`
}

type SpamSettings struct {
	Enabled         bool    `json:"enabled"`
	JunkThreshold   float64 `json:"junkThreshold"`
	RejectThreshold float64 `json:"rejectThreshold"`
	Greylisting     struct {
		Enabled      bool `json:"enabled"`
		DelaySeconds int  `json:"delaySeconds"`
	} `json:"greylisting"`
	DNSBL struct {
		Enabled bool     `json:"enabled"`
		Servers []string `json:"servers"`
	} `json:"dnsbl"`
	Bayes struct {
		Enabled   bool `json:"enabled"`
		AutoLearn bool `json:"autoLearn"`
		MinTokens int  `json:"minTokens"`
	} `json:"bayes"`
}

type AntivirusSettings struct {
	Enabled      bool   `json:"enabled"`
	ClamdAddress string `json:"clamdAddress"`
	Action       string `json:"action"`
}

type AIFeatures struct {
	SpamCheck      bool `json:"spamCheck"`
	Classify       bool `json:"classify"`
	Priority       bool `json:"priority"`
	Summarize      bool `json:"summarize"`
	ReplyDraft     bool `json:"replyDraft"`
	SemanticSearch bool `json:"semanticSearch"`
	ExtractEvents  bool `json:"extractEvents"`
}

type AISettings struct {
	Enabled        bool       `json:"enabled"`
	BaseURL        string     `json:"baseUrl"`
	APIKey         string     `json:"apiKey"`
	ChatModel      string     `json:"chatModel"`
	EmbeddingModel string     `json:"embeddingModel"`
	TimeoutSeconds int        `json:"timeoutSeconds"`
	Features       AIFeatures `json:"features"`
	Categories     []string   `json:"categories"`
}

type DMSSettings struct {
	Paperless PaperlessSettings `json:"paperless"`
}

type PaperlessSettings struct {
	Enabled     bool     `json:"enabled"`
	URL         string   `json:"url"`
	Token       string   `json:"token"`
	AutoForward bool     `json:"autoForward"`
	Categories  []string `json:"categories"`
}

type SecuritySettings struct {
	BruteForce struct {
		MaxAttempts   int `json:"maxAttempts"`
		WindowSeconds int `json:"windowSeconds"`
		BanSeconds    int `json:"banSeconds"`
	} `json:"bruteForce"`
}

func DefaultSettings(hostname string) Settings {
	var s Settings
	s.Server.Hostname = hostname
	s.Server.MaxMessageSize = 50 << 20
	s.TLS.ACME.DirectoryURL = "https://acme-v02.api.letsencrypt.org/directory"
	s.TLS.ACME.Domains = []string{}
	s.TLS.Mode = "selfsigned"
	s.TLS.HTTPPort = "open"
	s.Web.TrustedProxies = []string{}
	s.Spam.Enabled = true
	s.Spam.JunkThreshold = 5
	s.Spam.RejectThreshold = 15
	s.Spam.Greylisting.Enabled = false
	s.Spam.Greylisting.DelaySeconds = 300
	s.Spam.DNSBL.Enabled = true
	s.Spam.DNSBL.Servers = []string{"zen.spamhaus.org", "bl.spamcop.net", "b.barracudacentral.org"}
	s.Spam.Bayes.Enabled = true
	s.Spam.Bayes.AutoLearn = true
	s.Spam.Bayes.MinTokens = 200
	s.Antivirus.Enabled = false
	s.Antivirus.ClamdAddress = "clamav:3310"
	s.Antivirus.Action = "reject"
	s.AI.Enabled = false
	s.AI.BaseURL = "http://ollama:11434/v1"
	s.AI.ChatModel = "qwen2.5:7b"
	s.AI.EmbeddingModel = "nomic-embed-text"
	s.AI.TimeoutSeconds = 120
	s.AI.Features = AIFeatures{SpamCheck: true, Classify: true, Priority: true, Summarize: true, ReplyDraft: true, SemanticSearch: true, ExtractEvents: true}
	s.AI.Categories = []string{"Rechnung", "Newsletter", "Support", "Privat", "Arbeit", "Versand", "Termin"}
	s.DMS.Paperless.URL = "http://paperless:8000"
	s.DMS.Paperless.Categories = []string{"Rechnung"}
	s.DynDNS = DynDNSSettings{Preset: "strato", UpdateURL: "https://dyndns.strato.com/nic/update", Hostnames: []string{},
		IPCheckURL: "https://api.ipify.org", IntervalMinutes: 5}
	s.Relay = RelaySettings{Preset: "strato", Host: "smtp.strato.de", Port: 465, Security: "tls", SPFInclude: "include:_spf.strato.com"}
	s.Security.BruteForce.MaxAttempts = 5
	s.Security.BruteForce.WindowSeconds = 600
	s.Security.BruteForce.BanSeconds = 3600
	return s
}

// SettingsCache keeps the current settings in memory; writers persist them.
type SettingsCache struct {
	mu  sync.RWMutex
	cur Settings
	st  *Store
}

func (s *Store) LoadSettings(ctx context.Context, hostname string) (*SettingsCache, error) {
	def := DefaultSettings(hostname)
	var raw string
	err := s.DB.QueryRowContext(ctx, `SELECT value FROM settings WHERE key = 'settings'`).Scan(&raw)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}
	cur := def
	if raw != "" {
		// Unmarshal over defaults so new fields get sensible values.
		if err := json.Unmarshal([]byte(raw), &cur); err != nil {
			return nil, err
		}
	}
	if cur.TLS.Mode == "" {
		// Settings from before the TLS mode existed.
		cur.TLS.Mode = "selfsigned"
		if cur.TLS.ACME.Enabled {
			cur.TLS.Mode = "acme"
		}
	}
	if cur.DynDNS.Hostnames == nil {
		cur.DynDNS.Hostnames = []string{}
	}
	if cur.TLS.HTTPPort == "" {
		cur.TLS.HTTPPort = "open"
	}
	if cur.Web.TrustedProxies == nil {
		cur.Web.TrustedProxies = []string{}
	}
	c := &SettingsCache{cur: cur, st: s}
	if raw == "" {
		if err := c.Save(ctx, cur); err != nil {
			return nil, err
		}
	}
	return c, nil
}

func (c *SettingsCache) Get() Settings {
	c.mu.RLock()
	defer c.mu.RUnlock()
	// Deep copy via JSON so callers cannot mutate shared slices.
	b, _ := json.Marshal(c.cur)
	var out Settings
	_ = json.Unmarshal(b, &out)
	return out
}

// Reload re-reads the settings from the database (e.g. after a CLI change).
func (c *SettingsCache) Reload(ctx context.Context) error {
	var raw string
	if err := c.st.DB.QueryRowContext(ctx, `SELECT value FROM settings WHERE key = 'settings'`).Scan(&raw); err != nil {
		return err
	}
	cur := c.Get()
	if err := json.Unmarshal([]byte(raw), &cur); err != nil {
		return err
	}
	c.mu.Lock()
	c.cur = cur
	c.mu.Unlock()
	return nil
}

func (c *SettingsCache) Save(ctx context.Context, s Settings) error {
	b, err := json.Marshal(s)
	if err != nil {
		return err
	}
	if _, err := c.st.DB.ExecContext(ctx, `INSERT INTO settings(key, value) VALUES('settings', ?)
		ON CONFLICT(key) DO UPDATE SET value = excluded.value`, string(b)); err != nil {
		return err
	}
	c.mu.Lock()
	c.cur = s
	c.mu.Unlock()
	return nil
}
