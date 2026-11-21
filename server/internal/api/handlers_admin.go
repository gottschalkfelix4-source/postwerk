package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"postwerk/internal/dnsprov"
	"regexp"
	"strings"
	"time"

	"postwerk/internal/auth"
	"postwerk/internal/logx"
	"postwerk/internal/mailauth"
	"postwerk/internal/store"
)

// ---- dashboard ----

func (s *Server) dashboard(w http.ResponseWriter, r *http.Request, u *store.Account) (any, error) {
	ctx := r.Context()
	received, hours := s.Store.StatSeries(ctx, "received", 24)
	sent, _ := s.Store.StatSeries(ctx, "sent", 24)
	spamS, _ := s.Store.StatSeries(ctx, "spam", 24)
	virus, _ := s.Store.StatSeries(ctx, "virus", 24)
	rejected, _ := s.Store.StatSeries(ctx, "rejected", 24)
	nAcc, _ := s.Store.CountAccounts(ctx)
	domains, _ := s.Store.ListDomains(ctx)
	msgs, bytes := s.Store.TotalMessages(ctx)
	cats, _ := s.Store.CategoryCounts(ctx, time.Now().AddDate(0, 0, -7))
	hourLabels := make([]string, len(hours))
	for i, h := range hours {
		hourLabels[i] = h.Format(time.RFC3339)
	}
	cfg := s.Settings.Get().AI
	reachable := false
	if cfg.Enabled {
		c, cancel := context.WithTimeout(ctx, 3*time.Second)
		_, err := s.AI.Models(c)
		cancel()
		reachable = err == nil
	}
	events := s.Logs.Query("warn", "", 10)
	if len(events) < 10 {
		events = s.Logs.Query("info", "", 10)
	}
	return map[string]any{
		"stats": map[string]any{
			"accounts": nAcc, "domains": len(domains), "messages": msgs, "storageBytes": bytes, "queueSize": s.Store.QueueSize(ctx),
			"received24h": store.Sum(received), "sent24h": store.Sum(sent), "spam24h": store.Sum(spamS),
			"virus24h": store.Sum(virus), "rejected24h": store.Sum(rejected),
		},
		"series":        map[string]any{"hours": hourLabels, "received": received, "sent": sent, "spam": spamS},
		"categories":    cats,
		"recentEvents":  events,
		"version":       s.Version,
		"uptimeSeconds": int64(time.Since(s.Started).Seconds()),
		"ai":            map[string]any{"enabled": cfg.Enabled, "reachable": reachable, "model": cfg.ChatModel},
		"warnings":      s.dashboardWarnings(ctx),
	}, nil
}

// ---- domains ----

var reDomain = regexp.MustCompile(`^([a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?\.)+[a-z]{2,}$`)
var reLocal = regexp.MustCompile(`^[a-z0-9._%+\-]+$`)

func validEmail(e string) bool {
	local, dom, ok := strings.Cut(e, "@")
	return ok && reLocal.MatchString(local) && (reDomain.MatchString(dom) || dom == "localhost")
}

func (s *Server) listDomains(w http.ResponseWriter, r *http.Request, u *store.Account) (any, error) {
	d, err := s.Store.ListDomains(r.Context())
	if d == nil {
		d = []store.Domain{}
	}
	return d, err
}

func (s *Server) createDomain(w http.ResponseWriter, r *http.Request, u *store.Account) (any, error) {
	var req struct{ Name string }
	if err := decode(r, &req); err != nil {
		return nil, err
	}
	name := store.Normalize(req.Name)
	if !reDomain.MatchString(name) && name != "localhost" {
		return nil, invalid("Ungültiger Domainname")
	}
	key, err := mailauth.GenerateKey()
	if err != nil {
		return nil, err
	}
	selector := "pw" + time.Now().Format("200601")
	if err := s.Store.CreateDomain(r.Context(), name, selector, key); err != nil {
		return nil, err
	}
	s.addInitialEd25519Key(r.Context(), name)
	s.Log.Info("domain created", "component", "admin", "domain", name, "by", u.Email)
	return s.Store.GetDomain(r.Context(), name)
}

func (s *Server) patchDomain(w http.ResponseWriter, r *http.Request, u *store.Account) (any, error) {
	name := r.PathValue("name")
	var req struct {
		CatchAll *string `json:"catchAll"`
	}
	if err := decode(r, &req); err != nil {
		return nil, err
	}
	if req.CatchAll != nil && *req.CatchAll != "" {
		c := store.Normalize(*req.CatchAll)
		if !validEmail(c) {
			return nil, invalid("Ungültige Catch-all-Adresse")
		}
		req.CatchAll = &c
	}
	if err := s.Store.SetCatchAll(r.Context(), name, req.CatchAll); err != nil {
		return nil, err
	}
	return s.Store.GetDomain(r.Context(), name)
}

func (s *Server) deleteDomain(w http.ResponseWriter, r *http.Request, u *store.Account) (any, error) {
	d, err := s.Store.GetDomain(r.Context(), r.PathValue("name"))
	if err != nil {
		return nil, err
	}
	if d.AccountCount > 0 || d.AliasCount > 0 {
		return nil, errf(409, "conflict", "Domain hat noch Konten oder Aliase")
	}
	return ok(), s.Store.DeleteDomain(r.Context(), d.Name)
}

