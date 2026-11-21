package atrest

import (
	"bytes"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/pem"
	"io"
	"math/big"
	"mime"
	"mime/multipart"
	"net/mail"
	"strings"
	"testing"
	"time"

	"github.com/ProtonMail/go-crypto/openpgp"
	"github.com/ProtonMail/go-crypto/openpgp/armor"
	"github.com/ProtonMail/go-crypto/openpgp/packet"
	"github.com/smallstep/pkcs7"
)

const sample = "Received: from mx.example.org by mail.example.test\r\n" +
	"From: Alice <alice@example.org>\r\n" +
	"To: Bob <bob@example.test>\r\n" +
	"Cc: Carol <carol@example.org>\r\n" +
	"Subject: Geheime\r\n Besprechung\r\n" +
	"Date: Mon, 2 Nov 2026 10:00:00 +0100\r\n" +
	"Message-ID: <abc@example.org>\r\n" +
	"MIME-Version: 1.0\r\n" +
	"Content-Type: text/plain; charset=utf-8\r\n" +
	"\r\n" +
	"Der Tresorcode ist 4711.\r\n"

// PGPKey returns a test key pair and the armored public key.
func newPGPKey(t testing.TB) (*openpgp.Entity, []byte) {
	t.Helper()
	e, err := openpgp.NewEntity("Bob", "", "bob@example.test", &packet.Config{Algorithm: packet.PubKeyAlgoEdDSA})
	if err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	w, _ := armor.Encode(&buf, openpgp.PublicKeyType, nil)
	if err := e.Serialize(w); err != nil {
		t.Fatal(err)
	}
	w.Close()
	return e, buf.Bytes()
}

func newCert(t testing.TB) (*x509.Certificate, *rsa.PrivateKey, []byte) {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "Bob"}, EmailAddresses: []string{"bob@example.test"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(365 * 24 * time.Hour),
		KeyUsage: x509.KeyUsageKeyEncipherment | x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageEmailProtection},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	cert, _ := x509.ParseCertificate(der)
	return cert, key, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
}

func checkOuter(t *testing.T, out []byte, hideSubject bool) *mail.Message {
	t.Helper()
	m, err := mail.ReadMessage(bytes.NewReader(out))
	if err != nil {
		t.Fatal(err)
	}
	h := m.Header
	if h.Get("From") != "Alice <alice@example.org>" || h.Get("To") != "Bob <bob@example.test>" ||
		h.Get("Message-Id") != "<abc@example.org>" || h.Get("Date") == "" {
		t.Fatalf("clear headers missing: %v", h)
	}
	wantSubject := "Geheime Besprechung"
	if hideSubject {
		wantSubject = "..."
	}
	if got := strings.Join(strings.Fields(h.Get("Subject")), " "); got != wantSubject {
		t.Fatalf("subject = %q, want %q", got, wantSubject)
	}
	if h.Get("Cc") != "" || h.Get("Received") != "" {
		t.Fatal("non-minimal header left in clear")
	}
	if bytes.Contains(out, []byte("Tresorcode")) || bytes.Contains(out, []byte("carol@")) {
		t.Fatal("plaintext leaked")
	}
	if !IsEncrypted(out) {
		t.Fatal("IsEncrypted = false")
	}
	return m
}

func TestEncryptPGPRoundtrip(t *testing.T) {
	priv, pub := newPGPKey(t)
	info, err := ParseKey(KindPGP, pub)
	if err != nil {
		t.Fatal(err)
	}
	if info.Fingerprint == "" || len(info.Emails) != 1 || info.Emails[0] != "bob@example.test" {
		t.Fatalf("info = %+v", info)
	}
	for _, hide := range []bool{false, true} {
		out, err := Encrypt([]byte(sample), Options{Kind: KindPGP, PublicKey: pub, HideSubject: hide})
		if err != nil {
			t.Fatal(err)
		}
		m := checkOuter(t, out, hide)
		mt, params, _ := mime.ParseMediaType(m.Header.Get("Content-Type"))
		if mt != "multipart/encrypted" || params["protocol"] != "application/pgp-encrypted" {
			t.Fatalf("content type %s %v", mt, params)
		}
		mr := multipart.NewReader(m.Body, params["boundary"])
		p1, _ := mr.NextPart()
		v, _ := io.ReadAll(p1)
		if p1.Header.Get("Content-Type") != "application/pgp-encrypted" || !strings.Contains(string(v), "Version: 1") {
			t.Fatal("bad version part")
		}
		p2, _ := mr.NextPart()
		armored, _ := io.ReadAll(p2)
		block, err := armor.Decode(bytes.NewReader(armored))
		if err != nil {
			t.Fatal(err)
		}
		md, err := openpgp.ReadMessage(block.Body, openpgp.EntityList{priv}, nil, nil)
		if err != nil {
			t.Fatal(err)
		}
		plain, _ := io.ReadAll(md.UnverifiedBody)
		if string(plain) != sample {
			t.Fatalf("decrypted message differs:\n%s", plain)
		}
	}
}

