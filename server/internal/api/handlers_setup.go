package api

import (
	"context"
	"crypto/subtle"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"postwerk/internal/auth"
	"postwerk/internal/av"
	"postwerk/internal/mailauth"
	"postwerk/internal/store"
)

const flagSetupDone = "setup_completed"

func (s *Server) setupMode(ctx context.Context) (string, error) {
	n, err := s.Store.CountAccounts(ctx)
	if err != nil {
		return "", err
	}
	switch {
	case n == 0:
		return "initial", nil
	case s.Store.Flag(ctx, flagSetupDone):
		return "done", nil
	}
	return "configure", nil
}

// SetupDone reports whether the setup assistant was completed.
func (s *Server) SetupDone(ctx context.Context) bool {
	return s.Store.Flag(ctx, flagSetupDone)
}

func (s *Server) setupStatus(w http.ResponseWriter, r *http.Request) (any, error) {
	mode, err := s.setupMode(r.Context())
	if err != nil {
		return nil, err
	}
	return map[string]any{"mode": mode, "hostname": s.Settings.Get().Server.Hostname, "version": s.Version}, nil
}

func (s *Server) setupInitialize(w http.ResponseWriter, r *http.Request) (any, error) {
	ip := s.clientIP(r)
	if s.Auth.Guard.Banned(ip) {
		return nil, errf(429, "rate_limited", "Zu viele Fehlversuche. Bitte später erneut versuchen.")
	}
	var req struct {
		Token         string `json:"token"`
		Hostname      string `json:"hostname"`
		Domain        string `json:"domain"`
		AdminEmail    string `json:"adminEmail"`
		AdminName     string `json:"adminName"`
		AdminPassword string `json:"adminPassword"`
	}
	if err := decode(r, &req); err != nil {
		return nil, err
	}
	s.setupMu.Lock()
	defer s.setupMu.Unlock()
	if mode, err := s.setupMode(r.Context()); err != nil {
		return nil, err
	} else if mode != "initial" {
		return nil, errf(409, "already_initialized", "Postwerk ist bereits eingerichtet")
	}
	if s.SetupToken == "" || subtle.ConstantTimeCompare([]byte(auth.NormalizeAppPassword(req.Token)), []byte(auth.NormalizeAppPassword(s.SetupToken))) != 1 {
		s.Auth.Guard.Fail(ip, "setup token")
		return nil, errf(403, "invalid_token", "Setup-Token ist ungültig")
	}
	host := strings.ToLower(strings.TrimSpace(req.Hostname))
	domain := store.Normalize(req.Domain)
	email := store.Normalize(req.AdminEmail)
	if host == "" || (!reDomain.MatchString(host) && host != "localhost") {
		return nil, invalid("Ungültiger Hostname")
	}
	if !reDomain.MatchString(domain) && domain != "localhost" {
		return nil, invalid("Ungültige Domain")
	}
	if !validEmail(email) || store.DomainOf(email) != domain {
		return nil, invalid("Die Admin-Adresse muss zur Domain " + domain + " gehören")
	}
	if err := validPassword(req.AdminPassword); err != nil {
		return nil, err
	}
	ctx := r.Context()
	cfg := s.Settings.Get()
	cfg.Server.Hostname = host
	if err := s.Settings.Save(ctx, cfg); err != nil {
		return nil, err
	}
	if ok, _ := s.Store.DomainExists(ctx, domain); !ok {
		key, err := mailauth.GenerateKey()
		if err != nil {
			return nil, err
		}
		if err := s.Store.CreateDomain(ctx, domain, "pw"+time.Now().Format("200601"), key); err != nil {
			return nil, err
		}
	}
	h, err := auth.HashPassword(req.AdminPassword)
	if err != nil {
		return nil, err
	}
	name := strings.TrimSpace(req.AdminName)
	if name == "" {
		name = "Administrator"
	}
	acct := &store.Account{Email: email, Name: name, PasswordHash: h, Role: "admin"}
	if err := s.Store.CreateAccount(ctx, acct); err != nil {
		return nil, err
	}
	s.SetupToken = ""
	_ = os.Remove(filepath.Join(s.Cfg.DataDir, "setup-token.txt"))
	s.Log.Info("setup: first admin account created", "component", "setup", "email", email, "domain", domain, "hostname", host)
	if err := s.startSession(w, r, acct.ID); err != nil {
		return nil, err
	}
	a, err := s.Store.GetAccount(ctx, acct.ID)
	if err != nil {
		return nil, err
	}
	return toMe(a), nil
}

