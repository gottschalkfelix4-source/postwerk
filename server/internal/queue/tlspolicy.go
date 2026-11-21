package queue

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"log/slog"

	"postwerk/internal/metrics"
)

// hostTLS is the TLS requirement for one MX host.
type hostTLS struct {
	label    string // dane | mta-sts | mta-sts-testing | opportunistic
	required bool   // no plaintext fallback
	config   *tls.Config
}

// domainTLS holds what is known about a recipient domain before its MX hosts
// are tried.
type domainTLS struct {
	domain   string
	sts      *STSPolicy
	mxSecure bool // MX RRset is DNSSEC-validated (DANE applies)
}

func opportunistic() *hostTLS {
	return &hostTLS{label: "opportunistic", config: &tls.Config{InsecureSkipVerify: true, MinVersion: tls.VersionTLS12}}
}

func (q *Queue) domainPolicy(ctx context.Context, domain string) domainTLS {
	p := domainTLS{domain: domain}
	if q.DANE != nil {
		p.mxSecure = q.DANE.MXSecure(ctx, domain)
	}
	if q.STS != nil {
		p.sts = q.STS.Policy(ctx, domain)
	}
	return p
}

// hostPolicy decides how to talk TLS to one MX host. DANE wins over MTA-STS;
// without either, TLS is opportunistic. An error means the host must not be
// used for this attempt.
func (q *Queue) hostPolicy(ctx context.Context, p domainTLS, host string) (*hostTLS, error) {
	if q.DANE != nil && p.mxSecure {
		recs, secure, err := q.DANE.TLSA(ctx, host)
		if err != nil {
			return nil, fmt.Errorf("DANE: %v", err)
		}
		if secure && len(recs) > 0 {
			return daneTLS(host, recs), nil
		}
	}
	if p.sts != nil {
		switch p.sts.Mode {
		case "enforce":
			if !p.sts.Matches(host) {
				return nil, fmt.Errorf("MTA-STS: MX %s ist in der Policy von %s nicht erlaubt", host, p.domain)
			}
			return &hostTLS{label: "mta-sts", required: true,
				config: &tls.Config{ServerName: host, MinVersion: tls.VersionTLS12}}, nil
		case "testing":
			return stsTestingTLS(host, p, q.Log), nil
		}
	}
	return opportunistic(), nil
}

// daneTLS requires TLS and checks the certificate against the TLSA records.
// If none of them is usable for SMTP, TLS is still mandatory but not
// authenticated (RFC 7672 2.2).
func daneTLS(host string, recs []tlsaRecord) *hostTLS {
	var usable []tlsaRecord
	for _, r := range recs {
		if r.usable() {
			usable = append(usable, r)
		}
	}
	cfg := &tls.Config{ServerName: host, InsecureSkipVerify: true, MinVersion: tls.VersionTLS12}
	if len(usable) > 0 {
		cfg.VerifyConnection = func(cs tls.ConnectionState) error { return verifyDANE(cs, host, usable) }
	}
	return &hostTLS{label: "dane", required: true, config: cfg}
}

// stsTestingTLS delivers like opportunistic TLS but reports what enforce
// mode would have rejected.
func stsTestingTLS(host string, p domainTLS, log *slog.Logger) *hostTLS {
	report := func(reason string) {
		metrics.OutboundTLS.WithLabelValues("mta-sts-testing", "policy-violation").Inc()
		if log != nil {
			log.Warn("mta-sts testing: policy would fail in enforce mode", "domain", p.domain, "mx", host, "reason", reason)
		}
	}
	if !p.sts.Matches(host) {
		report("MX nicht in der Policy")
	}
	cfg := &tls.Config{ServerName: host, InsecureSkipVerify: true, MinVersion: tls.VersionTLS12,
		VerifyConnection: func(cs tls.ConnectionState) error {
			if err := verifyPKIX(cs, host); err != nil {
				report(err.Error())
			}
			return nil
		}}
	return &hostTLS{label: "mta-sts-testing", config: cfg}
}

// verifyPKIX performs the standard WebPKI check for host.
func verifyPKIX(cs tls.ConnectionState, host string) error {
	if len(cs.PeerCertificates) == 0 {
		return fmt.Errorf("kein Zertifikat")
	}
	inter := x509.NewCertPool()
	for _, c := range cs.PeerCertificates[1:] {
		inter.AddCert(c)
	}
	_, err := cs.PeerCertificates[0].Verify(x509.VerifyOptions{DNSName: host, Intermediates: inter})
	return err
}
