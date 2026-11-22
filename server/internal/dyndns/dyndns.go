// Package dyndns keeps the mail host's A record current on connections with
// a changing public IP, using the widespread dyndns2 protocol (STRATO, INWX,
// deSEC, No-IP …). The provider is only contacted when the IP changed, as
// repeated identical updates are treated as abuse by most services.
package dyndns

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"postwerk/internal/store"

	"postwerk/internal/httpx"
)

type Preset struct {
	ID           string  `json:"id"`
	Name         string  `json:"name"`
	UpdateURL    string  `json:"updateUrl"`
	UsernameHint string  `json:"usernameHint"`
	PasswordHint string  `json:"passwordHint"`
	DocsURL      *string `json:"docsUrl"`
	Notes        *string `json:"notes"`
}

func sp(s string) *string { return &s }

var Presets = []Preset{
	{ID: "strato", Name: "STRATO", UpdateURL: "https://dyndns.strato.com/nic/update",
		UsernameHint: "Die Domain bzw. Subdomain, z. B. mail.deinedomain.de",
		PasswordHint: "DynDNS-Passwort aus dem STRATO-Login (Sicherheit → Passwörter festlegen)",
		DocsURL:      sp("https://www.strato.de/faq/domains/so-einfach-richten-sie-dyndns-fuer-ihre-domains-ein/"),
		Notes:        sp("Im STRATO-Login für die (Sub-)Domain DynDNS aktivieren. Der MX-Eintrag der Domain muss auf diesen Hostnamen zeigen.")},
	{ID: "inwx", Name: "INWX", UpdateURL: "https://dyndns.inwx.com/nic/update",
		UsernameHint: "DynDNS-Benutzername aus dem INWX-Konto", PasswordHint: "DynDNS-Passwort",
		DocsURL: sp("https://www.inwx.de/de/offer/dyndns")},
	{ID: "desec", Name: "deSEC", UpdateURL: "https://update.dedyn.io/nic/update",
		UsernameHint: "Die Domain", PasswordHint: "API-Token", DocsURL: sp("https://desec.io/")},
	{ID: "noip", Name: "No-IP", UpdateURL: "https://dynupdate.no-ip.com/nic/update",
		UsernameHint: "No-IP-Benutzername oder DDNS-Key", PasswordHint: "Passwort", DocsURL: sp("https://www.noip.com/integrate")},
	{ID: "custom", Name: "Anderer (dyndns2)", UpdateURL: "", UsernameHint: "Benutzername", PasswordHint: "Passwort"},
}

type Status struct {
	Enabled     bool                `json:"enabled"`
	CurrentIP   *string             `json:"currentIp"`
	DNSIPs      map[string][]string `json:"dnsIps"`
	LastUpdate  *time.Time          `json:"lastUpdate"`
	LastResult  *string             `json:"lastResult"`
	Error       *string             `json:"error"`
	PausedUntil *time.Time          `json:"pausedUntil"`
	NextCheck   *time.Time          `json:"nextCheck"`
}

type Updater struct {
	Settings *store.SettingsCache
	Log      *slog.Logger
	HTTP     *http.Client

	mu        sync.Mutex
	lastIP    string // IP the provider confirmed
	lastCfg   string // config fingerprint for which lastIP is valid
	current   string
	lastOK    *time.Time
	lastRes   string
	lastErr   string
	paused    time.Time
	nextCheck time.Time
	wake      chan struct{}
}

func New(s *store.SettingsCache, log *slog.Logger) *Updater {
	return &Updater{Settings: s, Log: log.With("component", "dyndns"), HTTP: &http.Client{Timeout: 20 * time.Second, CheckRedirect: httpx.SameHostRedirect}, wake: make(chan struct{}, 1)}
}

// Notify triggers a check soon (e.g. after settings changed).
func (u *Updater) Notify() {
	select {
	case u.wake <- struct{}{}:
	default:
	}
}

