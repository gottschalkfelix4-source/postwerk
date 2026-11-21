// Package atrest encrypts stored mail with a user's public key (OpenPGP or
// S/MIME), so only the user's own client can read it: PGP/MIME (RFC 3156)
// multipart/encrypted or S/MIME application/pkcs7-mime enveloped data. Only
// From, To, Subject (optional), Date and Message-ID stay readable on the
// outer message. The encrypted part is the complete original message, so
// decrypting yields it byte for byte.
package atrest

import (
	"bytes"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"fmt"
	"mime"
	"net/mail"
	"strings"
	"time"

	"github.com/ProtonMail/go-crypto/openpgp"
	"github.com/ProtonMail/go-crypto/openpgp/armor"
	"github.com/ProtonMail/go-crypto/openpgp/packet"
	"github.com/smallstep/pkcs7"
)

// Kinds of keys.
const (
	KindPGP   = "pgp"
	KindSMIME = "smime"
)

// MaxKeySize limits uploaded keys and certificates.
const MaxKeySize = 256 << 10

func init() {
	// AES-256-CBC with RSA key transport is what every S/MIME client reads.
	pkcs7.ContentEncryptionAlgorithm = pkcs7.EncryptionAlgorithmAES256CBC
}

// KeyInfo describes an uploaded public key or certificate.
type KeyInfo struct {
	Kind        string     `json:"kind"`
	Fingerprint string     `json:"fingerprint"`
	Description string     `json:"description"`
	Emails      []string   `json:"emails"`
	Algorithm   string     `json:"algorithm"`
	Expires     *time.Time `json:"expires"`
}

// ErrNoEncryptionKey is returned for keys that cannot encrypt.
var ErrNoEncryptionKey = errors.New("der Schlüssel enthält keinen gültigen Verschlüsselungsschlüssel")

// ParseKey validates a public key (kind pgp: ASCII-armored or binary
// OpenPGP key; kind smime: PEM or DER X.509 certificate with an RSA key).
func ParseKey(kind string, data []byte) (*KeyInfo, error) {
	if len(data) == 0 {
		return nil, errors.New("kein Schlüssel angegeben")
	}
	if len(data) > MaxKeySize {
		return nil, errors.New("der Schlüssel ist zu groß")
	}
	switch kind {
	case KindPGP:
		e, err := readEntity(data)
		if err != nil {
			return nil, err
		}
		return pgpInfo(e)
	case KindSMIME:
		cert, err := readCert(data)
		if err != nil {
			return nil, err
		}
		return certInfo(cert), nil
	}
	return nil, fmt.Errorf("unbekannte Schlüsselart %q", kind)
}

func readEntity(data []byte) (*openpgp.Entity, error) {
	var list openpgp.EntityList
	var err error
	if bytes.Contains(data, []byte("-----BEGIN PGP")) {
		if bytes.Contains(data, []byte("PRIVATE KEY BLOCK")) {
			return nil, errors.New("das ist ein privater Schlüssel – bitte nur den öffentlichen Schlüssel hochladen")
		}
		list, err = openpgp.ReadArmoredKeyRing(bytes.NewReader(data))
	} else {
		list, err = openpgp.ReadKeyRing(bytes.NewReader(data))
	}
	if err != nil {
		return nil, fmt.Errorf("OpenPGP-Schlüssel nicht lesbar: %v", err)
	}
	if len(list) != 1 {
		return nil, fmt.Errorf("genau ein öffentlicher Schlüssel erwartet, gefunden: %d", len(list))
	}
	e := list[0]
	if e.PrivateKey != nil {
		return nil, errors.New("das ist ein privater Schlüssel – bitte nur den öffentlichen Schlüssel hochladen")
	}
	if e.Revoked(time.Now()) {
		return nil, errors.New("der Schlüssel wurde widerrufen")
	}
	if _, ok := encryptionTime(e); !ok {
		return nil, ErrNoEncryptionKey
	}
	return e, nil
}

