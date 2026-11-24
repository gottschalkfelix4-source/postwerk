package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
)

// Settings are persisted as JSON. String fields tagged `secret:"true"` are
// encrypted with the master key in the database (vault.EncryptFields) and
// decrypted transparently on load; add the tag to every new secret field.
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
	Backup    BackupSettings    `json:"backup"`
	LDAP      LDAPSettings      `json:"ldap"`
	OIDC      OIDCSettings      `json:"oidc"`
	MailAuth  MailAuthSettings  `json:"mailauth"`
	// Limits are the outbound send limits per account (sendlimits.go).
	Limits SendLimitSettings `json:"limits"`
	// Notifications send operational warnings by mail/webhook (notify.go).
	Notifications NotificationSettings `json:"notifications"`
}

// MailAuthSettings tune sender authentication and the system addresses.
type MailAuthSettings struct {
	// TrustedARCSealers are domains whose ARC seals may relax a failed DMARC
	// policy (e.g. forwarders and mailing lists that seal what they checked).
	TrustedARCSealers []string `json:"trustedArcSealers"`
	// PostmasterAddress receives mail for postmaster@ and abuse@ of every
	// domain that has no such account or alias (empty: first admin account).
	PostmasterAddress string `json:"postmasterAddress"`
	// SkipReportDelivery drops DMARC and TLS report mails addressed to a
	// report address (postmaster@, dmarc@, ...) once every report in them
	// has been stored; they are then only shown under "Berichte". Mails that
	// could not be evaluated completely are still delivered.
	SkipReportDelivery bool `json:"skipReportDelivery"`
	// MTASTS configures the MTA-STS policy published for our own domains.
	MTASTS MTASTSSettings `json:"mtaSts"`
}

// MTASTSSettings select the mode ("none", "testing", "enforce") and max_age
// (seconds) of the MTA-STS policy served at mta-sts.<domain>.
type MTASTSSettings struct {
	Mode   string `json:"mode"`
	MaxAge int    `json:"maxAge"`
}

// DefaultTrustedARCSealers are large providers that ARC-seal forwarded mail.
var DefaultTrustedARCSealers = []string{"google.com", "microsoft.com", "outlook.com", "icloud.com", "yahoo.com"}

// BackupSettings configure automatic archives of the whole system.
type BackupSettings struct {
	Target   BackupTarget   `json:"target"`
	Schedule BackupSchedule `json:"schedule"`
	// Keep is the number of archives kept on the target (0 = keep all).
	Keep int `json:"keep"`
	// Encrypt protects archives with Password (AES-256-GCM, key via scrypt).
	Encrypt  bool   `json:"encrypt"`
	Password string `json:"password" secret:"true"`
}

type BackupTarget struct {
	Type      string `json:"type"` // local | webdav | smb
	LocalPath string `json:"localPath"`
	WebDAV    struct {
		URL           string `json:"url"`
		Username      string `json:"username"`
		Password      string `json:"password" secret:"true"`
		SkipTLSVerify bool   `json:"skipTlsVerify"`
	} `json:"webdav"`
	SMB struct {
		Host     string `json:"host"`
		Port     int    `json:"port"`
		Share    string `json:"share"`
		Path     string `json:"path"`
		Username string `json:"username"`
		Password string `json:"password" secret:"true"`
		Domain   string `json:"domain"`
	} `json:"smb"`
}

type BackupSchedule struct {
	Enabled   bool   `json:"enabled"`
	Frequency string `json:"frequency"` // hourly | daily | weekly
	Time      string `json:"time"`      // HH:MM (daily, weekly)
	Weekday   int    `json:"weekday"`   // 0 = Sonntag (weekly)
}

// DynDNSSettings keep the mail host's A record current on dynamic IPs.
type DynDNSSettings struct {
	Enabled         bool     `json:"enabled"`
	Preset          string   `json:"preset"`
	UpdateURL       string   `json:"updateUrl"`
	Hostnames       []string `json:"hostnames"`
	Username        string   `json:"username"`
	Password        string   `json:"password" secret:"true"`
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
	Password   string `json:"password" secret:"true"`
	SPFInclude string `json:"spfInclude"`
}

type ServerSettings struct {
	Hostname       string            `json:"hostname"`
	MaxMessageSize int64             `json:"maxMessageSize"`
	Retention      RetentionSettings `json:"retention"`
}

