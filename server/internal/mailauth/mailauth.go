// Package mailauth evaluates SPF, DKIM and DMARC for inbound mail and signs
// outbound mail with DKIM.
package mailauth

import (
	"bytes"
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
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
	verifs, err := dkim.VerifyWithOptions(bytes.NewReader(raw), &dkim.VerifyOptions{MaxVerifications: 5})
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
	return fmt.Sprintf("Authentication-Results: %s; spf=%s smtp.mailfrom=%s; dkim=%s; dmarc=%s header.from=%s",
		authserv, r.SPF, r.SPFDomain, r.DKIM, r.DMARC, r.FromDomain)
}

// ---- DKIM keys and signing ----

func GenerateKey() (string, error) {
	k, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		return "", err
	}
	der := x509.MarshalPKCS1PrivateKey(k)
	return string(pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: der})), nil
}

func parseKey(p string) (*rsa.PrivateKey, error) {
	blk, _ := pem.Decode([]byte(p))
	if blk == nil {
		return nil, errors.New("invalid PEM")
	}
	return x509.ParsePKCS1PrivateKey(blk.Bytes)
}

// DNSValue returns the TXT value for <selector>._domainkey.<domain>.
func DNSValue(privPEM string) (string, error) {
	k, err := parseKey(privPEM)
	if err != nil {
		return "", err
	}
	der, err := x509.MarshalPKIXPublicKey(&k.PublicKey)
	if err != nil {
		return "", err
	}
	return "v=DKIM1; k=rsa; p=" + base64.StdEncoding.EncodeToString(der), nil
}

// SignedHeaders lists the header fields covered by our DKIM signatures.
// From, Subject, To, Date and Message-ID appear twice (oversigning): the
// second, non-existent instance makes the signature break if anyone adds
// another copy of these fields later.
var SignedHeaders = []string{
	"From", "From", "Subject", "Subject", "To", "To", "Date", "Date", "Message-ID", "Message-ID",
	"Cc", "Reply-To", "In-Reply-To", "References", "MIME-Version", "Content-Type", "Content-Transfer-Encoding",
}

// Sign prepends a DKIM-Signature header to raw.
func Sign(raw []byte, domain, selector, privPEM string) ([]byte, error) {
	k, err := parseKey(privPEM)
	if err != nil {
		return nil, err
	}
	opts := &dkim.SignOptions{
		Domain:                 domain,
		Selector:               selector,
		Signer:                 crypto.Signer(k),
		HeaderCanonicalization: dkim.CanonicalizationRelaxed,
		BodyCanonicalization:   dkim.CanonicalizationRelaxed,
		HeaderKeys:             SignedHeaders,
	}
	var out bytes.Buffer
	if err := dkim.Sign(&out, bytes.NewReader(raw), opts); err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}