// encryptionTime returns the time at which e has a usable encryption key:
// now, or, for an expired key, just after its newest self-signature (mail
// keeps being encrypted to an expired key instead of being stored in
// plaintext; the user still holds the private key).
func encryptionTime(e *openpgp.Entity) (time.Time, bool) {
	now := time.Now()
	if _, ok := e.EncryptionKey(now); ok {
		return now, true
	}
	if e.Revoked(now) {
		return time.Time{}, false
	}
	t := e.PrimaryKey.CreationTime
	if sig, _ := e.PrimarySelfSignature(); sig != nil && sig.CreationTime.After(t) {
		t = sig.CreationTime
	}
	for _, sk := range e.Subkeys {
		if sk.Sig != nil && sk.Sig.CreationTime.After(t) {
			t = sk.Sig.CreationTime
		}
	}
	t = t.Add(time.Second)
	if _, ok := e.EncryptionKey(t); ok {
		return t, true
	}
	return time.Time{}, false
}

func pgpInfo(e *openpgp.Entity) (*KeyInfo, error) {
	t, _ := encryptionTime(e)
	k, ok := e.EncryptionKey(t)
	if !ok {
		return nil, ErrNoEncryptionKey
	}
	info := &KeyInfo{Kind: KindPGP, Fingerprint: strings.ToUpper(hex.EncodeToString(e.PrimaryKey.Fingerprint)), Emails: []string{}}
	var names []string
	for name, id := range e.Identities {
		names = append(names, name)
		if id.UserId != nil && id.UserId.Email != "" {
			info.Emails = append(info.Emails, strings.ToLower(id.UserId.Email))
		}
	}
	if id := e.PrimaryIdentity(); id != nil {
		info.Description = id.Name
	} else if len(names) > 0 {
		info.Description = names[0]
	}
	if bits, err := k.PublicKey.BitLength(); err == nil {
		info.Algorithm = fmt.Sprintf("%s %d", pubKeyAlgoName(k.PublicKey.PubKeyAlgo), bits)
	} else {
		info.Algorithm = pubKeyAlgoName(k.PublicKey.PubKeyAlgo)
	}
	if k.SelfSignature != nil && k.SelfSignature.KeyLifetimeSecs != nil && *k.SelfSignature.KeyLifetimeSecs > 0 {
		exp := k.PublicKey.CreationTime.Add(time.Duration(*k.SelfSignature.KeyLifetimeSecs) * time.Second).UTC()
		info.Expires = &exp
	}
	if sig, _ := e.PrimarySelfSignature(); sig != nil && sig.KeyLifetimeSecs != nil && *sig.KeyLifetimeSecs > 0 {
		exp := e.PrimaryKey.CreationTime.Add(time.Duration(*sig.KeyLifetimeSecs) * time.Second).UTC()
		if info.Expires == nil || exp.Before(*info.Expires) {
			info.Expires = &exp
		}
	}
	return info, nil
}

func pubKeyAlgoName(a packet.PublicKeyAlgorithm) string {
	switch a {
	case packet.PubKeyAlgoRSA, packet.PubKeyAlgoRSAEncryptOnly:
		return "RSA"
	case packet.PubKeyAlgoElGamal:
		return "ElGamal"
	case packet.PubKeyAlgoECDH:
		return "ECDH"
	case packet.PubKeyAlgoX25519:
		return "X25519"
	case packet.PubKeyAlgoX448:
		return "X448"
	}
	return fmt.Sprintf("Algorithmus %d", a)
}

func readCert(data []byte) (*x509.Certificate, error) {
	der := data
	if block, _ := pem.Decode(data); block != nil {
		if strings.Contains(block.Type, "PRIVATE KEY") {
			return nil, errors.New("das ist ein privater Schlüssel – bitte nur das Zertifikat hochladen")
		}
		if block.Type != "CERTIFICATE" {
			return nil, fmt.Errorf("PEM-Block %q ist kein Zertifikat", block.Type)
		}
		der = block.Bytes
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		return nil, fmt.Errorf("Zertifikat nicht lesbar: %v", err)
	}
	if _, ok := cert.PublicKey.(*rsa.PublicKey); !ok {
		return nil, errors.New("nur Zertifikate mit RSA-Schlüssel werden unterstützt")
	}
	if cert.KeyUsage != 0 && cert.KeyUsage&(x509.KeyUsageKeyEncipherment) == 0 {
		return nil, errors.New("das Zertifikat erlaubt keine Verschlüsselung (Key Usage)")
	}
	return cert, nil
}

