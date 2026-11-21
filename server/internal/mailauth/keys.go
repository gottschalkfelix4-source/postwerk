package mailauth

import (
	"bytes"
	"context"
	"crypto"
	"crypto/ed25519"
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

	"github.com/emersion/go-msgauth/dkim"
)

// DKIM key algorithms.
const (
	AlgRSA     = "rsa"
	AlgEd25519 = "ed25519"
)

// lookupTXT resolves TXT records (hook for tests).
var lookupTXT = func(ctx context.Context, name string) ([]string, error) {
	return net.DefaultResolver.LookupTXT(ctx, name)
}

// GenerateKey creates a new RSA-2048 DKIM key (PEM, PKCS#1).
func GenerateKey() (string, error) { return GenerateKeyFor(AlgRSA) }

// GenerateKeyFor creates a new DKIM private key for the given algorithm:
// RSA-2048 as PKCS#1 PEM or Ed25519 (RFC 8463) as PKCS#8 PEM.
func GenerateKeyFor(alg string) (string, error) {
	switch alg {
	case AlgRSA:
		k, err := rsa.GenerateKey(rand.Reader, 2048)
		if err != nil {
			return "", err
		}
		der := x509.MarshalPKCS1PrivateKey(k)
		return string(pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: der})), nil
	case AlgEd25519:
		_, k, err := ed25519.GenerateKey(rand.Reader)
		if err != nil {
			return "", err
		}
		der, err := x509.MarshalPKCS8PrivateKey(k)
		if err != nil {
			return "", err
		}
		return string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der})), nil
	}
	return "", fmt.Errorf("unknown dkim algorithm %q", alg)
}

// parseSigner decodes a PEM private key (PKCS#1 RSA or PKCS#8 RSA/Ed25519).
func parseSigner(p string) (crypto.Signer, string, error) {
	blk, _ := pem.Decode([]byte(p))
	if blk == nil {
		return nil, "", errors.New("invalid PEM")
	}
	if blk.Type == "RSA PRIVATE KEY" {
		k, err := x509.ParsePKCS1PrivateKey(blk.Bytes)
		return k, AlgRSA, err
	}
	k, err := x509.ParsePKCS8PrivateKey(blk.Bytes)
	if err != nil {
		return nil, "", err
	}
	switch k := k.(type) {
	case *rsa.PrivateKey:
		return k, AlgRSA, nil
	case ed25519.PrivateKey:
		return k, AlgEd25519, nil
	}
	return nil, "", errors.New("unsupported private key type")
}

// KeyAlgorithm reports the algorithm (rsa or ed25519) of a PEM private key.
func KeyAlgorithm(privPEM string) (string, error) {
	_, alg, err := parseSigner(privPEM)
	return alg, err
}

// publicKeyData returns the k= and p= values of the DKIM key record.
func publicKeyData(privPEM string) (string, string, error) {
	signer, alg, err := parseSigner(privPEM)
	if err != nil {
		return "", "", err
	}
	if alg == AlgEd25519 {
		// RFC 8463: p= carries the raw 32-byte public key.
		return alg, base64.StdEncoding.EncodeToString(signer.Public().(ed25519.PublicKey)), nil
	}
	der, err := x509.MarshalPKIXPublicKey(signer.Public())
	if err != nil {
		return "", "", err
	}
	return alg, base64.StdEncoding.EncodeToString(der), nil
}

// DNSValue returns the TXT value for <selector>._domainkey.<domain>.
func DNSValue(privPEM string) (string, error) {
	alg, p, err := publicKeyData(privPEM)
	if err != nil {
		return "", err
	}
	return "v=DKIM1; k=" + alg + "; p=" + p, nil
}

// SignedHeaders lists the header fields covered by our DKIM signatures.
// From, Subject, To, Date and Message-ID appear twice (oversigning): the
// second, non-existent instance makes the signature break if anyone adds
// another copy of these fields later.
var SignedHeaders = []string{
	"From", "From", "Subject", "Subject", "To", "To", "Date", "Date", "Message-ID", "Message-ID",
	"Cc", "Reply-To", "In-Reply-To", "References", "MIME-Version", "Content-Type", "Content-Transfer-Encoding",
}

// SigningKey is one DKIM key used for signing.
type SigningKey struct {
	Selector   string
	PrivateKey string
}

// Sign prepends a DKIM-Signature header to raw.
func Sign(raw []byte, domain, selector, privPEM string) ([]byte, error) {
	return SignAll(raw, domain, []SigningKey{{Selector: selector, PrivateKey: privPEM}})
}

// SignAll adds one DKIM-Signature per key (e.g. RSA and Ed25519 for dual
// signing, RFC 8463). Keys that cannot be used are skipped; an error is only
// returned when no signature could be added at all.
func SignAll(raw []byte, domain string, keys []SigningKey) ([]byte, error) {
	out := raw
	var firstErr error
	signed := 0
	for _, k := range keys {
		signer, _, err := parseSigner(k.PrivateKey)
		if err != nil {
			if firstErr == nil {
				firstErr = fmt.Errorf("selector %s: %w", k.Selector, err)
			}
			continue
		}
		opts := &dkim.SignOptions{
			Domain:                 domain,
			Selector:               k.Selector,
			Signer:                 signer,
			HeaderCanonicalization: dkim.CanonicalizationRelaxed,
			BodyCanonicalization:   dkim.CanonicalizationRelaxed,
			HeaderKeys:             SignedHeaders,
		}
		var buf bytes.Buffer
		if err := dkim.Sign(&buf, bytes.NewReader(out), opts); err != nil {
			if firstErr == nil {
				firstErr = fmt.Errorf("selector %s: %w", k.Selector, err)
			}
			continue
		}
		out = buf.Bytes()
		signed++
	}
	if signed == 0 {
		if firstErr == nil {
			firstErr = errors.New("no dkim key")
		}
		return nil, firstErr
	}
	return out, nil
}

// Publication is the result of checking a DKIM key record in DNS.
type Publication struct {
	Name      string   `json:"name"`
	Published bool     `json:"published"`
	Found     []string `json:"found"`
	Error     string   `json:"error,omitempty"`
}

// CheckPublished looks up <selector>._domainkey.<domain> and reports whether
// the public key of privPEM is published there.
func CheckPublished(ctx context.Context, domain, selector, privPEM string) Publication {
	name := selector + "._domainkey." + strings.TrimSuffix(domain, ".")
	pub := Publication{Name: name, Found: []string{}}
	_, want, err := publicKeyData(privPEM)
	if err != nil {
		pub.Error = "Schlüssel ungültig: " + err.Error()
		return pub
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	txts, err := lookupTXT(ctx, name)
	if err != nil {
		var dnsErr *net.DNSError
		if errors.As(err, &dnsErr) && dnsErr.IsNotFound {
			pub.Error = "Kein TXT-Eintrag unter " + name + " gefunden"
		} else {
			pub.Error = "DNS-Abfrage fehlgeschlagen: " + err.Error()
		}
		return pub
	}
	for _, t := range txts {
		pub.Found = append(pub.Found, t)
		tags, err := parseTags(t)
		if err != nil {
			continue
		}
		if stripWSP(tags["p"]) == want {
			pub.Published = true
		}
	}
	if !pub.Published && pub.Error == "" {
		pub.Error = "Der veröffentlichte Eintrag enthält einen anderen Schlüssel"
		if len(txts) == 0 {
			pub.Error = "Kein TXT-Eintrag unter " + name + " gefunden"
		}
	}
	return pub
}