// RetentionSettings empty Trash and Junk automatically (0 = never).
type RetentionSettings struct {
	TrashDays int `json:"trashDays"`
	JunkDays  int `json:"junkDays"`
	// DKIMCleanup deletes retired DKIM keys some time after their grace
	// period (DKIMRetireGrace + DKIMCleanupMargin).
	DKIMCleanup bool `json:"dkimCleanup"`
	// DAVTombstoneDays keeps records of deleted calendar entries and
	// contacts for sync clients this long (0 = forever).
	DAVTombstoneDays int `json:"davTombstoneDays"`
}

// WebSettings describe how the web interface is reached (reverse proxy).
type WebSettings struct {
	PublicURL      string   `json:"publicUrl"`
	BehindProxy    bool     `json:"behindProxy"`
	TrustedProxies []string `json:"trustedProxies"`
	SecureCookies  bool     `json:"secureCookies"`
	// UndoSendSeconds delays webmail sends so the sender can cancel them
	// (0 = off). The delay applies per send and survives restarts.
	UndoSendSeconds int `json:"undoSendSeconds"`
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
	Enabled bool `json:"enabled"`
	// Provider selects the wire protocol of BaseURL: "openai" (OpenAI-compatible:
	// Ollama, LM Studio, vLLM, OpenAI, OpenRouter, ...) or "anthropic"
	// (Anthropic Messages API and compatible services).
	Provider       string `json:"provider"`
	BaseURL        string `json:"baseUrl"`
	APIKey         string `json:"apiKey" secret:"true"`
	ChatModel      string `json:"chatModel"`
	EmbeddingModel string `json:"embeddingModel"`
	// Optional separate OpenAI-compatible endpoint for embeddings. Required
	// for semantic search with the anthropic provider (it has no embeddings).
	EmbeddingBaseURL string     `json:"embeddingBaseUrl"`
	EmbeddingAPIKey  string     `json:"embeddingApiKey" secret:"true"`
	MaxTokens        int        `json:"maxTokens"` // anthropic: max_tokens per request
	TimeoutSeconds   int        `json:"timeoutSeconds"`
	Features         AIFeatures `json:"features"`
	Categories       []string   `json:"categories"`
	AutoFile         AIAutoFile `json:"autoFile"`
	// Briefing sends a daily summary of the latest messages to every account
	// (optional, off by default). Time is "HH:MM" in server local time.
	Briefing BriefingSettings `json:"briefing"`
}

// BriefingSettings configure the optional daily AI summary mail.
type BriefingSettings struct {
	Enabled bool   `json:"enabled"`
	Time    string `json:"time"`
}

// AIAutoFile moves analyzed messages out of the inbox into folders.
type AIAutoFile struct {
	Enabled bool `json:"enabled"`
	// Mode "categories": one folder per AI category (CategoryFolders, created
	// on demand). Mode "folders": the AI picks one of the account's own
	// existing folders.
	Mode            string            `json:"mode"`
	CategoryFolders map[string]string `json:"categoryFolders"`
	// KeepImportant leaves high-priority messages in the inbox.
	KeepImportant bool `json:"keepImportant"`
}

type DMSSettings struct {
	Paperless PaperlessSettings `json:"paperless"`
}

