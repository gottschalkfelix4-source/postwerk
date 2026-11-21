package api

import (
	"context"
	"fmt"
	"net"
	"strings"
	"sync"
	"time"

	"postwerk/internal/spam"
	"postwerk/internal/store"
)

// dashWarning is a problem shown prominently on the admin dashboard.
type dashWarning struct {
	ID       string `json:"id"`
	Severity string `json:"severity"` // warning | error
	Title    string `json:"title"`
	Detail   string `json:"detail"`
	Link     string `json:"link,omitempty"` // route in the web interface
}

const (
	certWarnDays    = 14
	queueWarnAge    = time.Hour
	dnsblInterval   = 6 * time.Hour
	ptrInterval     = time.Hour
	netCheckTimeout = 20 * time.Second
)

// dnsblZones are checked for the server's own public IPv4 and IPv6
// addresses (both lists answer IPv6 queries per RFC 5782).
var dnsblZones = []string{"zen.spamhaus.org", "b.barracudacentral.org"}

// netChecks caches the DNS based checks (DNSBL, PTR); they run in the
// background so the dashboard never waits for them.
type netChecks struct {
	mu        sync.Mutex
	running   bool
	host      string
	dnsblAt   time.Time
	ptrAt     time.Time
	dnsbl     []dashWarning
	ptr       []dashWarning
	lookupIPs func(ctx context.Context, host string) ([]string, error)
	lookupPTR func(ctx context.Context, ip string) ([]string, error)
	lookupA   func(ctx context.Context, name string) ([]string, error)
}

var opsNet = &netChecks{
	lookupIPs: net.DefaultResolver.LookupHost,
	lookupPTR: net.DefaultResolver.LookupAddr,
	lookupA:   net.DefaultResolver.LookupHost,
}

// snapshot returns the cached results and starts a refresh when they are stale.
func (n *netChecks) snapshot(host string) []dashWarning {
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.host != host {
		n.host, n.dnsblAt, n.ptrAt, n.dnsbl, n.ptr = host, time.Time{}, time.Time{}, nil, nil
	}
	dueDNSBL := time.Since(n.dnsblAt) >= dnsblInterval
	duePTR := time.Since(n.ptrAt) >= ptrInterval
	if (dueDNSBL || duePTR) && !n.running {
		n.running = true
		go n.refresh(host, dueDNSBL, duePTR)
	}
	return append(append([]dashWarning{}, n.dnsbl...), n.ptr...)
}

func (n *netChecks) refresh(host string, doDNSBL, doPTR bool) {
	ctx, cancel := context.WithTimeout(context.Background(), netCheckTimeout)
	defer cancel()
	ips := publicIPs(ctx, n.lookupIPs, host)
	var bl, ptr []dashWarning
	if doDNSBL {
		bl = n.checkDNSBL(ctx, ips)
	}
	if doPTR {
		ptr = n.checkPTR(ctx, host, ips)
	}
	n.mu.Lock()
	defer n.mu.Unlock()
	n.running = false
	if n.host != host {
		return
	}
	now := time.Now()
	if doDNSBL {
		n.dnsbl, n.dnsblAt = bl, now
	}
	if doPTR {
		n.ptr, n.ptrAt = ptr, now
	}
}

// publicIPs resolves the mail hostname to its public IPv4 and IPv6 addresses.
func publicIPs(ctx context.Context, lookup func(context.Context, string) ([]string, error), host string) []net.IP {
	addrs, _ := lookup(ctx, host)
	var out []net.IP
	for _, a := range addrs {
		ip := net.ParseIP(a)
		if ip == nil || !ip.IsGlobalUnicast() || ip.IsPrivate() {
			continue
		}
		if v4 := ip.To4(); v4 != nil {
			ip = v4
		}
		out = append(out, ip)
	}
	return out
}

func (n *netChecks) checkDNSBL(ctx context.Context, ips []net.IP) []dashWarning {
	var out []dashWarning
	for _, ip := range ips {
		rev := spam.ReverseIP(ip)
		if rev == "" {
			continue
		}
		for _, zone := range dnsblZones {
			answers, err := n.lookupA(ctx, rev+"."+zone)
			if err != nil {
				continue // NXDOMAIN: not listed
			}
			if spam.DNSBLListed(answers) {
				out = append(out, dashWarning{
					ID: "dnsbl-" + ip.String() + "-" + zone, Severity: "error",
					Title:  fmt.Sprintf("IP %s steht auf der Sperrliste %s", ip, zone),
					Detail: "Viele Empfänger lehnen Mails von dieser Adresse ab. Ursache (z. B. offenes Relay, kompromittiertes Konto) beheben und beim Betreiber der Liste die Entfernung beantragen.",
				})
			}
		}
	}
	return out
}