// dnsRecord is one recommended DNS entry. Besides the zone-file view
// (name/value) it carries the value split into the fields that provider
// forms ask for (host/prefix, priority, weight, port, target …).
type dnsRecord struct {
	Key      string `json:"key"`
	Type     string `json:"type"`
	Name     string `json:"name"`
	Value    string `json:"value"`
	Purpose  string `json:"purpose"`
	Title    string `json:"title"`
	Required bool   `json:"required"`

	Zone        string  `json:"zone"`        // the mail domain
	Host        string  `json:"host"`        // name relative to the domain, "" = the domain itself
	OutsideZone bool    `json:"outsideZone"` // record lives in another domain (e.g. hostname elsewhere)
	Content     string  `json:"content"`     // value without trailing dot (A/TXT/CNAME)
	Priority    *int    `json:"priority"`    // MX, SRV
	Weight      *int    `json:"weight"`      // SRV
	Port        *int    `json:"port"`        // SRV
	Target      *string `json:"target"`      // MX/SRV target without trailing dot
	Service     *string `json:"service"`     // SRV, e.g. "submissions" (without underscore)
	Protocol    *string `json:"protocol"`    // SRV, e.g. "tcp"
}

func ip(i int) *int         { return &i }
func sptr(s string) *string { return &s }

// relHost returns the name relative to zone ("" for the zone itself).
func relHost(fqdn, zone string) (string, bool) {
	f := strings.TrimSuffix(strings.ToLower(fqdn), ".")
	z := strings.ToLower(zone)
	switch {
	case f == z:
		return "", false
	case strings.HasSuffix(f, "."+z):
		return strings.TrimSuffix(f, "."+z), false
	}
	return f, true
}