type PaperlessSettings struct {
	Enabled     bool     `json:"enabled"`
	URL         string   `json:"url"`
	Token       string   `json:"token" secret:"true"`
	AutoForward bool     `json:"autoForward"`
	Categories  []string `json:"categories"`
	// Accounts lists the addresses whose mail may be forwarded automatically.
	// The archive is shared, so nobody's documents land there unless the
	// admin names the account; empty means no automatic forwarding.
	Accounts []string `json:"accounts"`
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
	s.Server.Retention = RetentionSettings{TrashDays: 30, JunkDays: 30}
	s.TLS.ACME.DirectoryURL = "https://acme-v02.api.letsencrypt.org/directory"
	s.TLS.ACME.Domains = []string{}
	s.TLS.Mode = "selfsigned"
	s.TLS.HTTPPort = "open"
	s.Web.TrustedProxies = []string{}
	s.Web.UndoSendSeconds = 10
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
	s.AI.Provider = "openai"
	s.AI.BaseURL = "http://ollama:11434/v1"
	s.AI.MaxTokens = 16000
	s.AI.ChatModel = "qwen2.5:7b"
	s.AI.EmbeddingModel = "nomic-embed-text"
	s.AI.TimeoutSeconds = 120
	s.AI.Features = AIFeatures{SpamCheck: true, Classify: true, Priority: true, Summarize: true, ReplyDraft: true, SemanticSearch: true, ExtractEvents: true}
	s.AI.Categories = []string{"Rechnung", "Newsletter", "Support", "Privat", "Arbeit", "Versand", "Termin"}
	s.AI.Briefing = BriefingSettings{Time: "07:00"}
	s.AI.AutoFile = AIAutoFile{Mode: "categories", KeepImportant: true,
		CategoryFolders: map[string]string{"Rechnung": "Rechnungen", "Newsletter": "Newsletter", "Versand": "Versand"}}
	s.DMS.Paperless.URL = "http://paperless:8000"
	s.DMS.Paperless.Categories = []string{"Rechnung"}
	s.DMS.Paperless.Accounts = []string{}
	s.DynDNS = DynDNSSettings{Preset: "strato", UpdateURL: "https://dyndns.strato.com/nic/update", Hostnames: []string{},
		IPCheckURL: "https://api.ipify.org", IntervalMinutes: 5}
	s.Relay = RelaySettings{Preset: "strato", Host: "smtp.strato.de", Port: 465, Security: "tls", SPFInclude: "include:_spf.strato.com"}
	s.Backup.Target.Type = "local"
	s.Backup.Target.SMB.Port = 445
	s.Backup.Schedule = BackupSchedule{Frequency: "daily", Time: "03:30", Weekday: 0}
	s.Backup.Keep = 7
	s.Security.BruteForce.MaxAttempts = 5
	s.Security.BruteForce.WindowSeconds = 600
	s.Security.BruteForce.BanSeconds = 3600
	applyDirectoryDefaults(&s)
	s.MailAuth.TrustedARCSealers = append([]string{}, DefaultTrustedARCSealers...)
	s.MailAuth.MTASTS = MTASTSSettings{Mode: "testing", MaxAge: 604800}
	s.Server.Retention.DKIMCleanup = true
	s.Server.Retention.DAVTombstoneDays = 90
	applySendLimitDefaults(&s)
	applyNotificationDefaults(&s)
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
	plain, err := s.Vault.DecryptFields(&cur)
	if err != nil {
		return nil, fmt.Errorf("settings secrets: %w", err)
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
	if cur.AI.AutoFile.CategoryFolders == nil {
		cur.AI.AutoFile.CategoryFolders = map[string]string{}
	}
	if cur.AI.AutoFile.Mode == "" {
		cur.AI.AutoFile.Mode = "categories"
	}
	if cur.Web.TrustedProxies == nil {
		cur.Web.TrustedProxies = []string{}
	}
	if cur.MailAuth.TrustedARCSealers == nil {
		cur.MailAuth.TrustedARCSealers = []string{}
	}
	if cur.MailAuth.MTASTS.Mode == "" {
		cur.MailAuth.MTASTS.Mode = "testing"
	}
	if cur.MailAuth.MTASTS.MaxAge <= 0 {
		cur.MailAuth.MTASTS.MaxAge = 604800
	}
	if cur.Notifications.Recipients == nil {
		cur.Notifications.Recipients = []string{}
	}
	if cur.DMS.Paperless.Accounts == nil {
		cur.DMS.Paperless.Accounts = []string{}
	}
	if cur.Notifications.Categories == nil {
		cur.Notifications.Categories = []string{}
	}
	c := &SettingsCache{cur: cur, st: s}
	s.settings = c
	// Save also encrypts secrets that are still stored in plaintext.
	if raw == "" || plain > 0 {
		if err := c.Save(ctx, cur); err != nil {
			return nil, err
		}
	}
	return c, nil
}

// postmasterAddress returns the configured postmaster target without the
// deep copy of Get.
func (c *SettingsCache) postmasterAddress() string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.cur.MailAuth.PostmasterAddress
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
	if _, err := c.st.Vault.DecryptFields(&cur); err != nil {
		return fmt.Errorf("settings secrets: %w", err)
	}
	c.mu.Lock()
	c.cur = cur
	c.mu.Unlock()
	return nil
}

func (c *SettingsCache) Save(ctx context.Context, s Settings) error {
	// Encrypt secrets in a deep copy; the cache keeps the plaintext.
	b, err := json.Marshal(s)
	if err != nil {
		return err
	}
	var enc Settings
	if err := json.Unmarshal(b, &enc); err != nil {
		return err
	}
	c.st.Vault.EncryptFields(&enc)
	if b, err = json.Marshal(enc); err != nil {
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
