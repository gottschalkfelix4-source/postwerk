// Package mailauth evaluates SPF, DKIM, DMARC and ARC for inbound mail and
// signs outbound mail with DKIM and ARC.
package mailauth

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	mrand "math/rand/v2"
	"net"
	"strings"
	"time"

	"blitiri.com.ar/go/spf"
	"github.com/emersion/go-msgauth/dkim"
	"github.com/emersion/go-msgauth/dmarc"
	"golang.org/x/net/publicsuffix"
)

type Result struct {
	SPF         string // pass, fail, softfail, neutral, none, temperror, permerror
	SPFDomain   string
	DKIM        string // pass, fail, none
	DKIMDomains []string
	DMARC       string // pass, fail, none, temperror
	// DMARCPolicy is the policy to apply on failure after sp= (subdomains)
	// and pct= have been taken into account: none, quarantine, reject.
	DMARCPolicy string
	FromDomain  string
	// ARC is the result of the ARC chain validation (none, pass, fail);
	// empty when no validation took place.
	ARC string
	// ARCChain holds the details of the validated ARC sets.
	ARCChain ARCResult
	// DMARCOverride names the reason a failed DMARC policy was relaxed
	// locally, e.g. "arc=pass, sealed by google.com".
	DMARCOverride string
}

// Hooks for tests.
var (
	lookupDMARC = dmarc.Lookup
	randPercent = func() int { return mrand.IntN(100) }
)

// Check runs SPF, DKIM and DMARC for an inbound message.
func Check(ctx context.Context, ip net.IP, helo, mailFrom, fromDomain string, raw []byte) Result {
	r := Result{FromDomain: fromDomain, DMARC: "none"}
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()

	// SPF on MAIL FROM, falling back to HELO for null sender.
	sender := mailFrom
	if sender == "" {
		sender = "postmaster@" + helo
	}
	r.SPFDomain = domainOf(sender)
	res, _ := spf.CheckHostWithSender(ip, helo, sender, spf.WithContext(ctx))
	r.SPF = string(res)
	if r.SPF == "" {
		r.SPF = "none"
	}

	// DKIM
	verifs, err := dkim.VerifyWithOptions(bytes.NewReader(raw), &dkim.VerifyOptions{
		MaxVerifications: 5,
		LookupTXT:        func(name string) ([]string, error) { return lookupTXT(ctx, name) },
	})
	r.DKIM = "none"
	if err == nil {
		for _, v := range verifs {
			if v.Err == nil {
				r.DKIM = "pass"
				r.DKIMDomains = append(r.DKIMDomains, strings.ToLower(v.Domain))
			} else if r.DKIM != "pass" {
				r.DKIM = "fail"
			}
		}
	}

	// ARC
	r.ARCChain = VerifyARC(ctx, raw)
	r.ARC = r.ARCChain.Result

	// DMARC
	if fromDomain == "" {
		return r
	}
	rec, err := lookupDMARC(fromDomain)
	viaOrg := false
	if errors.Is(err, dmarc.ErrNoPolicy) {
		if org := orgDomain(fromDomain); org != fromDomain {
			rec, err = lookupDMARC(org)
			viaOrg = true
		}
	}
	if err != nil || rec == nil {
		if err != nil && dmarc.IsTempFail(err) {
			r.DMARC = "temperror"
		}
		return r
	}
	evaluateDMARC(&r, rec, viaOrg)
	return r
}

// evaluateDMARC sets DMARC and DMARCPolicy from a policy record. viaOrg is
// true when the record was found at the organizational domain, in which case
// sp= applies to the (sub)domain of the From header.
func evaluateDMARC(r *Result, rec *dmarc.Record, viaOrg bool) {
	policy := rec.Policy
	if viaOrg && rec.SubdomainPolicy != "" {
		policy = rec.SubdomainPolicy
	}
	if policy == "" {
		policy = dmarc.PolicyNone
	}
	// pct=: messages outside the sampled percentage get the next less strict
	// policy (RFC 7489 section 6.6.4).
	if rec.Percent != nil && *rec.Percent < 100 && randPercent() >= *rec.Percent {
		switch policy {
		case dmarc.PolicyReject:
			policy = dmarc.PolicyQuarantine
		case dmarc.PolicyQuarantine:
			policy = dmarc.PolicyNone
		}
	}
	r.DMARCPolicy = string(policy)
	spfAligned := r.SPF == "pass" && aligned(r.SPFDomain, r.FromDomain, rec.SPFAlignment)
	dkimAligned := false
	for _, d := range r.DKIMDomains {
		if aligned(d, r.FromDomain, rec.DKIMAlignment) {
			dkimAligned = true
			break
		}
	}
	if spfAligned || dkimAligned {
		r.DMARC = "pass"
	} else {
		r.DMARC = "fail"
	}
}

