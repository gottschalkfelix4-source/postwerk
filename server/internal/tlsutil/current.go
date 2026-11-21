package tlsutil

import (
	"context"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
)

// CertState describes the certificate served for the mail hostname.
type CertState struct {
	Leaf *x509.Certificate
	// SelfSigned is true when the self-signed fallback is active.
	SelfSigned bool
	// Problem explains why the configured certificate is not in use (empty if fine).
	Problem string
}

// Current returns the certificate that is (or will be) served for the mail
// hostname without side effects: in ACME mode it only reads the cache and
// never starts an order.
func (d *Dynamic) Current(ctx context.Context) (*CertState, error) {
	mode, s := d.mode()
	switch mode {
	case "acme":
		m := d.manager(s.ACME)
		if m == nil || m.Cache == nil {
			break
		}
		data, err := m.Cache.Get(ctx, d.Hostname())
		if err != nil {
			return d.selfState("Noch kein Let's-Encrypt-Zertifikat vorhanden")
		}
		if l := firstCert(data); l != nil {
			return &CertState{Leaf: l}, nil
		}
		return d.selfState("Let's-Encrypt-Zertifikat im Cache ist unlesbar")
	case "files":
		c, err := d.fileCert(s.Files.CertPath, s.Files.KeyPath)
		if err != nil {
			return d.selfState(fmt.Sprintf("Zertifikatsdatei nicht nutzbar: %v", err))
		}
		if l := leaf(c); l != nil {
			return &CertState{Leaf: l}, nil
		}
		return d.selfState("Zertifikatsdatei enthält kein Zertifikat")
	}
	return d.selfState("")
}

func (d *Dynamic) selfState(problem string) (*CertState, error) {
	c, err := d.selfSigned()
	if err != nil {
		return nil, err
	}
	l := leaf(c)
	if l == nil {
		return nil, errors.New("selbstsigniertes Zertifikat unlesbar")
	}
	return &CertState{Leaf: l, SelfSigned: true, Problem: problem}, nil
}

// firstCert returns the first CERTIFICATE block of a PEM bundle.
func firstCert(data []byte) *x509.Certificate {
	for {
		var b *pem.Block
		b, data = pem.Decode(data)
		if b == nil {
			return nil
		}
		if b.Type == "CERTIFICATE" {
			c, err := x509.ParseCertificate(b.Bytes)
			if err != nil {
				return nil
			}
			return c
		}
	}
}
