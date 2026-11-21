package api

import (
	"context"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"postwerk/internal/mailauth"
	"postwerk/internal/store"
	"postwerk/internal/tlsutil"
)

// mtaSTSPolicy renders our MTA-STS policy from the settings.
func (s *Server) mtaSTSPolicy(cfg store.Settings) string {
	p := cfg.MailAuth.MTASTS
	return mailauth.MTASTSPolicy(p.Mode, []string{cfg.Server.Hostname}, p.MaxAge)
}

// mtaSTSID is the id published in the _mta-sts TXT record. It is derived
// from the policy text, so it changes whenever mode, max_age or the MX host
// change and senders fetch the new policy.
func (s *Server) mtaSTSID(cfg store.Settings) string {
	return mailauth.MTASTSPolicyID(s.mtaSTSPolicy(cfg))
}

func validateMTASTS(c *store.MTASTSSettings) error {
	c.Mode = strings.ToLower(strings.TrimSpace(c.Mode))
	if c.Mode == "" {
		c.Mode = mailauth.MTASTSTesting
	}
	if !mailauth.ValidMTASTSMode(c.Mode) {
		return invalid("Ungültiger MTA-STS-Modus (none, testing oder enforce)")
	}
	if c.MaxAge == 0 {
		c.MaxAge = mailauth.DefaultMTASTSMaxAge
	}
	if c.MaxAge < mailauth.MinMTASTSMaxAge || c.MaxAge > mailauth.MaxMTASTSMaxAge {
		return invalid(fmt.Sprintf("MTA-STS max_age: %d bis %d Sekunden (1 Tag bis 1 Jahr)", mailauth.MinMTASTSMaxAge, mailauth.MaxMTASTSMaxAge))
	}
	return nil
}

// mtaSTSCheck is the certificate check of one host the policy depends on.
type mtaSTSCheck struct {
	Host    string `json:"host"`
	Purpose string `json:"purpose"` // "mx" | "policy"
	OK      bool   `json:"ok"`
	Detail  string `json:"detail"`
}

type mtaSTSStatus struct {
	Mode   string        `json:"mode"`
	MaxAge int           `json:"maxAge"`
	Policy string        `json:"policy"`
	ID     string        `json:"id"`
	Checks []mtaSTSCheck `json:"checks"`
	// EnforceReady is true when every check passed.
	EnforceReady bool `json:"enforceReady"`
}

// mtaSTSProbe fetches the published policy the way sending servers do
// (HTTPS with the system roots). Tests replace it.
var mtaSTSProbe = func(ctx context.Context, domain string) error {
	req, err := http.NewRequestWithContext(ctx, "GET", "https://mta-sts."+domain+"/.well-known/mta-sts.txt", nil)
	if err != nil {
		return err
	}
	c := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := c.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("HTTP-Status %d", resp.StatusCode)
	}
	if !strings.Contains(string(body), "version: STSv1") {
		return errors.New("Antwort ist keine MTA-STS-Policy")
	}
	return nil
}

// certProblem describes why a certificate is not usable for host ("" = fine).
func certProblem(leaf *x509.Certificate, selfSigned bool, problem, host string) string {
	switch {
	case leaf == nil:
		return "Kein Zertifikat vorhanden"
	case selfSigned:
		if problem != "" {
			return problem + " – nur das selbstsignierte Ersatzzertifikat ist aktiv"
		}
		return "Nur ein selbstsigniertes Zertifikat ist aktiv"
	case time.Now().After(leaf.NotAfter):
		return "Zertifikat ist am " + leaf.NotAfter.Local().Format("02.01.2006") + " abgelaufen"
	case leaf.VerifyHostname(host) != nil:
		return "Zertifikat gilt nicht für " + host
	case !tlsutil.IsPublicCA(leaf, host):
		return "Zertifikat stammt nicht von einer öffentlichen Zertifizierungsstelle"
	}
	return ""
}

// localCert checks the certificate Postwerk itself serves for host.
func (s *Server) localCert(ctx context.Context, host string) (bool, string) {
	if s.TLS == nil {
		return false, "Zertifikatsverwaltung nicht verfügbar"
	}
	cs, err := s.TLS.CertFor(ctx, host)
	if err != nil {
		return false, err.Error()
	}
	if p := certProblem(cs.Leaf, cs.SelfSigned, cs.Problem, host); p != "" {
		return false, p
	}
	return true, "Gültiges Zertifikat bis " + cs.Leaf.NotAfter.Local().Format("02.01.2006")
}

// mtaSTSStatusHandler reports the policy and whether the certificates needed
// for "enforce" are in place: a publicly trusted certificate for the MX host
// (STARTTLS) and for mta-sts.<domain> of every domain (policy over HTTPS).
func (s *Server) mtaSTSStatusHandler(w http.ResponseWriter, r *http.Request, u *store.Account) (any, error) {
	cfg := s.Settings.Get()
	st := mtaSTSStatus{Mode: cfg.MailAuth.MTASTS.Mode, MaxAge: cfg.MailAuth.MTASTS.MaxAge, Policy: s.mtaSTSPolicy(cfg), ID: s.mtaSTSID(cfg), Checks: []mtaSTSCheck{}}
	ctx, cancel := context.WithTimeout(r.Context(), 8*time.Second)
	defer cancel()

	host := cfg.Server.Hostname
	ok, detail := s.localCert(ctx, host)
	st.Checks = append(st.Checks, mtaSTSCheck{Host: host, Purpose: "mx", OK: ok, Detail: detail})

	domains, err := s.Store.ListDomains(ctx)
	if err != nil {
		return nil, err
	}
	checks := make([]mtaSTSCheck, len(domains))
	var wg sync.WaitGroup
	for i, d := range domains {
		wg.Add(1)
		go func(i int, domain string) {
			defer wg.Done()
			name := "mta-sts." + domain
			c := mtaSTSCheck{Host: name, Purpose: "policy"}
			pctx, pcancel := context.WithTimeout(ctx, 5*time.Second)
			probeErr := mtaSTSProbe(pctx, domain)
			pcancel()
			if probeErr == nil {
				c.OK, c.Detail = true, "Policy öffentlich per HTTPS abrufbar, Zertifikat gültig"
			} else if cfg.Web.BehindProxy {
				c.Detail = "Abruf über HTTPS fehlgeschlagen (" + probeErr.Error() + "). Das Zertifikat liefert der Reverse-Proxy – dort mta-sts." + domain + " aufnehmen."
			} else if lok, ldetail := s.localCert(ctx, name); lok {
				// Our own certificate is fine; the probe may fail without
				// hairpin NAT, so this counts as ready.
				c.OK, c.Detail = true, ldetail+" (öffentlicher Abruf von hier aus nicht möglich: "+probeErr.Error()+")"
			} else {
				c.Detail = ldetail + "; Abruf über HTTPS fehlgeschlagen (" + probeErr.Error() + ")"
			}
			checks[i] = c
		}(i, d.Name)
	}
	wg.Wait()
	st.Checks = append(st.Checks, checks...)
	st.EnforceReady = true
	for _, c := range st.Checks {
		st.EnforceReady = st.EnforceReady && c.OK
	}
	return st, nil
}