func TestEncryptSMIMERoundtrip(t *testing.T) {
	cert, key, pemCert := newCert(t)
	info, err := ParseKey(KindSMIME, pemCert)
	if err != nil {
		t.Fatal(err)
	}
	if info.Description != "Bob" || info.Expires == nil || info.Algorithm != "RSA 2048" {
		t.Fatalf("info = %+v", info)
	}
	out, err := Encrypt([]byte(sample), Options{Kind: KindSMIME, PublicKey: pemCert})
	if err != nil {
		t.Fatal(err)
	}
	m := checkOuter(t, out, false)
	mt, params, _ := mime.ParseMediaType(m.Header.Get("Content-Type"))
	if mt != "application/pkcs7-mime" || params["smime-type"] != "enveloped-data" {
		t.Fatalf("content type %s %v", mt, params)
	}
	b64, _ := io.ReadAll(m.Body)
	der, err := base64.StdEncoding.DecodeString(strings.Join(strings.Fields(string(b64)), ""))
	if err != nil {
		t.Fatal(err)
	}
	p7, err := pkcs7.Parse(der)
	if err != nil {
		t.Fatal(err)
	}
	plain, err := p7.Decrypt(cert, key)
	if err != nil {
		t.Fatal(err)
	}
	if string(plain) != sample {
		t.Fatalf("decrypted message differs:\n%s", plain)
	}
}

func TestAlreadyEncryptedUnchanged(t *testing.T) {
	_, pub := newPGPKey(t)
	out, _ := Encrypt([]byte(sample), Options{Kind: KindPGP, PublicKey: pub})
	again, err := Encrypt(out, Options{Kind: KindPGP, PublicKey: pub})
	if err != nil || !bytes.Equal(out, again) {
		t.Fatal("encrypted message was encrypted again")
	}
	if IsEncrypted([]byte(sample)) {
		t.Fatal("plain message reported as encrypted")
	}
	signed := "Content-Type: application/pkcs7-mime; smime-type=signed-data; name=smime.p7m\r\n\r\nxx"
	if IsEncrypted([]byte(signed)) {
		t.Fatal("signed-only S/MIME reported as encrypted")
	}
}

func TestParseKeyRejects(t *testing.T) {
	priv, _ := newPGPKey(t)
	var buf bytes.Buffer
	w, _ := armor.Encode(&buf, openpgp.PrivateKeyType, nil)
	priv.SerializePrivate(w, nil)
	w.Close()
	if _, err := ParseKey(KindPGP, buf.Bytes()); err == nil || !strings.Contains(err.Error(), "privater") {
		t.Fatalf("private key accepted: %v", err)
	}
	if _, err := ParseKey(KindPGP, []byte("hello")); err == nil {
		t.Fatal("garbage accepted")
	}
	_, key, _ := newCert(t)
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)})
	if _, err := ParseKey(KindSMIME, keyPEM); err == nil {
		t.Fatal("private key accepted as certificate")
	}
	if _, err := ParseKey("rot13", []byte("x")); err == nil {
		t.Fatal("unknown kind accepted")
	}
}

func TestExpiredPGPKeyStillEncrypts(t *testing.T) {
	cfg := &packet.Config{Algorithm: packet.PubKeyAlgoEdDSA, KeyLifetimeSecs: 3600,
		Time: func() time.Time { return time.Now().Add(-48 * time.Hour) }}
	e, err := openpgp.NewEntity("Old", "", "old@example.test", cfg)
	if err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	w, _ := armor.Encode(&buf, openpgp.PublicKeyType, nil)
	e.Serialize(w)
	w.Close()
	info, err := ParseKey(KindPGP, buf.Bytes())
	if err != nil {
		t.Fatal(err)
	}
	if info.Expires == nil || info.Expires.After(time.Now()) {
		t.Fatalf("expiry not reported: %+v", info.Expires)
	}
	if _, err := Encrypt([]byte(sample), Options{Kind: KindPGP, PublicKey: buf.Bytes()}); err != nil {
		t.Fatal(err)
	}
}
