package api

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"postwerk/internal/dnsprov"
	"postwerk/internal/mailauth"
	"postwerk/internal/store"
)

// ---- DKIM keys ----

// dkimKeyView is a DKIM key with its DNS record for the admin UI.
type dkimKeyView struct {
	store.DKIMKey
	DNSName     string     `json:"dnsName"`
	DNSValue    string     `json:"dnsValue"`
	RemovableAt *time.Time `json:"removableAt"`
}

func dkimView(k store.DKIMKey) dkimKeyView {
	v := dkimKeyView{DKIMKey: k, DNSName: k.Selector + "._domainkey." + k.Domain + ".", RemovableAt: k.RemovableAt()}
	v.DNSValue, _ = mailauth.DNSValue(k.PrivateKey)
	return v
}

// dkimErr turns state machine errors into 409 responses.
func dkimErr(err error) error {
	var se *store.DKIMStateError
	if errors.As(err, &se) {
		return errf(409, "conflict", se.Msg)
	}
	return err
}

func (s *Server) listDKIMKeys(w http.ResponseWriter, r *http.Request, u *store.Account) (any, error) {
	d, err := s.Store.GetDomain(r.Context(), r.PathValue("name"))
	if err != nil {
		return nil, err
	}
	keys, err := s.Store.ListDKIMKeys(r.Context(), d.Name)
	if err != nil {
		return nil, err
	}
	out := make([]dkimKeyView, 0, len(keys))
	for _, k := range keys {
		out = append(out, dkimView(k))
	}
	return out, nil
}

// newDKIMKey generates and stores a key for a domain.
func (s *Server) newDKIMKey(ctx context.Context, domain, alg, state string) (*store.DKIMKey, error) {
	keys, err := s.Store.ListDKIMKeys(ctx, domain)
	if err != nil {
		return nil, err
	}
	pemKey, err := mailauth.GenerateKeyFor(alg)
	if err != nil {
		return nil, invalid("Unbekannter Schlüsseltyp")
	}
	k, err := s.Store.AddDKIMKey(ctx, domain, store.NextDKIMSelector(keys, alg, time.Now()), alg, pemKey, state)
	return k, dkimErr(err)
}

func (s *Server) createDKIMKey(w http.ResponseWriter, r *http.Request, u *store.Account) (any, error) {
	var req struct {
		Algorithm string `json:"algorithm"`
	}
	if err := decode(r, &req); err != nil {
		return nil, err
	}
	if req.Algorithm == "" {
		req.Algorithm = store.DKIMAlgRSA
	}
	if req.Algorithm != store.DKIMAlgRSA && req.Algorithm != store.DKIMAlgEd25519 {
		return nil, invalid("Schlüsseltyp muss rsa oder ed25519 sein")
	}
	d, err := s.Store.GetDomain(r.Context(), r.PathValue("name"))
	if err != nil {
		return nil, err
	}
	k, err := s.newDKIMKey(r.Context(), d.Name, req.Algorithm, store.DKIMPending)
	if err != nil {
		return nil, err
	}
	s.Log.Info("dkim key created", "component", "admin", "domain", d.Name, "selector", k.Selector, "algorithm", k.Algorithm, "by", u.Email)
	return dkimView(*k), nil
}

func (s *Server) dkimKeyFromPath(r *http.Request) (*store.DKIMKey, error) {
	id, err := pathID(r, "id")
	if err != nil {
		return nil, err
	}
	return s.Store.GetDKIMKey(r.Context(), r.PathValue("name"), id)
}

func (s *Server) checkDKIMKey(w http.ResponseWriter, r *http.Request, u *store.Account) (any, error) {
	k, err := s.dkimKeyFromPath(r)
	if err != nil {
		return nil, err
	}
	return mailauth.CheckPublished(r.Context(), k.Domain, k.Selector, k.PrivateKey), nil
}