func (s *Server) setupComplete(w http.ResponseWriter, r *http.Request, u *store.Account) (any, error) {
	if err := s.Store.SetFlag(r.Context(), flagSetupDone, true); err != nil {
		return nil, err
	}
	s.Log.Info("setup assistant completed", "component", "setup", "by", u.Email)
	return ok(), nil
}

// ---- DNS check ----

type dnsCheck struct {
	Key      string   `json:"key"`
	Title    string   `json:"title"`
	Type     string   `json:"type"`
	Name     string   `json:"name"`
	Expected string   `json:"expected"`
	Found    []string `json:"found"`
	Status   string   `json:"status"`
	Purpose  string   `json:"purpose"`
	Required bool     `json:"required"`
}

func txt(ctx context.Context, name string) []string {
	recs, err := net.DefaultResolver.LookupTXT(ctx, strings.TrimSuffix(name, "."))
	if err != nil {
		return []string{}
	}
	return recs
}

func normSpace(s string) string { return strings.Join(strings.Fields(s), "") }

func (s *Server) domainDNSCheck(w http.ResponseWriter, r *http.Request, u *store.Account) (any, error) {
	d, err := s.Store.GetDomain(r.Context(), r.PathValue("name"))
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	host := s.Settings.Get().Server.Hostname
	recs, err := s.dnsRecords(d)
	if err != nil {
		return nil, err
	}
	hostIPs, _ := net.DefaultResolver.LookupHost(ctx, host)
	out := []dnsCheck{}
	for _, rec := range recs {
		c := dnsCheck{Key: rec.Key, Title: rec.Title, Type: rec.Type, Name: rec.Name, Expected: rec.Value, Purpose: rec.Purpose, Found: []string{}, Required: rec.Required}
		name := strings.TrimSuffix(rec.Name, ".")
		fail := func() string {
			if rec.Required {
				return "missing"
			}
			return "warning"
		}
		switch {
		case rec.Type == "A":
			c.Found = append(c.Found, hostIPs...)
			c.Status = "ok"
			if len(hostIPs) == 0 {
				c.Status = fail()
			}
		case rec.Type == "MX":
			mxs, _ := net.DefaultResolver.LookupMX(ctx, name)
			c.Status = fail()
			for _, mx := range mxs {
				h := strings.TrimSuffix(mx.Host, ".")
				c.Found = append(c.Found, h)
				if strings.EqualFold(h, host) {
					c.Status = "ok"
				}
			}
			if c.Status != "ok" && len(mxs) > 0 {
				c.Status = "mismatch"
			}
		case rec.Type == "TXT":
			prefix := strings.SplitN(rec.Value, ";", 2)[0]
			prefix = strings.Fields(prefix)[0]
			c.Status = fail()
			for _, t := range txt(ctx, name) {
				if !strings.HasPrefix(strings.ToLower(normSpace(t)), strings.ToLower(normSpace(prefix))) {
					continue
				}
				c.Found = append(c.Found, t)
				switch {
				case strings.HasPrefix(prefix, "v=spf1"):
					c.Status = "mismatch"
					lt := " " + strings.ToLower(t) + " "
					if strings.Contains(lt, " mx ") || strings.Contains(lt, " mx:"+d.Name) || strings.Contains(lt, " +mx ") || strings.Contains(lt, "a:"+host) {
						c.Status = "ok"
					}
					// With a smarthost its include must be present as well.
					if inc := strings.ToLower(s.dnsOptions(s.Settings.Get()).SPFInclude); inc != "" && !strings.Contains(lt, " "+inc+" ") {
						c.Status = "mismatch"
					}
					for _, ip := range hostIPs {
						if strings.Contains(lt, "ip4:"+ip) || strings.Contains(lt, "ip6:"+ip) {
							c.Status = "ok"
						}
					}
				case strings.HasPrefix(prefix, "v=DKIM1"):
					want := rec.Value[strings.Index(rec.Value, "p=")+2:]
					c.Status = "mismatch"
					if strings.Contains(normSpace(t), normSpace(want)) {
						c.Status = "ok"
					}
				default:
					c.Status = "ok"
				}
			}
		case rec.Type == "CNAME":
			if ips, err := net.DefaultResolver.LookupHost(ctx, name); err == nil {
				c.Found = ips
				c.Status = "ok"
			} else {
				c.Status = fail()
			}
		case rec.Type == "SRV":
			parts := strings.SplitN(name, ".", 3)
			if len(parts) == 3 {
				_, srvs, err := net.DefaultResolver.LookupSRV(ctx, strings.TrimPrefix(parts[0], "_"), strings.TrimPrefix(parts[1], "_"), parts[2])
				c.Status = fail()
				if err == nil {
					for _, sv := range srvs {
						c.Found = append(c.Found, strings.TrimSuffix(sv.Target, ".")+":"+itoa(int(sv.Port)))
						if strings.EqualFold(strings.TrimSuffix(sv.Target, "."), host) {
							c.Status = "ok"
						}
					}
				}
			}
		default:
			c.Status = "info"
		}
		out = append(out, c)
	}
	return out, nil
}