// dnsRecords lists the recommended DNS entries for a domain.
func (s *Server) dnsRecords(d *store.Domain) ([]dnsRecord, error) {
	host := s.Settings.Get().Server.Hostname
	dkim, err := mailauth.DNSValue(d.DKIMPrivateKey)
	if err != nil {
		return nil, err
	}
	n := d.Name
	opt := s.dnsOptions(s.Settings.Get())
	spf := dnsprov.MergeSPF("", opt.SPFInclude)
	spfPurpose := "Legt fest, welche Server Mails für " + n + " verschicken dürfen. Ohne diesen Eintrag landen deine Mails oft im Spam."
	if opt.SPFInclude != "" {
		spfPurpose = "Legt fest, wer Mails für " + n + " verschicken darf: dein Mailserver und der Smarthost (" + strings.TrimPrefix(opt.SPFInclude, "include:") + ")."
	}
	aValue, aPurpose := "<öffentliche IPv4 des Servers>", "Die Adresse deines Mailservers im Internet. Trage hier deine öffentliche IP ein."
	if opt.DynDNS {
		aValue, aPurpose = "<wird per DynDNS gesetzt>", "Die Adresse deines Mailservers. Postwerk hält sie per DynDNS automatisch aktuell – hier nichts eintragen."
	}
	recs := []dnsRecord{
		{Key: "a", Type: "A", Name: host + ".", Value: aValue, Title: "Mailserver-Adresse", Purpose: aPurpose, Required: true, Content: aValue},
		{Key: "mx", Type: "MX", Name: n + ".", Value: "10 " + host + ".", Title: "Posteingang (MX)",
			Purpose: "Sagt anderen Mailservern, wohin Mails für " + n + " geschickt werden. Ersetzt den bisherigen Mailanbieter.", Required: true,
			Priority: ip(10), Target: sptr(host)},
		{Key: "spf", Type: "TXT", Name: n + ".", Value: spf, Title: "Absender-Erlaubnis (SPF)", Purpose: spfPurpose, Required: true, Content: spf},
		{Key: "dkim", Type: "TXT", Name: d.DKIMSelector + "._domainkey." + n + ".", Value: dkim, Title: "Digitale Signatur (DKIM)",
			Purpose: "Öffentlicher Schlüssel, mit dem Empfänger prüfen, dass deine Mails echt sind.", Required: true, Content: dkim},
		{Key: "dmarc", Type: "TXT", Name: "_dmarc." + n + ".", Value: "v=DMARC1; p=quarantine; adkim=r; aspf=r; rua=mailto:postmaster@" + n,
			Title: "Schutz vor Fälschungen (DMARC)", Purpose: "Sagt Empfängern, was mit gefälschten Mails in deinem Namen passieren soll.", Required: true},
		{Key: "mta-sts-txt", Type: "TXT", Name: "_mta-sts." + n + ".", Value: "v=STSv1; id=" + time.Now().Format("20060102"), Title: "Verschlüsselung erzwingen (MTA-STS)",
			Purpose: "Optional: Andere Server liefern nur noch verschlüsselt an dich aus."},
		{Key: "mta-sts-cname", Type: "CNAME", Name: "mta-sts." + n + ".", Value: host + ".", Title: "MTA-STS-Adresse", Purpose: "Optional: gehört zu MTA-STS."},
		{Key: "tlsrpt", Type: "TXT", Name: "_smtp._tls." + n + ".", Value: "v=TLSRPTv1; rua=mailto:postmaster@" + n, Title: "TLS-Berichte",
			Purpose: "Optional: Andere Server melden dir Probleme mit der Verschlüsselung."},
		{Key: "srv-submissions", Type: "SRV", Name: "_submissions._tcp." + n + ".", Value: "0 1 465 " + host + ".", Title: "Automatische Einrichtung: Versand (Port 465)",
			Purpose: "Optional: Mailprogramme finden die Versand-Einstellungen von selbst.", Priority: ip(0), Weight: ip(1), Port: ip(465), Target: sptr(host), Service: sptr("submissions"), Protocol: sptr("tcp")},
		{Key: "srv-submission", Type: "SRV", Name: "_submission._tcp." + n + ".", Value: "0 1 587 " + host + ".", Title: "Automatische Einrichtung: Versand (Port 587)",
			Purpose: "Optional: wie oben, für Port 587.", Priority: ip(0), Weight: ip(1), Port: ip(587), Target: sptr(host), Service: sptr("submission"), Protocol: sptr("tcp")},
		{Key: "srv-imaps", Type: "SRV", Name: "_imaps._tcp." + n + ".", Value: "0 1 993 " + host + ".", Title: "Automatische Einrichtung: Abruf (IMAP)",
			Purpose: "Optional: Mailprogramme finden die IMAP-Einstellungen von selbst.", Priority: ip(0), Weight: ip(1), Port: ip(993), Target: sptr(host), Service: sptr("imaps"), Protocol: sptr("tcp")},
		{Key: "srv-pop3s", Type: "SRV", Name: "_pop3s._tcp." + n + ".", Value: "0 1 995 " + host + ".", Title: "Automatische Einrichtung: Abruf (POP3)",
			Purpose: "Optional: wie oben, für POP3.", Priority: ip(0), Weight: ip(1), Port: ip(995), Target: sptr(host), Service: sptr("pop3s"), Protocol: sptr("tcp")},
		{Key: "srv-caldavs", Type: "SRV", Name: "_caldavs._tcp." + n + ".", Value: "0 1 443 " + host + ".", Title: "Automatische Einrichtung: Kalender (CalDAV)",
			Purpose: "Optional: Kalender-Apps (z. B. iPhone, DAVx⁵, Thunderbird) finden den CalDAV-Server von selbst.", Priority: ip(0), Weight: ip(1), Port: ip(443), Target: sptr(host), Service: sptr("caldavs"), Protocol: sptr("tcp")},
		{Key: "txt-caldavs", Type: "TXT", Name: "_caldavs._tcp." + n + ".", Value: "path=/dav/", Title: "Automatische Einrichtung: CalDAV-Pfad",
			Purpose: "Optional: gehört zum CalDAV-SRV-Eintrag und nennt den Pfad."},
		{Key: "srv-carddavs", Type: "SRV", Name: "_carddavs._tcp." + n + ".", Value: "0 1 443 " + host + ".", Title: "Automatische Einrichtung: Kontakte (CardDAV)",
			Purpose: "Optional: Adressbuch-Apps finden den CardDAV-Server von selbst.", Priority: ip(0), Weight: ip(1), Port: ip(443), Target: sptr(host), Service: sptr("carddavs"), Protocol: sptr("tcp")},
		{Key: "txt-carddavs", Type: "TXT", Name: "_carddavs._tcp." + n + ".", Value: "path=/dav/", Title: "Automatische Einrichtung: CardDAV-Pfad",
			Purpose: "Optional: gehört zum CardDAV-SRV-Eintrag und nennt den Pfad."},
		{Key: "autoconfig", Type: "CNAME", Name: "autoconfig." + n + ".", Value: host + ".", Title: "Automatische Einrichtung: Thunderbird",
			Purpose: "Optional: Thunderbird richtet Konten damit selbst ein."},
	}
	// Further DKIM keys (Ed25519, rotation) follow the primary DKIM record.
	if extra := s.extraDKIMRecords(context.Background(), d); len(extra) > 0 {
		recs = append(recs[:4], append(extra, recs[4:]...)...)
	}
	for i := range recs {
		r := &recs[i]
		r.Zone = n
		r.Host, r.OutsideZone = relHost(r.Name, n)
		switch r.Type {
		case "TXT":
			r.Content = r.Value
		case "CNAME":
			r.Content = strings.TrimSuffix(r.Value, ".")
		}
	}
	return recs, nil
}

func (s *Server) domainDNS(w http.ResponseWriter, r *http.Request, u *store.Account) (any, error) {
	d, err := s.Store.GetDomain(r.Context(), r.PathValue("name"))
	if err != nil {
		return nil, err
	}
	return s.dnsRecords(d)
}

// ---- accounts ----

func (s *Server) listAccounts(w http.ResponseWriter, r *http.Request, u *store.Account) (any, error) {
	items, total, err := s.Store.ListAccounts(r.Context(), r.URL.Query().Get("q"), queryInt(r, "limit", 50, 200), queryInt(r, "offset", 0, 0))
	if err != nil {
		return nil, err
	}
	if items == nil {
		items = []*store.Account{}
	}
	return map[string]any{"total": total, "items": items}, nil
}