func (u *Updater) Run(ctx context.Context) {
	for {
		cfg := u.Settings.Get().DynDNS
		if cfg.Enabled {
			_, _ = u.Check(ctx, false)
		}
		interval := time.Duration(max(cfg.IntervalMinutes, 1)) * time.Minute
		u.mu.Lock()
		u.nextCheck = time.Now().Add(interval)
		u.mu.Unlock()
		select {
		case <-ctx.Done():
			return
		case <-u.wake:
		case <-time.After(interval):
		}
	}
}

func fingerprint(c store.DynDNSSettings) string {
	return fmt.Sprint(c.UpdateURL, c.Hostnames, c.Username, c.Password)
}

// Check determines the public IP and updates the provider if it changed
// (or always with force).
func (u *Updater) Check(ctx context.Context, force bool) (Status, error) {
	cfg := u.Settings.Get().DynDNS
	if !cfg.Enabled {
		return u.Status(ctx), nil
	}
	u.mu.Lock()
	if fp := fingerprint(cfg); fp != u.lastCfg {
		// New credentials or hosts: forget state and pauses.
		u.lastCfg, u.lastIP, u.paused, u.lastErr = fp, "", time.Time{}, ""
	}
	paused := time.Now().Before(u.paused)
	u.mu.Unlock()
	if paused && !force {
		return u.Status(ctx), nil
	}

	ip, err := u.publicIP(ctx, cfg.IPCheckURL)
	if err != nil {
		u.setErr("Öffentliche IP konnte nicht ermittelt werden: " + err.Error())
		return u.Status(ctx), err
	}
	u.mu.Lock()
	u.current = ip
	unchanged := ip == u.lastIP
	u.mu.Unlock()
	if unchanged && !force {
		return u.Status(ctx), nil
	}
	// After a restart lastIP is empty: skip the update if DNS already points to us.
	if !force && u.lastIP == "" && dnsMatches(ctx, cfg.Hostnames, ip) {
		u.mu.Lock()
		u.lastIP = ip
		u.mu.Unlock()
		return u.Status(ctx), nil
	}
	if err := u.update(ctx, cfg, ip); err != nil {
		return u.Status(ctx), err
	}
	return u.Status(ctx), nil
}

func (u *Updater) setErr(msg string) {
	u.mu.Lock()
	u.lastErr = msg
	u.mu.Unlock()
	u.Log.Warn(msg)
}

func dnsMatches(ctx context.Context, hosts []string, ip string) bool {
	if len(hosts) == 0 {
		return false
	}
	for _, h := range hosts {
		addrs, err := net.DefaultResolver.LookupHost(ctx, h)
		if err != nil {
			return false
		}
		found := false
		for _, a := range addrs {
			if a == ip {
				found = true
			}
		}
		if !found {
			return false
		}
	}
	return true
}

func (u *Updater) publicIP(ctx context.Context, checkURL string) (string, error) {
	if checkURL == "" {
		checkURL = "https://api.ipify.org"
	}
	req, err := http.NewRequestWithContext(ctx, "GET", checkURL, nil)
	if err != nil {
		return "", err
	}
	resp, err := u.HTTP.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 256))
	ip := strings.TrimSpace(string(b))
	parsed := net.ParseIP(ip)
	if parsed == nil || parsed.To4() == nil {
		return "", fmt.Errorf("unerwartete Antwort (HTTP %d, keine IPv4-Adresse)", resp.StatusCode)
	}
	return ip, nil
}