func certInfo(cert *x509.Certificate) *KeyInfo {
	sum := sha256.Sum256(cert.Raw)
	exp := cert.NotAfter.UTC()
	info := &KeyInfo{Kind: KindSMIME, Fingerprint: strings.ToUpper(hex.EncodeToString(sum[:])), Expires: &exp, Emails: []string{}}
	for _, e := range cert.EmailAddresses {
		info.Emails = append(info.Emails, strings.ToLower(e))
	}
	info.Description = cert.Subject.CommonName
	if info.Description == "" {
		info.Description = cert.Subject.String()
	}
	if k, ok := cert.PublicKey.(*rsa.PublicKey); ok {
		info.Algorithm = fmt.Sprintf("RSA %d", k.N.BitLen())
	}
	return info
}

// Options select the key for Encrypt.
type Options struct {
	Kind      string
	PublicKey []byte
	// HideSubject replaces the outer Subject with "..." (the real subject
	// stays inside the encrypted part).
	HideSubject bool
}

// clearHeaders stay readable on the outer message.
var clearHeaders = map[string]bool{"from": true, "to": true, "subject": true, "date": true, "message-id": true}

// Encrypt returns the encrypted form of raw. Messages that are already
// encrypted are returned unchanged.
func Encrypt(raw []byte, opt Options) ([]byte, error) {
	if IsEncrypted(raw) {
		return raw, nil
	}
	outer := outerHeaders(raw, opt.HideSubject)
	var body []byte
	var err error
	switch opt.Kind {
	case KindPGP:
		body, err = encryptPGP(raw, opt.PublicKey, outer)
	case KindSMIME:
		body, err = encryptSMIME(raw, opt.PublicKey, outer)
	default:
		err = fmt.Errorf("unknown key kind %q", opt.Kind)
	}
	if err != nil {
		return nil, err
	}
	return body, nil
}

// outerHeaders copies the clear header fields (with their original folding)
// and normalizes line endings to CRLF.
func outerHeaders(raw []byte, hideSubject bool) []byte {
	var out bytes.Buffer
	keep := false
	for _, line := range headerLines(raw) {
		if line == "" {
			continue
		}
		if line[0] == ' ' || line[0] == '\t' {
			if keep {
				out.WriteString(line + "\r\n")
			}
			continue
		}
		name, _, _ := strings.Cut(line, ":")
		name = strings.ToLower(strings.TrimSpace(name))
		keep = clearHeaders[name]
		if name == "subject" && hideSubject {
			out.WriteString("Subject: ...\r\n")
			keep = false
			continue
		}
		if keep {
			out.WriteString(line + "\r\n")
		}
	}
	return out.Bytes()
}

// headerLines returns the lines of the header block (without line endings).
func headerLines(raw []byte) []string {
	head := raw
	if i := bytes.Index(raw, []byte("\r\n\r\n")); i >= 0 {
		head = raw[:i]
	} else if i := bytes.Index(raw, []byte("\n\n")); i >= 0 {
		head = raw[:i]
	}
	s := strings.ReplaceAll(string(head), "\r\n", "\n")
	return strings.Split(s, "\n")
}

func boundary() string {
	b := make([]byte, 18)
	_, _ = rand.Read(b)
	return "pw-enc-" + hex.EncodeToString(b)
}