func (s *Server) activateDKIMKey(w http.ResponseWriter, r *http.Request, u *store.Account) (any, error) {
	var req struct {
		Force bool `json:"force"`
	}
	if err := decode(r, &req); err != nil {
		return nil, err
	}
	k, err := s.dkimKeyFromPath(r)
	if err != nil {
		return nil, err
	}
	if k.State != store.DKIMPending {
		return nil, errf(409, "conflict", "Nur neue (noch nicht aktive) Schlüssel können aktiviert werden")
	}
	if !req.Force {
		if pub := mailauth.CheckPublished(r.Context(), k.Domain, k.Selector, k.PrivateKey); !pub.Published {
			return nil, errf(409, "dkim_not_published", "Der neue Schlüssel ist noch nicht im DNS veröffentlicht ("+pub.Error+"). Erst den TXT-Eintrag anlegen und prüfen.")
		}
	}
	if err := s.Store.ActivateDKIMKey(r.Context(), k.Domain, k.ID); err != nil {
		return nil, dkimErr(err)
	}
	s.Log.Info("dkim key activated", "component", "admin", "domain", k.Domain, "selector", k.Selector, "algorithm", k.Algorithm, "forced", req.Force, "by", u.Email)
	return s.listDKIMKeys(w, r, u)
}

func (s *Server) deleteDKIMKey(w http.ResponseWriter, r *http.Request, u *store.Account) (any, error) {
	k, err := s.dkimKeyFromPath(r)
	if err != nil {
		return nil, err
	}
	if err := s.Store.DeleteDKIMKey(r.Context(), k.Domain, k.ID); err != nil {
		return nil, dkimErr(err)
	}
	s.Log.Info("dkim key deleted", "component", "admin", "domain", k.Domain, "selector", k.Selector, "state", k.State, "by", u.Email)
	return ok(), nil
}

// addInitialEd25519Key gives a new domain a pending Ed25519 key that can be
// activated for dual signing once its DNS record is published.
func (s *Server) addInitialEd25519Key(ctx context.Context, domain string) {
	if _, err := s.newDKIMKey(ctx, domain, store.DKIMAlgEd25519, store.DKIMPending); err != nil {
		s.Log.Warn("creating ed25519 dkim key failed", "component", "admin", "domain", domain, "err", err)
	}
}

// extraDKIMRecords returns the DNS records of all keys except the primary
// (active RSA) key, which dnsRecords lists under the key "dkim".
func (s *Server) extraDKIMRecords(ctx context.Context, d *store.Domain) []dnsRecord {
	keys, err := s.Store.ListDKIMKeys(ctx, d.Name)
	if err != nil {
		return nil
	}
	var out []dnsRecord
	for _, k := range keys {
		if k.Selector == d.DKIMSelector {
			continue
		}
		v, err := mailauth.DNSValue(k.PrivateKey)
		if err != nil {
			continue
		}
		rec := dnsRecord{Key: "dkim-" + k.Selector, Type: "TXT", Name: k.Selector + "._domainkey." + d.Name + ".", Value: v, Content: v}
		alg := "RSA"
		if k.Algorithm == store.DKIMAlgEd25519 {
			alg = "Ed25519"
		}
		switch k.State {
		case store.DKIMActive:
			rec.Title = "Digitale Signatur (DKIM, " + alg + ")"
			rec.Purpose = "Zweiter aktiver Schlüssel: Mails werden zusätzlich mit " + alg + " signiert."
			rec.Required = true
		case store.DKIMPending:
			rec.Title = "Neuer DKIM-Schlüssel (" + alg + ", noch nicht aktiv)"
			rec.Purpose = "Vor dem Aktivieren veröffentlichen. Danach unter „DKIM-Schlüssel“ prüfen und aktivieren."
		case store.DKIMRetired:
			rec.Title = "Alter DKIM-Schlüssel (" + alg + ", ausgemustert)"
			rec.Purpose = "Wird nicht mehr zum Signieren benutzt. Bitte noch veröffentlicht lassen"
			if t := k.RemovableAt(); t != nil {
				if time.Now().After(*t) {
					rec.Purpose = "Wird nicht mehr benutzt und kann jetzt aus dem DNS entfernt werden"
				} else {
					rec.Purpose += " bis " + t.Local().Format("02.01.2006")
				}
			}
			rec.Purpose += "."
		default:
			continue
		}
		out = append(out, rec)
	}
	return out
}