func itoa(i int) string { return strconv.Itoa(i) }

// ---- system check ----

func (s *Server) systemCheck(w http.ResponseWriter, r *http.Request, u *store.Account) (any, error) {
	ctx, cancel := context.WithTimeout(r.Context(), 25*time.Second)
	defer cancel()
	cfg := s.Settings.Get()
	host := cfg.Server.Hostname
	ips, _ := net.DefaultResolver.LookupHost(ctx, host)
	type ptr struct {
		IP      string   `json:"ip"`
		Names   []string `json:"names"`
		Matches bool     `json:"matches"`
	}
	ptrs := []ptr{}
	for _, ip := range ips {
		p := ptr{IP: ip, Names: []string{}}
		names, _ := net.DefaultResolver.LookupAddr(ctx, ip)
		for _, n := range names {
			n = strings.TrimSuffix(n, ".")
			p.Names = append(p.Names, n)
			if strings.EqualFold(n, host) {
				p.Matches = true
			}
		}
		ptrs = append(ptrs, p)
	}
	if ips == nil {
		ips = []string{}
	}

	port25, port25Detail := s.checkPort25(ctx)

	clam, clamDetail := "info", "Virenscan ist deaktiviert"
	if cfg.Antivirus.Enabled {
		if err := av.Ping(cfg.Antivirus.ClamdAddress); err != nil {
			clam, clamDetail = "error", "ClamAV unter "+cfg.Antivirus.ClamdAddress+" nicht erreichbar: "+err.Error()
		} else {
			clam, clamDetail = "ok", "ClamAV antwortet unter "+cfg.Antivirus.ClamdAddress
		}
	}
	aiStatus, aiDetail := "info", "KI ist deaktiviert"
	if cfg.AI.Enabled {
		actx, acancel := context.WithTimeout(ctx, 8*time.Second)
		models, err := s.AI.Models(actx)
		acancel()
		if err != nil {
			aiStatus, aiDetail = "error", "KI-Endpunkt nicht erreichbar: "+err.Error()
		} else {
			aiStatus, aiDetail = "ok", "Endpunkt erreichbar, "+itoa(len(models))+" Modelle verfügbar"
		}
	}
	return map[string]any{
		"hostname": host, "hostnameIps": ips, "ptr": ptrs,
		"port25Outbound": port25, "port25Detail": port25Detail,
		"tls": s.TLS.Status(), "clamav": clam, "clamavDetail": clamDetail, "ai": aiStatus, "aiDetail": aiDetail,
	}, nil
}

// checkPort25 tries to reach a large public MX on port 25.
func (s *Server) checkPort25(ctx context.Context) (string, string) {
	if s.Cfg.RelayHost != "" {
		return "info", "Ausgehende Mails laufen über den Smarthost " + s.Cfg.RelayHost
	}
	mxs, err := net.DefaultResolver.LookupMX(ctx, "gmail.com")
	if err != nil || len(mxs) == 0 {
		return "warning", "DNS-Auflösung für den Test fehlgeschlagen"
	}
	d := net.Dialer{Timeout: 6 * time.Second}
	conn, err := d.DialContext(ctx, "tcp", net.JoinHostPort(strings.TrimSuffix(mxs[0].Host, "."), "25"))
	if err != nil {
		return "error", "Port 25 ausgehend scheint gesperrt (" + err.Error() + "). Smarthost über POSTWERK_RELAY_HOST eintragen."
	}
	defer conn.Close()
	_ = conn.SetReadDeadline(time.Now().Add(6 * time.Second))
	buf := make([]byte, 64)
	n, _ := conn.Read(buf)
	if strings.HasPrefix(string(buf[:n]), "220") {
		return "ok", "Verbindung zu " + strings.TrimSuffix(mxs[0].Host, ".") + ":25 möglich"
	}
	return "warning", "Verbindung aufgebaut, aber keine SMTP-Begrüßung erhalten"
}

func (s *Server) testClamAV(w http.ResponseWriter, r *http.Request, u *store.Account) (any, error) {
	var req struct{ Address string }
	if err := decode(r, &req); err != nil {
		return nil, err
	}
	if strings.TrimSpace(req.Address) == "" {
		return nil, invalid("Adresse fehlt")
	}
	if err := av.Ping(req.Address); err != nil {
		return map[string]any{"ok": false, "error": err.Error()}, nil
	}
	return map[string]any{"ok": true}, nil
}
