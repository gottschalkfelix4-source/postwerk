package api

import (
	"context"
	"errors"
	"net"
	"net/http"
	"strings"
	"time"

	"postwerk/internal/dnsprov"
	"postwerk/internal/mailauth"
	"postwerk/internal/store"
)

func (s *Server) dnsProviders(w http.ResponseWriter, r *http.Request, u *store.Account) (any, error) {
	return dnsprov.Providers(), nil
}

type dnsPlanReq struct {
	Provider    string            `json:"provider"`
	Credentials map[string]string `json:"credentials"`
	IPv4        *string           `json:"ipv4"`
	IPv6        *string           `json:"ipv6"`
	Keys        []string          `json:"keys"`
}

func dnsErr(err error) error {
	if dnsprov.IsProviderError(err) {
		return errf(502, "dns_provider_error", err.Error())
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return errf(502, "dns_provider_error", "Der DNS-Anbieter antwortet nicht (Zeitüberschreitung)")
	}
	var ne net.Error
	if errors.As(err, &ne) {
		return errf(502, "dns_provider_error", "DNS-Anbieter nicht erreichbar: "+err.Error())
	}
	return err
}

// dnsPlan builds provider and plan for a domain.
func (s *Server) dnsPlan(r *http.Request, ctx context.Context) (dnsprov.Provider, *dnsprov.Plan, *dnsPlanReq, error) {
	var req dnsPlanReq
	if err := decode(r, &req); err != nil {
		return nil, nil, nil, err
	}
	d, err := s.Store.GetDomain(ctx, r.PathValue("name"))
	if err != nil {
		return nil, nil, nil, err
	}
	ipv4, ipv6 := "", ""
	if req.IPv4 != nil {
		ipv4 = strings.TrimSpace(*req.IPv4)
		if ip := net.ParseIP(ipv4); ipv4 != "" && (ip == nil || ip.To4() == nil) {
			return nil, nil, nil, invalid("Ungültige IPv4-Adresse")
		}
	}
	if req.IPv6 != nil {
		ipv6 = strings.TrimSpace(*req.IPv6)
		if ip := net.ParseIP(ipv6); ipv6 != "" && (ip == nil || ip.To4() != nil) {
			return nil, nil, nil, invalid("Ungültige IPv6-Adresse")
		}
	}
	p, info, err := dnsprov.New(req.Provider, req.Credentials)
	if err != nil {
		return nil, nil, nil, invalid(err.Error())
	}
	dkim, err := mailauth.DNSValue(d.DKIMPrivateKey)
	if err != nil {
		return nil, nil, nil, err
	}
	cfg := s.Settings.Get()
	desired := dnsprov.DesiredRecords(d.Name, cfg.Server.Hostname, d.DKIMSelector, dkim, ipv4, ipv6, s.dnsOptions(cfg))
	plan, err := dnsprov.MakePlan(ctx, p, info, d.Name, desired)
	if err != nil {
		return nil, nil, nil, dnsErr(err)
	}
	return p, plan, &req, nil
}

func (s *Server) domainDNSPlan(w http.ResponseWriter, r *http.Request, u *store.Account) (any, error) {
	ctx, cancel := context.WithTimeout(r.Context(), 90*time.Second)
	defer cancel()
	_, plan, _, err := s.dnsPlan(r, ctx)
	if err != nil {
		return nil, err
	}
	return plan, nil
}

func (s *Server) domainDNSApply(w http.ResponseWriter, r *http.Request, u *store.Account) (any, error) {
	ctx, cancel := context.WithTimeout(r.Context(), 3*time.Minute)
	defer cancel()
	p, plan, req, err := s.dnsPlan(r, ctx)
	if err != nil {
		return nil, err
	}
	res := dnsprov.ApplyPlan(ctx, p, plan, req.Keys)
	s.Log.Info("dns records applied at provider", "component", "dns", "domain", r.PathValue("name"), "provider", req.Provider,
		"applied", strings.Join(res.Applied, ","), "failed", len(res.Failed), "by", u.Email)
	for _, f := range res.Failed {
		s.Log.Warn("dns record failed", "component", "dns", "domain", r.PathValue("name"), "key", f.Key, "err", f.Error)
	}
	return res, nil
}

// publicIP asks api.ipify.org for the server's public addresses (only on request).
func (s *Server) publicIP(w http.ResponseWriter, r *http.Request, u *store.Account) (any, error) {
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	get := func(u string) *string {
		req, _ := http.NewRequestWithContext(ctx, "GET", u, nil)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			return nil
		}
		defer resp.Body.Close()
		buf := make([]byte, 64)
		n, _ := resp.Body.Read(buf)
		ip := strings.TrimSpace(string(buf[:n]))
		if net.ParseIP(ip) == nil {
			return nil
		}
		return &ip
	}
	return map[string]any{"ipv4": get("https://api.ipify.org"), "ipv6": get("https://api6.ipify.org")}, nil
}

// dnsOptions derives SPF include and DynDNS handling from the settings.
func (s *Server) dnsOptions(cfg store.Settings) dnsprov.Options {
	var o dnsprov.Options
	if s.Cfg.RelayHost == "" && cfg.Relay.Enabled {
		o.SPFInclude = cfg.Relay.SPFInclude
	}
	if cfg.DynDNS.Enabled {
		for _, h := range cfg.DynDNS.Hostnames {
			if strings.EqualFold(h, cfg.Server.Hostname) {
				o.DynDNS = true
			}
		}
	}
	return o
}