func encryptPGP(raw, key []byte, outer []byte) ([]byte, error) {
	e, err := readEntity(key)
	if err != nil {
		return nil, err
	}
	t, ok := encryptionTime(e)
	if !ok {
		return nil, ErrNoEncryptionKey
	}
	cfg := &packet.Config{DefaultCipher: packet.CipherAES256, Time: func() time.Time { return t }}
	var armored bytes.Buffer
	aw, err := armor.Encode(&armored, "PGP MESSAGE", nil)
	if err != nil {
		return nil, err
	}
	w, err := openpgp.Encrypt(aw, []*openpgp.Entity{e}, nil, &openpgp.FileHints{IsBinary: true}, cfg)
	if err != nil {
		return nil, err
	}
	if _, err := w.Write(raw); err != nil {
		return nil, err
	}
	if err := w.Close(); err != nil {
		return nil, err
	}
	if err := aw.Close(); err != nil {
		return nil, err
	}
	b := boundary()
	var out bytes.Buffer
	out.Write(outer)
	out.WriteString("MIME-Version: 1.0\r\n")
	out.WriteString("Content-Type: multipart/encrypted; protocol=\"application/pgp-encrypted\"; boundary=\"" + b + "\"\r\n")
	out.WriteString("\r\nThis is an OpenPGP/MIME encrypted message (RFC 3156).\r\n")
	out.WriteString("--" + b + "\r\n")
	out.WriteString("Content-Type: application/pgp-encrypted\r\nContent-Description: PGP/MIME version identification\r\n\r\nVersion: 1\r\n\r\n")
	out.WriteString("--" + b + "\r\n")
	out.WriteString("Content-Type: application/octet-stream; name=\"encrypted.asc\"\r\n")
	out.WriteString("Content-Description: OpenPGP encrypted message\r\n")
	out.WriteString("Content-Disposition: inline; filename=\"encrypted.asc\"\r\n\r\n")
	out.WriteString(strings.ReplaceAll(strings.ReplaceAll(armored.String(), "\r\n", "\n"), "\n", "\r\n"))
	out.WriteString("\r\n--" + b + "--\r\n")
	return out.Bytes(), nil
}

func encryptSMIME(raw, certData []byte, outer []byte) ([]byte, error) {
	cert, err := readCert(certData)
	if err != nil {
		return nil, err
	}
	der, err := pkcs7.Encrypt(raw, []*x509.Certificate{cert})
	if err != nil {
		return nil, err
	}
	var out bytes.Buffer
	out.Write(outer)
	out.WriteString("MIME-Version: 1.0\r\n")
	out.WriteString("Content-Type: application/pkcs7-mime; smime-type=enveloped-data; name=\"smime.p7m\"\r\n")
	out.WriteString("Content-Transfer-Encoding: base64\r\n")
	out.WriteString("Content-Disposition: attachment; filename=\"smime.p7m\"\r\n")
	out.WriteString("Content-Description: S/MIME Encrypted Message\r\n\r\n")
	enc := base64.StdEncoding.EncodeToString(der)
	for len(enc) > 76 {
		out.WriteString(enc[:76] + "\r\n")
		enc = enc[76:]
	}
	out.WriteString(enc + "\r\n")
	return out.Bytes(), nil
}

// IsEncrypted reports whether raw is a PGP/MIME or S/MIME encrypted message.
func IsEncrypted(raw []byte) bool {
	var ct string
	hdr, err := mail.ReadMessage(bytes.NewReader(append(headerBlock(raw), '\r', '\n', '\r', '\n')))
	if err == nil {
		ct = hdr.Header.Get("Content-Type")
	}
	if ct == "" {
		return false
	}
	mt, params, err := mime.ParseMediaType(ct)
	if err != nil {
		return false
	}
	switch mt {
	case "multipart/encrypted":
		return strings.EqualFold(params["protocol"], "application/pgp-encrypted")
	case "application/pkcs7-mime", "application/x-pkcs7-mime":
		st := strings.ToLower(params["smime-type"])
		if st == "" {
			name := strings.ToLower(params["name"])
			return strings.HasSuffix(name, ".p7m")
		}
		return st == "enveloped-data" || st == "authenveloped-data"
	}
	return false
}

func headerBlock(raw []byte) []byte {
	if i := bytes.Index(raw, []byte("\r\n\r\n")); i >= 0 {
		return raw[:i]
	}
	if i := bytes.Index(raw, []byte("\n\n")); i >= 0 {
		return raw[:i]
	}
	return raw
}