func (s *Server) createAccount(w http.ResponseWriter, r *http.Request, u *store.Account) (any, error) {
	var req struct {
		Email      string `json:"email"`
		Name       string `json:"name"`
		Password   string `json:"password"`
		Role       string `json:"role"`
		QuotaBytes int64  `json:"quotaBytes"`
		Source     string `json:"source"`
	}
	if err := decode(r, &req); err != nil {
		return nil, err
	}
	if req.Source == "" {
		req.Source = store.SourceLocal
	}
	if !accountSourceValid(req.Source) {
		return nil, invalid("Ungültige Anmeldequelle")
	}
	email := store.Normalize(req.Email)
	if !validEmail(email) {
		return nil, invalid("Ungültige E-Mail-Adresse")
	}
	if ok, _ := s.Store.DomainExists(r.Context(), store.DomainOf(email)); !ok {
		return nil, invalid("Domain " + store.DomainOf(email) + " ist nicht angelegt")
	}
	if _, err := s.Store.GetAlias(r.Context(), email); err == nil {
		return nil, errf(409, "conflict", "Es gibt bereits einen Alias mit dieser Adresse")
	}
	if req.Role != "admin" {
		req.Role = "user"
	}
	// LDAP and SSO accounts have no local password.
	h := noPasswordHash
	if req.Source == store.SourceLocal {
		if err := validPassword(req.Password); err != nil {
			return nil, err
		}
		var err error
		if h, err = auth.HashPassword(req.Password); err != nil {
			return nil, err
		}
	}
	a := &store.Account{Email: email, Name: strings.TrimSpace(req.Name), PasswordHash: h, Role: req.Role, QuotaBytes: req.QuotaBytes, Source: req.Source}
	if err := s.Store.CreateAccount(r.Context(), a); err != nil {
		return nil, err
	}
	s.Log.Info("account created", "component", "admin", "account", email, "by", u.Email)
	return s.Store.GetAccount(r.Context(), a.ID)
}

func (s *Server) getAccount(w http.ResponseWriter, r *http.Request, u *store.Account) (any, error) {
	id, err := pathID(r, "id")
	if err != nil {
		return nil, err
	}
	return s.Store.GetAccount(r.Context(), id)
}

func (s *Server) patchAccount(w http.ResponseWriter, r *http.Request, u *store.Account) (any, error) {
	id, err := pathID(r, "id")
	if err != nil {
		return nil, err
	}
	var req struct {
		Name       *string `json:"name"`
		Password   *string `json:"password"`
		Role       *string `json:"role"`
		QuotaBytes *int64  `json:"quotaBytes"`
		Disabled   *bool   `json:"disabled"`
		ResetTOTP  bool    `json:"resetTotp"`
		Source     *string `json:"source"`
	}
	if err := decode(r, &req); err != nil {
		return nil, err
	}
	target, err := s.Store.GetAccount(r.Context(), id)
	if err != nil {
		return nil, err
	}
	source := target.Source
	if req.Source != nil && *req.Source != target.Source {
		if !accountSourceValid(*req.Source) {
			return nil, invalid("Ungültige Anmeldequelle")
		}
		if id == u.ID {
			return nil, invalid("Du kannst die Anmeldequelle deines eigenen Kontos nicht ändern")
		}
		if *req.Source == store.SourceLocal && (req.Password == nil || *req.Password == "") {
			return nil, invalid("Für ein lokales Konto bitte ein Passwort festlegen")
		}
		source = *req.Source
	}
	if req.Password != nil && *req.Password != "" && source != store.SourceLocal {
		return nil, invalid("Das Passwort dieses Kontos wird extern verwaltet (LDAP oder Single Sign-on)")
	}
	p := store.AccountPatch{Name: req.Name, QuotaBytes: req.QuotaBytes, Disabled: req.Disabled}
	if source != target.Source {
		p.Source = &source
		if source != store.SourceLocal && target.Source == store.SourceLocal {
			// The local password no longer applies.
			h, f := noPasswordHash, false
			p.PasswordHash, p.MustChangePassword = &h, &f
		}
	}
	if req.Role != nil {
		if *req.Role != "admin" && *req.Role != "user" {
			return nil, invalid("Ungültige Rolle")
		}
		if id == u.ID && *req.Role != "admin" {
			return nil, invalid("Du kannst dir selbst die Admin-Rolle nicht entziehen")
		}
		p.Role = req.Role
	}
	if req.Disabled != nil && *req.Disabled && id == u.ID {
		return nil, invalid("Du kannst dein eigenes Konto nicht deaktivieren")
	}
	if req.Password != nil && *req.Password != "" {
		if err := validPassword(*req.Password); err != nil {
			return nil, err
		}
		h, err := auth.HashPassword(*req.Password)
		if err != nil {
			return nil, err
		}
		p.PasswordHash = &h
		if id != u.ID {
			// The user must replace a password an admin has set.
			t := true
			p.MustChangePassword = &t
		}
	}
	if req.ResetTOTP {
		f, empty := false, ""
		p.TOTPEnabled, p.TOTPSecret = &f, &empty
	}
	if err := s.Store.UpdateAccount(r.Context(), id, p); err != nil {
		return nil, err
	}
	if p.PasswordHash != nil || (req.Disabled != nil && *req.Disabled) {
		_ = s.Store.DeleteAccountSessions(r.Context(), id)
	}
	if p.PasswordHash != nil {
		// A password reset must also lock out mail clients using app passwords.
		if err := s.Store.DeleteAccountAppPasswords(r.Context(), id); err != nil {
			return nil, err
		}
		s.Log.Info("password reset by admin, sessions and app passwords revoked", "component", "admin", "account", id, "by", u.Email)
	}
	return s.Store.GetAccount(r.Context(), id)
}