func (n *netChecks) checkPTR(ctx context.Context, host string, ips []net.IP) []dashWarning {
	var out []dashWarning
	for _, ip := range ips {
		names, _ := n.lookupPTR(ctx, ip.String())
		match := false
		for _, nm := range names {
			if strings.EqualFold(strings.TrimSuffix(nm, "."), host) {
				match = true
			}
		}
		if !match {
			found := "kein PTR-Eintrag"
			if len(names) > 0 {
				found = "zeigt auf " + strings.TrimSuffix(names[0], ".")
			}
			out = append(out, dashWarning{
				ID: "ptr-" + ip.String(), Severity: "warning",
				Title:  fmt.Sprintf("Reverse-DNS von %s passt nicht zu %s", ip, host),
				Detail: "Der PTR-Eintrag (" + found + ") sollte auf den Mail-Hostnamen zeigen; viele Empfänger stufen Mails sonst als Spam ein. Den PTR setzt der Provider der IP-Adresse.",
			})
		}
	}
	return out
}

// backupInterval returns the configured schedule interval (0 = no schedule).
func backupInterval(b store.BackupSettings) time.Duration {
	if !b.Schedule.Enabled {
		return 0
	}
	switch b.Schedule.Frequency {
	case "hourly":
		return time.Hour
	case "weekly":
		return 7 * 24 * time.Hour
	}
	return 24 * time.Hour
}

func (s *Server) dashboardWarnings(ctx context.Context) []dashWarning {
	out := []dashWarning{}
	cfg := s.Settings.Get()
	host := cfg.Server.Hostname

	// Certificate.
	if s.TLS != nil {
		cs, err := s.TLS.Current(ctx)
		switch {
		case err != nil:
			out = append(out, dashWarning{ID: "cert", Severity: "error", Title: "Zertifikat nicht lesbar", Detail: err.Error(), Link: "/admin/settings/tls"})
		case cs.SelfSigned:
			detail := "Mailprogramme und andere Server vertrauen diesem Zertifikat nicht."
			if cs.Problem != "" {
				detail = cs.Problem + ". " + detail
			}
			out = append(out, dashWarning{ID: "cert", Severity: "warning", Title: "Selbstsigniertes Zertifikat aktiv", Detail: detail, Link: "/admin/settings/tls"})
		default:
			left := time.Until(cs.Leaf.NotAfter)
			switch {
			case left <= 0:
				out = append(out, dashWarning{ID: "cert", Severity: "error", Title: "Zertifikat ist abgelaufen",
					Detail: "Abgelaufen am " + cs.Leaf.NotAfter.Local().Format("02.01.2006 15:04") + ".", Link: "/admin/settings/tls"})
			case left < certWarnDays*24*time.Hour:
				out = append(out, dashWarning{ID: "cert", Severity: "warning",
					Title:  fmt.Sprintf("Zertifikat läuft in %d Tagen ab", int(left.Hours()/24)),
					Detail: "Gültig bis " + cs.Leaf.NotAfter.Local().Format("02.01.2006 15:04") + ". Bei Let's Encrypt prüfen, ob die Erneuerung (Port 80) funktioniert.", Link: "/admin/settings/tls"})
			}
			if host != "" && cs.Leaf.VerifyHostname(host) != nil {
				out = append(out, dashWarning{ID: "cert-host", Severity: "warning", Title: "Zertifikat gilt nicht für " + host,
					Detail: "Mailprogramme melden einen Zertifikatsfehler.", Link: "/admin/settings/tls"})
			}
		}
	}

	// Queue.
	if oldest, ok := s.Store.OldestQueued(ctx); ok && time.Since(oldest) > queueWarnAge {
		out = append(out, dashWarning{ID: "queue", Severity: "warning",
			Title:  fmt.Sprintf("Nachrichten warten seit %s in der Warteschlange", ageText(time.Since(oldest))),
			Detail: fmt.Sprintf("%d Nachricht(en) in der Warteschlange. Die Fehlermeldungen dort zeigen, warum die Zustellung scheitert.", s.Store.QueueSize(ctx)),
			Link:   "/admin/queue"})
	}

	// Backup.
	if last, lastOK, err := s.Store.LastBackupRun(ctx); err == nil {
		if last != nil && last.Status == "failed" {
			out = append(out, dashWarning{ID: "backup-failed", Severity: "error", Title: "Letzte Sicherung fehlgeschlagen", Detail: last.Error, Link: "/admin/backup"})
		}
		if iv := backupInterval(cfg.Backup); iv > 0 && !lastOK.IsZero() && time.Since(lastOK) > 2*iv {
			out = append(out, dashWarning{ID: "backup-old", Severity: "warning",
				Title:  "Letzte erfolgreiche Sicherung vor " + ageText(time.Since(lastOK)),
				Detail: "Laut Zeitplan sollte häufiger gesichert werden.", Link: "/admin/backup"})
		}
	}

	// Reputation of the own IP only matters when mail leaves directly.
	if host != "" && s.Cfg.RelayHost == "" && !(cfg.Relay.Enabled && cfg.Relay.Host != "") {
		out = append(out, opsNet.snapshot(host)...)
	}
	return out
}

func ageText(d time.Duration) string {
	switch {
	case d >= 48*time.Hour:
		return fmt.Sprintf("%d Tagen", int(d.Hours()/24))
	case d >= 2*time.Hour:
		return fmt.Sprintf("%d Stunden", int(d.Hours()))
	}
	return fmt.Sprintf("%d Minuten", int(d.Minutes()))
}
