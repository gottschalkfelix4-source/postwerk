package delivery

import (
	"context"
	"errors"
	"log/slog"
	"strings"

	"postwerk/internal/mailauth"
	"postwerk/internal/message"
	"postwerk/internal/reports"
	"postwerk/internal/store"
)

// signingKeys returns the DKIM keys used for outgoing mail of a domain.
func (s *Service) signingKeys(ctx context.Context, domain string) []mailauth.SigningKey {
	keys, err := s.Store.SigningKeys(ctx, domain)
	if err != nil {
		return nil
	}
	out := make([]mailauth.SigningKey, 0, len(keys))
	for _, k := range keys {
		out = append(out, mailauth.SigningKey{Selector: k.Selector, PrivateKey: k.PrivateKey})
	}
	return out
}

// applyARCOverride relaxes a failed DMARC policy when the message carries a
// valid ARC chain sealed by a trusted forwarder (local policy override,
// RFC 7489 6.7 / RFC 8617 7.2).
func (s *Service) applyARCOverride(res *mailauth.Result, trusted []string, log *slog.Logger) {
	action, sealer := res.ARCOverride(trusted)
	switch action {
	case "accept":
		res.DMARCOverride = "arc=pass, sealed by " + sealer
		res.DMARCPolicy = "none"
	case "quarantine":
		if res.DMARCPolicy != "reject" {
			return
		}
		res.DMARCOverride = "arc=pass, sealed by " + sealer + ", reject downgraded to quarantine"
		res.DMARCPolicy = "quarantine"
	default:
		return
	}
	log.Info("dmarc policy overridden by arc", "header_from", res.FromDomain, "sealer", sealer, "action", action)
}

// SealForward adds an ARC set signed with the domain's DKIM key to a message
// that is forwarded to an external target (alias forwards, redirects). The
// set records this hop's authentication results. On any problem the
// message is returned unchanged.
func (s *Service) SealForward(ctx context.Context, domain string, auth mailauth.Result, raw []byte) []byte {
	keys, err := s.Store.SigningKeys(ctx, domain)
	if err != nil || len(keys) == 0 {
		return raw
	}
	// SigningKeys lists RSA first; RFC 8617 only defines rsa-sha256.
	key := keys[0]
	cv := auth.ARC
	if cv != "pass" && cv != "fail" {
		cv = "none"
	}
	out, err := mailauth.SealARC(raw, mailauth.ARCSealOptions{
		Domain: domain, Selector: key.Selector, PrivateKey: key.PrivateKey,
		AuthServID: s.hostname(), AuthResults: auth.ResultsString(), ChainValidation: cv,
	})
	if err != nil {
		if !errors.Is(err, mailauth.ErrARCChainFailed) && !errors.Is(err, mailauth.ErrARCLimit) {
			s.Log.Warn("arc sealing failed", "domain", domain, "err", err)
		}
		return raw
	}
	return out
}

// reportLocalParts are recipient local parts that receive DMARC aggregate
// (rua) and TLS-RPT reports.
var reportLocalParts = map[string]bool{
	"postmaster": true, "dmarc": true, "dmarc-reports": true, "dmarc-rua": true, "rua": true,
	"tlsrpt": true, "tls-reports": true, "tls-rpt": true, "tlsrpt-reports": true,
}

// ownsReportDomain reports whether d is one of our domains or a subdomain of one.
func (s *Service) ownsReportDomain(ctx context.Context, d string) bool {
	d = strings.Trim(strings.ToLower(d), ".")
	for d != "" {
		if s.isLocalDomain(ctx, d) {
			return true
		}
		_, rest, ok := strings.Cut(d, ".")
		if !ok {
			break
		}
		d = rest
	}
	return false
}

// isReportAddress reports whether rcpt is one of the report addresses of a
// local domain.
func (s *Service) isReportAddress(ctx context.Context, rcpt string) bool {
	local, dom, ok := strings.Cut(store.Normalize(rcpt), "@")
	return ok && reportLocalParts[local] && s.isLocalDomain(ctx, dom)
}

// ingestReports parses DMARC aggregate and TLS reports from a message sent
// to one of the report addresses (or clearly marked as a report) and stores
// those about our domains. It returns true when the message was fully
// consumed: at least one report was found, every report was about one of
// our domains and has been stored (or was already known), and nothing failed
// to parse. Only then may the delivery to the report addresses be skipped.
func (s *Service) ingestReports(ctx context.Context, log *slog.Logger, rcpts []string, parsed *message.Parsed, data []byte) bool {
	addressed := false
	for _, r := range rcpts {
		if s.isReportAddress(ctx, r) {
			addressed = true
			break
		}
	}
	if !addressed {
		if parsed == nil || !reports.LooksLikeReport(parsed.Subject, parsed.Header.Get("TLS-Report-Domain")) {
			return false
		}
	}
	res := reports.Extract(data)
	complete := len(res.DMARC)+len(res.TLS) > 0 && len(res.Errors) == 0
	for _, r := range res.DMARC {
		if !s.ownsReportDomain(ctx, r.Domain) {
			log.Info("dmarc report for foreign domain ignored", "domain", r.Domain, "org", r.OrgName)
			complete = false
			continue
		}
		stored, err := s.Store.SaveDMARCReport(ctx, r)
		if err != nil {
			log.Error("storing dmarc report failed", "domain", r.Domain, "err", err)
			complete = false
			continue
		}
		log.Info("dmarc report received", "domain", r.Domain, "org", r.OrgName, "report_id", r.ReportID, "messages", r.Messages, "failed", r.Failed, "new", stored)
	}
	for _, r := range res.TLS {
		if !s.ownsReportDomain(ctx, r.Domain) {
			log.Info("tls report for foreign domain ignored", "domain", r.Domain, "org", r.OrgName)
			complete = false
			continue
		}
		stored, err := s.Store.SaveTLSReport(ctx, r)
		if err != nil {
			log.Error("storing tls report failed", "domain", r.Domain, "err", err)
			complete = false
			continue
		}
		log.Info("tls report received", "domain", r.Domain, "org", r.OrgName, "report_id", r.ReportID, "failures", r.FailureCount, "new", stored)
	}
	if addressed || len(res.DMARC)+len(res.TLS) > 0 {
		for _, e := range res.Errors {
			log.Warn("report could not be parsed", "err", e)
		}
	}
	return complete
}