func (s *Server) deleteAccount(w http.ResponseWriter, r *http.Request, u *store.Account) (any, error) {
	id, err := pathID(r, "id")
	if err != nil {
		return nil, err
	}
	if id == u.ID {
		return nil, invalid("Du kannst dein eigenes Konto nicht löschen")
	}
	s.Log.Info("account deleted", "component", "admin", "id", fmt.Sprint(id), "by", u.Email)
	return ok(), s.Store.DeleteAccount(r.Context(), id)
}

// ---- aliases ----

func (s *Server) listAliases(w http.ResponseWriter, r *http.Request, u *store.Account) (any, error) {
	return s.Store.ListAliases(r.Context())
}

func (s *Server) validTargets(targets []string) error {
	if len(targets) == 0 {
		return invalid("Mindestens ein Ziel angeben")
	}
	for _, t := range targets {
		if !validEmail(store.Normalize(t)) {
			return invalid("Ungültiges Ziel: " + t)
		}
	}
	return nil
}

func (s *Server) createAlias(w http.ResponseWriter, r *http.Request, u *store.Account) (any, error) {
	var req struct {
		Address string   `json:"address"`
		Targets []string `json:"targets"`
	}
	if err := decode(r, &req); err != nil {
		return nil, err
	}
	addr := store.Normalize(req.Address)
	if !validEmail(addr) {
		return nil, invalid("Ungültige Alias-Adresse")
	}
	if ok, _ := s.Store.DomainExists(r.Context(), store.DomainOf(addr)); !ok {
		return nil, invalid("Domain " + store.DomainOf(addr) + " ist nicht angelegt")
	}
	if _, err := s.Store.GetAccountByEmail(r.Context(), addr); err == nil {
		return nil, errf(409, "conflict", "Es gibt bereits ein Konto mit dieser Adresse")
	}
	if err := s.validTargets(req.Targets); err != nil {
		return nil, err
	}
	return s.Store.CreateAlias(r.Context(), addr, req.Targets)
}

func (s *Server) patchAlias(w http.ResponseWriter, r *http.Request, u *store.Account) (any, error) {
	id, err := pathID(r, "id")
	if err != nil {
		return nil, err
	}
	var req struct {
		Targets []string `json:"targets"`
	}
	if err := decode(r, &req); err != nil {
		return nil, err
	}
	if err := s.validTargets(req.Targets); err != nil {
		return nil, err
	}
	if err := s.Store.UpdateAlias(r.Context(), id, req.Targets); err != nil {
		return nil, err
	}
	all, err := s.Store.ListAliases(r.Context())
	if err != nil {
		return nil, err
	}
	for _, a := range all {
		if a.ID == id {
			return a, nil
		}
	}
	return nil, errNotFound
}

func (s *Server) deleteAlias(w http.ResponseWriter, r *http.Request, u *store.Account) (any, error) {
	id, err := pathID(r, "id")
	if err != nil {
		return nil, err
	}
	return ok(), s.Store.DeleteAlias(r.Context(), id)
}

// ---- queue, logs, security ----

func (s *Server) listQueue(w http.ResponseWriter, r *http.Request, u *store.Account) (any, error) {
	return s.Store.ListQueue(r.Context())
}

func (s *Server) retryQueue(w http.ResponseWriter, r *http.Request, u *store.Account) (any, error) {
	id, err := pathID(r, "id")
	if err != nil {
		return nil, err
	}
	if err := s.Store.RetryQueueItem(r.Context(), id); err != nil {
		return nil, err
	}
	if s.Delivery.Queue != nil {
		s.Delivery.Queue.Notify()
	}
	return ok(), nil
}

func (s *Server) deleteQueue(w http.ResponseWriter, r *http.Request, u *store.Account) (any, error) {
	id, err := pathID(r, "id")
	if err != nil {
		return nil, err
	}
	return ok(), s.Store.DeleteQueueItem(r.Context(), id)
}

func (s *Server) logs(w http.ResponseWriter, r *http.Request, u *store.Account) (any, error) {
	q := r.URL.Query()
	out := s.Logs.Query(q.Get("level"), q.Get("q"), queryInt(r, "limit", 200, 1000))
	if out == nil {
		out = []logx.Entry{}
	}
	return out, nil
}