// within reports whether d equals domain or is a subdomain of it.
func within(d, domain string) bool {
	d, domain = strings.ToLower(strings.TrimSuffix(d, ".")), strings.ToLower(strings.TrimSuffix(domain, "."))
	return d != "" && domain != "" && (d == domain || strings.HasSuffix(d, "."+domain))
}

// AuthenticatedFor reports whether the message is authenticated for domain:
// SPF passed for a MAIL FROM within domain, or a valid DKIM signature of
// domain (or one of its subdomains) is present. Unlike relaxed DMARC
// alignment this does not rely on the public suffix list, so a sibling
// domain under a shared parent cannot vouch for a hosted domain.
func (r Result) AuthenticatedFor(domain string) bool {
	if r.SPF == "pass" && within(r.SPFDomain, domain) {
		return true
	}
	for _, d := range r.DKIMDomains {
		if within(d, domain) {
			return true
		}
	}
	return false
}

func aligned(d, from string, mode dmarc.AlignmentMode) bool {
	d, from = strings.ToLower(d), strings.ToLower(from)
	if mode == dmarc.AlignmentStrict {
		return d == from
	}
	return orgDomain(d) == orgDomain(from)
}

func orgDomain(d string) string {
	if o, err := publicsuffix.EffectiveTLDPlusOne(d); err == nil {
		return o
	}
	return d
}

func domainOf(addr string) string {
	if i := strings.LastIndexByte(addr, '@'); i >= 0 {
		return strings.ToLower(addr[i+1:])
	}
	return strings.ToLower(addr)
}

// Header renders an Authentication-Results header line (without CRLF).
func (r Result) Header(authserv string) string {
	return "Authentication-Results: " + authserv + "; " + r.ResultsString()
}

// ResultsString renders the method results of an Authentication-Results
// header (everything after the authserv-id), as used in our
// Authentication-Results and ARC-Authentication-Results headers.
func (r Result) ResultsString() string {
	dmarcRes := r.DMARC
	if dmarcRes == "" {
		dmarcRes = "none"
	}
	if r.DMARCOverride != "" {
		dmarcRes += " (policy overridden: " + r.DMARCOverride + ")"
	}
	s := fmt.Sprintf("spf=%s smtp.mailfrom=%s; dkim=%s; dmarc=%s header.from=%s",
		r.SPF, headerToken(r.SPFDomain), r.DKIM, dmarcRes, headerToken(r.FromDomain))
	if r.ARC != "" {
		s += "; arc=" + r.ARC
		if r.ARC == "fail" && r.ARCChain.Reason != "" {
			s += " (" + sanitizeComment(r.ARCChain.Reason) + ")"
		}
	}
	return s
}

// headerToken returns a domain for use as an Authentication-Results
// property value. Anything outside host-name characters (an injected
// "; dmarc=pass", spaces, line breaks) is replaced, so attacker-chosen
// envelope or header domains cannot add results of their own.
func headerToken(d string) string {
	if d == "" {
		return "none"
	}
	for i := 0; i < len(d); i++ {
		c := d[i]
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-' || c == '.' || c == '_' || c >= 0x80) {
			return "invalid"
		}
	}
	return d
}

// sanitizeComment keeps a string safe for use inside a header comment.
func sanitizeComment(s string) string {
	s = strings.Map(func(r rune) rune {
		switch {
		case r == '(' || r == ')' || r == '\\':
			return -1
		case r < 0x20 || r == 0x7f:
			return ' '
		}
		return r
	}, s)
	if len(s) > 120 {
		s = s[:120]
	}
	return s
}
