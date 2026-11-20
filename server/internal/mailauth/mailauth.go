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
	DMARC       string // pass, fail, none
	DMARCPolicy string // none, quarantine, reject
	FromDomain  string
}

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
	rec, err := dmarc.Lookup(fromDomain)
	if errors.Is(err, dmarc.ErrNoPolicy) {
		if org := orgDomain(fromDomain); org != fromDomain {
			rec, err = dmarc.Lookup(org)
		}
	}
	if err != nil || rec == nil {
		return r
	}
	r.DMARCPolicy = string(rec.Policy)
	spfAligned := r.SPF == "pass" && aligned(r.SPFDomain, fromDomain, rec.SPFAlignment)
	dkimAligned := false
	for _, d := range r.DKIMDomains {
		if aligned(d, fromDomain, rec.DKIMAlignment) {
			dkimAligned = true
			break
		}
	}
	if spfAligned || dkimAligned {
		r.DMARC = "pass"
	} else {
		r.DMARC = "fail"
	}
	return r
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
		HeaderKeys:             []string{"From", "To", "Cc", "Subject", "Date", "Message-ID", "In-Reply-To", "References", "MIME-Version", "Content-Type", "Content-Transfer-Encoding"},
	}
	var out bytes.Buffer
	if err := dkim.Sign(&out, bytes.NewReader(raw), opts); err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}