func (s *Server) bans(w http.ResponseWriter, r *http.Request, u *store.Account) (any, error) {
	return s.Auth.Guard.List(), nil
}

func (s *Server) unban(w http.ResponseWriter, r *http.Request, u *store.Account) (any, error) {
	s.Auth.Guard.Unban(r.PathValue("ip"))
	s.Log.Info("ip unbanned", "component", "security", "ip", r.PathValue("ip"), "by", u.Email)
	return ok(), nil
}

// ---- settings ----

const mask = "••••"

const (
	webdavSecretMsg = "WebDAV: Die URL wurde geändert – bitte das Passwort erneut eingeben."
	smbSecretMsg    = "SMB: Der Server wurde geändert – bitte das Passwort erneut eingeben."
)

// maskedSecret ties a secret to the endpoint it is sent to.
type maskedSecret struct {
	cur                    *string
	old                    string
	endpoint, oldEndpoint  string
	changedEndpointMessage string
}

// restore replaces the mask with the stored secret if the endpoint is
// unchanged. A changed endpoint requires the secret to be entered again.
func (m maskedSecret) restore() error {
	if *m.cur != mask {
		return nil
	}
	if m.old != "" && !sameEndpoint(m.endpoint, m.oldEndpoint) {
		return invalid(m.changedEndpointMessage)
	}
	*m.cur = m.old
	return nil
}

func sameEndpoint(a, b string) bool {
	norm := func(s string) string { return strings.TrimRight(strings.ToLower(strings.TrimSpace(s)), "/") }
	return norm(a) == norm(b)
}

func (s *Server) maskedSettings() store.Settings {
	cur := s.Settings.Get()
	if cur.AI.APIKey != "" {
		cur.AI.APIKey = mask
	}
	if cur.AI.EmbeddingAPIKey != "" {
		cur.AI.EmbeddingAPIKey = mask
	}
	if cur.DMS.Paperless.Token != "" {
		cur.DMS.Paperless.Token = mask
	}
	if cur.DynDNS.Password != "" {
		cur.DynDNS.Password = mask
	}
	if cur.Relay.Password != "" {
		cur.Relay.Password = mask
	}
	for _, p := range []*string{&cur.Backup.Password, &cur.Backup.Target.WebDAV.Password, &cur.Backup.Target.SMB.Password,
		&cur.LDAP.BindPassword, &cur.OIDC.ClientSecret} {
		if *p != "" {
			*p = mask
		}
	}
	cur.TLS.Status = s.TLS.Status()
	if s.HTTPPort != nil {
		_, cur.TLS.HTTPStatus = s.HTTPPort.Status()
	}
	return cur
}

func (s *Server) getSettings(w http.ResponseWriter, r *http.Request, u *store.Account) (any, error) {
	return s.maskedSettings(), nil
}