// extraDKIMDesired returns the additional DKIM records (active and pending,
// not retired) for the automatic DNS setup.
func (s *Server) extraDKIMDesired(ctx context.Context, d *store.Domain) []dnsprov.DKIMRecord {
	keys, err := s.Store.ListDKIMKeys(ctx, d.Name)
	if err != nil {
		return nil
	}
	var out []dnsprov.DKIMRecord
	for _, k := range keys {
		if k.Selector == d.DKIMSelector || k.State == store.DKIMRetired {
			continue
		}
		v, err := mailauth.DNSValue(k.PrivateKey)
		if err != nil {
			continue
		}
		purpose := "DKIM-Schlüssel (" + k.Algorithm + ")"
		if k.State == store.DKIMPending {
			purpose = "Neuer DKIM-Schlüssel (" + k.Algorithm + ", noch nicht aktiv)"
		}
		out = append(out, dnsprov.DKIMRecord{Selector: k.Selector, Value: v, Purpose: purpose, Required: k.State == store.DKIMActive})
	}
	return out
}

// ---- system aliases ----

func (s *Server) listSystemAliases(w http.ResponseWriter, r *http.Request, u *store.Account) (any, error) {
	return s.Store.ListSystemAliases(r.Context())
}

// ---- settings ----

// validateMailAuth normalizes the mail authentication settings.
func validateMailAuth(c *store.MailAuthSettings) error {
	seen := map[string]bool{}
	sealers := []string{}
	for _, d := range c.TrustedARCSealers {
		d = strings.Trim(strings.ToLower(strings.TrimSpace(d)), ".")
		if d == "" || seen[d] {
			continue
		}
		if !reDomain.MatchString(d) {
			return invalid("Ungültige Domain in der ARC-Liste: " + d)
		}
		seen[d] = true
		sealers = append(sealers, d)
	}
	c.TrustedARCSealers = sealers
	c.PostmasterAddress = store.Normalize(c.PostmasterAddress)
	if c.PostmasterAddress != "" && !validEmail(c.PostmasterAddress) {
		return invalid("Ungültige Adresse für postmaster@ und abuse@")
	}
	if store.IsSystemAddress(c.PostmasterAddress) {
		return invalid("Die Zieladresse darf selbst keine postmaster@- oder abuse@-Adresse sein")
	}
	return nil
}

// ---- reports ----

func reportFilter(r *http.Request) store.ReportFilter {
	days := queryInt(r, "days", 30, 400)
	if days < 1 {
		days = 30
	}
	return store.ReportFilter{Domain: store.Normalize(r.URL.Query().Get("domain")), Since: time.Now().AddDate(0, 0, -days)}
}

func (s *Server) dmarcReports(w http.ResponseWriter, r *http.Request, u *store.Account) (any, error) {
	ctx, f := r.Context(), reportFilter(r)
	summary, err := s.Store.DMARCSummary(ctx, f)
	if err != nil {
		return nil, err
	}
	sources, err := s.Store.DMARCSources(ctx, f, 500)
	if err != nil {
		return nil, err
	}
	list, err := s.Store.ListDMARCReports(ctx, f, 200)
	if err != nil {
		return nil, err
	}
	return map[string]any{"summary": summary, "sources": sources, "reports": list}, nil
}

func (s *Server) dmarcReport(w http.ResponseWriter, r *http.Request, u *store.Account) (any, error) {
	id, err := pathID(r, "id")
	if err != nil {
		return nil, err
	}
	return s.Store.GetDMARCReport(r.Context(), id)
}

func (s *Server) deleteDMARCReport(w http.ResponseWriter, r *http.Request, u *store.Account) (any, error) {
	id, err := pathID(r, "id")
	if err != nil {
		return nil, err
	}
	return ok(), s.Store.DeleteDMARCReport(r.Context(), id)
}

func (s *Server) tlsReports(w http.ResponseWriter, r *http.Request, u *store.Account) (any, error) {
	ctx, f := r.Context(), reportFilter(r)
	summary, err := s.Store.TLSSummary(ctx, f)
	if err != nil {
		return nil, err
	}
	failures, err := s.Store.TLSFailures(ctx, f)
	if err != nil {
		return nil, err
	}
	list, err := s.Store.ListTLSReports(ctx, f, 200)
	if err != nil {
		return nil, err
	}
	return map[string]any{"summary": summary, "failures": failures, "reports": list}, nil
}

func (s *Server) deleteTLSReport(w http.ResponseWriter, r *http.Request, u *store.Account) (any, error) {
	id, err := pathID(r, "id")
	if err != nil {
		return nil, err
	}
	return ok(), s.Store.DeleteTLSReport(r.Context(), id)
}
