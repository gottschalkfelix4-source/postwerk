package queue

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/sha512"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"strings"
	"time"

	"github.com/miekg/dns"
)

// DANE for SMTP (RFC 7672): TLSA records under _25._tcp.<mx> pin the MX
// certificate. They are only trusted when a DNSSEC-validating resolver
// confirms them (AD flag).

type tlsaRecord struct {
	Usage, Selector, Matching uint8
	Data                      []byte
}

const (
	tlsaUsageDANETA = 2
	tlsaUsageDANEEE = 3
)

// usable reports whether the record is supported for SMTP (RFC 7672 3.1).
func (r tlsaRecord) usable() bool {
	return (r.Usage == tlsaUsageDANETA || r.Usage == tlsaUsageDANEEE) &&
		r.Selector <= 1 && r.Matching <= 2 && len(r.Data) > 0
}

func (r tlsaRecord) matches(c *x509.Certificate) bool {
	var data []byte
	switch r.Selector {
	case 0:
		data = c.Raw
	case 1:
		data = c.RawSubjectPublicKeyInfo
	default:
		return false
	}
	switch r.Matching {
	case 0:
		return bytes.Equal(data, r.Data)
	case 1:
		sum := sha256.Sum256(data)
		return bytes.Equal(sum[:], r.Data)
	case 2:
		sum := sha512.Sum512(data)
		return bytes.Equal(sum[:], r.Data)
	}
	return false
}

// verifyDANE checks the presented chain against the usable TLSA records.
// DANE-EE (3) matches the leaf without name or expiry checks; DANE-TA (2)
// matches a certificate of the chain that must then issue a leaf valid for
// host.
func verifyDANE(cs tls.ConnectionState, host string, recs []tlsaRecord) error {
	if len(cs.PeerCertificates) == 0 {
		return errors.New("DANE: Server hat kein Zertifikat gesendet")
	}
	leaf := cs.PeerCertificates[0]
	for _, r := range recs {
		if !r.usable() {
			continue
		}
		switch r.Usage {
		case tlsaUsageDANEEE:
			if r.matches(leaf) {
				return nil
			}
		case tlsaUsageDANETA:
			for i, c := range cs.PeerCertificates {
				if i == 0 && len(cs.PeerCertificates) > 1 {
					continue // the anchor must be an issuer, not the leaf itself
				}
				if !r.matches(c) {
					continue
				}
				roots := x509.NewCertPool()
				roots.AddCert(c)
				inter := x509.NewCertPool()
				for _, ic := range cs.PeerCertificates[1:] {
					inter.AddCert(ic)
				}
				if _, err := leaf.Verify(x509.VerifyOptions{DNSName: host, Roots: roots, Intermediates: inter,
					KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageAny}}); err == nil {
					return nil
				}
			}
		}
	}
	return fmt.Errorf("DANE: Zertifikat von %s passt zu keinem TLSA-Eintrag", host)
}

// DANE queries a DNSSEC-validating resolver.
type DANE struct {
	Servers []string // host:port
	Timeout time.Duration
	// Exchange sends one query; tests replace it.
	Exchange func(ctx context.Context, m *dns.Msg, server string) (*dns.Msg, error)
}

// NewSystemDANE uses the resolvers from /etc/resolv.conf. It returns nil
// where no such file exists (DANE is then skipped).
func NewSystemDANE() *DANE {
	cfg, err := dns.ClientConfigFromFile("/etc/resolv.conf")
	if err != nil || len(cfg.Servers) == 0 {
		return nil
	}
	d := &DANE{Timeout: 5 * time.Second}
	for _, s := range cfg.Servers {
		d.Servers = append(d.Servers, net.JoinHostPort(s, cfg.Port))
	}
	return d
}

func (d *DANE) exchange(ctx context.Context, m *dns.Msg, server string) (*dns.Msg, error) {
	if d.Exchange != nil {
		return d.Exchange(ctx, m, server)
	}
	timeout := d.Timeout
	if timeout == 0 {
		timeout = 5 * time.Second
	}
	c := &dns.Client{Net: "udp", Timeout: timeout}
	r, _, err := c.ExchangeContext(ctx, m, server)
	if err == nil && r.Truncated {
		c.Net = "tcp"
		r, _, err = c.ExchangeContext(ctx, m, server)
	}
	return r, err
}

// query asks with the DO bit set so the resolver validates and reports AD.
func (d *DANE) query(ctx context.Context, name string, qtype uint16) (*dns.Msg, error) {
	m := new(dns.Msg)
	m.SetQuestion(dns.Fqdn(name), qtype)
	m.RecursionDesired = true
	m.AuthenticatedData = true
	m.SetEdns0(4096, true)
	var lastErr error
	for _, s := range d.Servers {
		r, err := d.exchange(ctx, m, s)
		if err != nil {
			lastErr = err
			continue
		}
		return r, nil
	}
	if lastErr == nil {
		lastErr = errors.New("kein DNS-Server konfiguriert")
	}
	return nil, lastErr
}

// MXSecure reports whether the MX RRset of domain is DNSSEC-signed and
// validated. Only then are TLSA records of its MX hosts considered.
func (d *DANE) MXSecure(ctx context.Context, domain string) bool {
	r, err := d.query(ctx, domain, dns.TypeMX)
	return err == nil && r.Rcode == dns.RcodeSuccess && r.AuthenticatedData
}

// TLSA returns the validated TLSA records of an MX host. secure is false when
// the answer is not DNSSEC-validated (records are then ignored). An error
// means the lookup failed in a way that must not be treated as "no records".
func (d *DANE) TLSA(ctx context.Context, host string) (recs []tlsaRecord, secure bool, err error) {
	r, err := d.query(ctx, "_25._tcp."+strings.TrimSuffix(host, "."), dns.TypeTLSA)
	if err != nil {
		return nil, false, err
	}
	switch r.Rcode {
	case dns.RcodeSuccess, dns.RcodeNameError:
	default:
		// Callers only ask for hosts of a validated MX RRset; a failing TLSA
		// lookup there may hide bogus records (RFC 7672 2.2.1).
		return nil, false, fmt.Errorf("TLSA-Abfrage für %s: %s", host, dns.RcodeToString[r.Rcode])
	}
	if !r.AuthenticatedData {
		return nil, false, nil
	}
	for _, rr := range r.Answer {
		t, ok := rr.(*dns.TLSA)
		if !ok {
			continue
		}
		data, err := hex.DecodeString(t.Certificate)
		if err != nil {
			continue
		}
		recs = append(recs, tlsaRecord{Usage: t.Usage, Selector: t.Selector, Matching: t.MatchingType, Data: data})
	}
	return recs, true, nil
}