func (s *Server) putSettings(w http.ResponseWriter, r *http.Request, u *store.Account) (any, error) {
	var patch map[string]json.RawMessage
	if err := decode(r, &patch); err != nil {
		return nil, err
	}
	cur := s.Settings.Get()
	old := s.Settings.Get()
	oldKey, oldToken, oldACME := cur.AI.APIKey, cur.DMS.Paperless.Token, cur.TLS.ACME
	oldEmbedKey := cur.AI.EmbeddingAPIKey
	oldDyn, oldRelay := cur.DynDNS.Password, cur.Relay.Password
	oldBackup := cur.Backup
	sections := map[string]any{
		"server": &cur.Server, "web": &cur.Web, "tls": &cur.TLS, "spam": &cur.Spam, "antivirus": &cur.Antivirus,
		"ai": &cur.AI, "dms": &cur.DMS, "security": &cur.Security, "dyndns": &cur.DynDNS, "relay": &cur.Relay,
		"backup": &cur.Backup, "ldap": &cur.LDAP, "oidc": &cur.OIDC, "mailauth": &cur.MailAuth,
	}
	for k, raw := range patch {
		target, known := sections[k]
		if !known {
			return nil, invalid("Unbekannter Abschnitt: " + k)
		}
		if err := json.Unmarshal(raw, target); err != nil {
			return nil, invalid("Abschnitt " + k + ": " + err.Error())
		}
	}
	// A masked secret keeps its stored value only while it still goes to the
	// same endpoint; otherwise it would be sent to a host the admin just entered.
	for _, sc := range []maskedSecret{
		{&cur.AI.APIKey, oldKey, cur.AI.BaseURL, old.AI.BaseURL, "KI: Die Server-URL wurde geändert – bitte den API-Schlüssel erneut eingeben."},
		{&cur.AI.EmbeddingAPIKey, oldEmbedKey, cur.AI.EmbeddingBaseURL, old.AI.EmbeddingBaseURL, "KI: Die Embedding-URL wurde geändert – bitte den Embedding-API-Schlüssel erneut eingeben."},
		{&cur.DynDNS.Password, oldDyn, cur.DynDNS.UpdateURL, old.DynDNS.UpdateURL, "DynDNS: Die Update-URL wurde geändert – bitte das Passwort erneut eingeben."},
		{&cur.Relay.Password, oldRelay, cur.Relay.Host, old.Relay.Host, "Relay: Der Server wurde geändert – bitte das Passwort erneut eingeben."},
		{&cur.DMS.Paperless.Token, oldToken, cur.DMS.Paperless.URL, old.DMS.Paperless.URL, "Paperless: Die URL wurde geändert – bitte das Token erneut eingeben."},
		{&cur.Backup.Password, oldBackup.Password, "", "", ""},
		{&cur.Backup.Target.WebDAV.Password, oldBackup.Target.WebDAV.Password, cur.Backup.Target.WebDAV.URL, oldBackup.Target.WebDAV.URL, webdavSecretMsg},
		{&cur.Backup.Target.SMB.Password, oldBackup.Target.SMB.Password, cur.Backup.Target.SMB.Host, oldBackup.Target.SMB.Host, smbSecretMsg},
		{&cur.LDAP.BindPassword, old.LDAP.BindPassword, cur.LDAP.URL, old.LDAP.URL, ldapSecretMsg},
		{&cur.OIDC.ClientSecret, old.OIDC.ClientSecret, cur.OIDC.IssuerURL, old.OIDC.IssuerURL, oidcSecretMsg},
	} {
		if err := sc.restore(); err != nil {
			return nil, err
		}
	}
	cur.TLS.Status = ""
	if err := validateSettings(&cur, &old); err != nil {
		return nil, err
	}
	if err := s.validateDirectorySettings(r.Context(), &cur); err != nil {
		return nil, err
	}
	if err := s.Settings.Save(r.Context(), cur); err != nil {
		return nil, err
	}
	keys := []string{}
	for k := range patch {
		keys = append(keys, k)
	}
	s.Log.Info("settings updated", "component", "admin", "sections", strings.Join(keys, ","), "by", u.Email)
	if fmt.Sprint(oldACME) != fmt.Sprint(cur.TLS.ACME) || patch["tls"] != nil {
		s.Log.Info("tls settings changed", "component", "tls", "status", s.TLS.Status())
	}
	if s.AIWorker != nil {
		s.AIWorker.Notify()
	}
	if s.Backups != nil && patch["backup"] != nil {
		s.Backups.Notify()
	}
	if s.DynDNS != nil && patch["dyndns"] != nil {
		s.DynDNS.Notify()
	}
	if s.HTTPPort != nil && (patch["tls"] != nil || patch["web"] != nil || patch["server"] != nil) {
		s.HTTPPort.Notify()
	}
	return s.maskedSettings(), nil
}