// update sends a dyndns2 request and interprets the answer.
func (u *Updater) update(ctx context.Context, cfg store.DynDNSSettings, ip string) error {
	if cfg.UpdateURL == "" || len(cfg.Hostnames) == 0 {
		u.setErr("DynDNS: Update-URL oder Hostname fehlt")
		return fmt.Errorf("incomplete configuration")
	}
	q := url.Values{"hostname": {strings.Join(cfg.Hostnames, ",")}, "myip": {ip}}
	req, err := http.NewRequestWithContext(ctx, "GET", cfg.UpdateURL+"?"+q.Encode(), nil)
	if err != nil {
		return err
	}
	req.SetBasicAuth(cfg.Username, cfg.Password)
	req.Header.Set("User-Agent", "Postwerk-DynDNS/1.0")
	resp, err := u.HTTP.Do(req)
	if err != nil {
		u.setErr("DynDNS-Dienst nicht erreichbar: " + err.Error())
		return err
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
	body := strings.TrimSpace(string(b))
	ok, msg, pause := interpret(resp.StatusCode, body)
	now := time.Now().UTC()
	u.mu.Lock()
	u.lastRes = shortReason(body)
	if ok {
		u.lastIP, u.lastOK, u.lastErr = ip, &now, ""
	} else {
		u.lastErr = msg
		if pause > 0 {
			u.paused = time.Now().Add(pause)
		}
	}
	u.mu.Unlock()
	if ok {
		u.Log.Info("dyndns updated", "hosts", strings.Join(cfg.Hostnames, ","), "ip", ip, "result", body)
		return nil
	}
	u.Log.Warn("dyndns update failed", "hosts", strings.Join(cfg.Hostnames, ","), "status", resp.StatusCode, "err", msg)
	return fmt.Errorf("%s", msg)
}

// interpret maps dyndns2 return codes (one line per hostname) to a result.
func interpret(status int, body string) (ok bool, msg string, pause time.Duration) {
	if status == 401 {
		return false, "badauth – Benutzername oder DynDNS-Passwort falsch", 24 * time.Hour
	}
	if body == "" {
		return false, fmt.Sprintf("leere Antwort (HTTP %d)", status), 30 * time.Minute
	}
	for _, line := range strings.Split(body, "\n") {
		code := strings.ToLower(strings.Fields(line + " x")[0])
		switch code {
		case "good", "nochg":
			continue
		case "badauth":
			return false, "badauth – Benutzername oder DynDNS-Passwort falsch", 24 * time.Hour
		case "nohost":
			return false, "nohost – Hostname nicht gefunden oder DynDNS dafür nicht aktiviert", 24 * time.Hour
		case "notfqdn":
			return false, "notfqdn – Hostname ist kein vollständiger Domainname", 24 * time.Hour
		case "abuse":
			return false, "abuse – der Anbieter hat Aktualisierungen vorübergehend gesperrt", 6 * time.Hour
		case "badagent", "!donator":
			return false, code + " – Anfrage vom Anbieter abgelehnt", 24 * time.Hour
		case "dnserr", "911":
			return false, code + " – Störung beim Anbieter, neuer Versuch später", 30 * time.Minute
		default:
			return false, "unbekannte Antwort: " + shortReason(line), 30 * time.Minute
		}
	}
	return true, "", 0
}

func (u *Updater) Status(ctx context.Context) Status {
	cfg := u.Settings.Get().DynDNS
	u.mu.Lock()
	st := Status{Enabled: cfg.Enabled, DNSIPs: map[string][]string{}, LastUpdate: u.lastOK}
	if u.current != "" {
		c := u.current
		st.CurrentIP = &c
	}
	if u.lastRes != "" {
		r := u.lastRes
		st.LastResult = &r
	}
	if u.lastErr != "" {
		e := u.lastErr
		st.Error = &e
	}
	if time.Now().Before(u.paused) {
		p := u.paused.UTC()
		st.PausedUntil = &p
	}
	if cfg.Enabled && !u.nextCheck.IsZero() {
		n := u.nextCheck.UTC()
		st.NextCheck = &n
	}
	u.mu.Unlock()
	lctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	for _, h := range cfg.Hostnames {
		addrs, _ := net.DefaultResolver.LookupHost(lctx, h)
		if addrs == nil {
			addrs = []string{}
		}
		st.DNSIPs[h] = addrs
	}
	return st
}

// shortReason reduces a provider response to a short, printable first line so
// arbitrary response bodies are never shown in the UI or logs.
func shortReason(s string) string {
	s, _, _ = strings.Cut(strings.TrimSpace(s), "\n")
	s = strings.TrimSpace(s)
	if strings.ContainsAny(s, "<>{}") {
		return "(Antwort nicht darstellbar)"
	}
	out := make([]rune, 0, len(s))
	for _, r := range s {
		if r < 0x20 || r == 0x7f {
			continue
		}
		out = append(out, r)
		if len(out) >= 60 {
			out = append(out, '…')
			break
		}
	}
	return string(out)
}