func validateSettings(c, old *store.Settings) error {
	if err := validateBackup(&c.Backup, &old.Backup); err != nil {
		return err
	}
	if err := validateMailAuth(&c.MailAuth); err != nil {
		return err
	}
	switch c.AI.AutoFile.Mode {
	case "", "categories":
		c.AI.AutoFile.Mode = "categories"
	case "folders":
	default:
		return invalid("Ungültiger Modus für das Einsortieren")
	}
	for k, v := range c.AI.AutoFile.CategoryFolders {
		v = strings.Trim(strings.TrimSpace(v), "/")
		if len(v) > 200 || strings.Contains(v, "//") {
			return invalid("Ungültiger Ordnername für Kategorie " + k)
		}
		if strings.EqualFold(v, "INBOX") || strings.EqualFold(v, "Posteingang") {
			v = ""
		}
		c.AI.AutoFile.CategoryFolders[k] = v
	}
	c.Server.Hostname = strings.TrimSpace(strings.ToLower(c.Server.Hostname))
	if c.Server.Hostname == "" {
		return invalid("Hostname darf nicht leer sein")
	}
	if c.Server.MaxMessageSize < 1<<20 {
		return invalid("Maximale Nachrichtengröße muss mindestens 1 MB sein")
	}
	for _, d := range []int{c.Server.Retention.TrashDays, c.Server.Retention.JunkDays} {
		if d < 0 || d > 3650 {
			return invalid("Aufbewahrung für Papierkorb und Spam: 0 (nie leeren) bis 3650 Tage")
		}
	}
	if c.Spam.JunkThreshold <= 0 {
		return invalid("Spam-Schwelle muss größer als 0 sein")
	}
	if c.Spam.RejectThreshold != 0 && c.Spam.RejectThreshold <= c.Spam.JunkThreshold {
		return invalid("Ablehnungs-Schwelle muss größer als die Spam-Schwelle sein")
	}
	switch c.TLS.Mode {
	case "acme", "files", "selfsigned":
	default:
		return invalid("Ungültige Zertifikatsquelle")
	}
	c.TLS.ACME.Enabled = c.TLS.Mode == "acme"
	switch c.TLS.HTTPPort {
	case "open", "redirect", "auto-close":
	case "":
		c.TLS.HTTPPort = "open"
	default:
		return invalid("Ungültige Einstellung für Port 80")
	}
	c.TLS.HTTPStatus = ""
	if c.TLS.Mode == "files" && strings.TrimSpace(c.TLS.Files.CertPath) == "" {
		return invalid("Bitte den Pfad zum Zertifikat angeben")
	}
	if c.DynDNS.IntervalMinutes < 1 {
		c.DynDNS.IntervalMinutes = 5
	}
	hosts := []string{}
	for _, h := range c.DynDNS.Hostnames {
		if h = strings.ToLower(strings.TrimSpace(h)); h != "" {
			hosts = append(hosts, h)
		}
	}
	c.DynDNS.Hostnames = hosts
	if c.DynDNS.Enabled {
		if !strings.HasPrefix(c.DynDNS.UpdateURL, "https://") && !strings.HasPrefix(c.DynDNS.UpdateURL, "http://") {
			return invalid("DynDNS: ungültige Update-URL")
		}
		if len(hosts) == 0 || c.DynDNS.Username == "" || c.DynDNS.Password == "" {
			return invalid("DynDNS: Hostname, Benutzername und Passwort sind nötig")
		}
	}
	c.Relay.Host = strings.TrimSpace(c.Relay.Host)
	if c.Relay.Security != "tls" {
		c.Relay.Security = "starttls"
	}
	if c.Relay.Port <= 0 || c.Relay.Port > 65535 {
		c.Relay.Port = 587
	}
	if c.Relay.Enabled && c.Relay.Host == "" {
		return invalid("Smarthost: Server fehlt")
	}
	c.Relay.SPFInclude = strings.TrimSpace(c.Relay.SPFInclude)
	if c.Relay.SPFInclude != "" && !strings.HasPrefix(c.Relay.SPFInclude, "include:") && !strings.HasPrefix(c.Relay.SPFInclude, "ip4:") && !strings.HasPrefix(c.Relay.SPFInclude, "ip6:") && !strings.HasPrefix(c.Relay.SPFInclude, "a:") {
		return invalid("Smarthost: SPF-Eintrag muss mit include:, ip4:, ip6: oder a: beginnen")
	}
	c.Web.PublicURL = strings.TrimRight(strings.TrimSpace(c.Web.PublicURL), "/")
	if c.Web.PublicURL != "" && !strings.HasPrefix(c.Web.PublicURL, "http://") && !strings.HasPrefix(c.Web.PublicURL, "https://") {
		return invalid("Die öffentliche URL muss mit http:// oder https:// beginnen")
	}
	proxies := []string{}
	for _, p := range c.Web.TrustedProxies {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		if _, _, err := net.ParseCIDR(p); err != nil && net.ParseIP(p) == nil {
			return invalid("Ungültige Proxy-Adresse: " + p)
		}
		proxies = append(proxies, p)
	}
	c.Web.TrustedProxies = proxies
	if c.Antivirus.Action != "reject" && c.Antivirus.Action != "quarantine" {
		c.Antivirus.Action = "reject"
	}
	if c.Security.BruteForce.MaxAttempts < 1 {
		c.Security.BruteForce.MaxAttempts = 5
	}
	if c.Security.BruteForce.WindowSeconds < 10 {
		c.Security.BruteForce.WindowSeconds = 600
	}
	if c.Security.BruteForce.BanSeconds < 10 {
		c.Security.BruteForce.BanSeconds = 3600
	}
	if c.AI.TimeoutSeconds <= 0 {
		c.AI.TimeoutSeconds = 120
	}
	switch strings.ToLower(strings.TrimSpace(c.AI.Provider)) {
	case "", "openai":
		c.AI.Provider = "openai"
	case "anthropic":
		c.AI.Provider = "anthropic"
	default:
		return invalid("Unbekannter KI-Anbieter: " + c.AI.Provider)
	}
	c.AI.BaseURL = strings.TrimSpace(c.AI.BaseURL)
	c.AI.EmbeddingBaseURL = strings.TrimSpace(c.AI.EmbeddingBaseURL)
	for _, u := range []string{c.AI.BaseURL, c.AI.EmbeddingBaseURL} {
		if u != "" && !strings.HasPrefix(u, "http://") && !strings.HasPrefix(u, "https://") {
			return invalid("KI-Adresse muss mit http:// oder https:// beginnen: " + u)
		}
	}
	if c.AI.MaxTokens <= 0 {
		c.AI.MaxTokens = 16000
	}
	if c.AI.MaxTokens > 128000 {
		c.AI.MaxTokens = 128000
	}
	clean := []string{}
	for _, cat := range c.AI.Categories {
		if cat = strings.TrimSpace(cat); cat != "" {
			clean = append(clean, cat)
		}
	}
	c.AI.Categories = clean
	return nil
}

func (s *Server) testAI(w http.ResponseWriter, r *http.Request, u *store.Account) (any, error) {
	start := time.Now()
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	res, err := s.AI.TestConnection(ctx)
	if err != nil {
		return map[string]any{"ok": false, "latencyMs": time.Since(start).Milliseconds(), "models": []string{}, "error": err.Error()}, nil
	}
	out := map[string]any{"ok": res.ChatError == "", "latencyMs": time.Since(start).Milliseconds(), "models": res.Models, "chatOk": res.ChatOK}
	if res.ChatError != "" {
		out["error"] = "Chat-Modell: " + res.ChatError
	}
	if res.EmbeddingError != "" {
		out["embeddingError"] = res.EmbeddingError
	}
	out["embeddingOk"] = res.EmbeddingOK
	return out, nil
}
