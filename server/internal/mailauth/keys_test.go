package mailauth

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/emersion/go-msgauth/dkim"
)

func TestEd25519KeyAndDNSValue(t *testing.T) {
	key, err := GenerateKeyFor(AlgEd25519)
	if err != nil {
		t.Fatal(err)
	}
	if alg, err := KeyAlgorithm(key); err != nil || alg != AlgEd25519 {
		t.Fatalf("alg = %q, %v", alg, err)
	}
	v, err := DNSValue(key)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(v, "v=DKIM1; k=ed25519; p=") || len(strings.TrimPrefix(v, "v=DKIM1; k=ed25519; p=")) != 44 {
		t.Fatalf("dns value: %q", v)
	}
	rsaKey, _ := GenerateKey()
	if alg, _ := KeyAlgorithm(rsaKey); alg != AlgRSA {
		t.Fatalf("rsa alg = %q", alg)
	}
	if _, err := GenerateKeyFor("dsa"); err == nil {
		t.Fatal("unknown algorithm accepted")
	}
}

// TestDualSigning signs with RSA and Ed25519 (RFC 8463) and verifies both
// signatures with go-msgauth.
func TestDualSigning(t *testing.T) {
	rsaKey, _ := GenerateKeyFor(AlgRSA)
	edKey, _ := GenerateKeyFor(AlgEd25519)
	records := map[string]string{}
	for sel, k := range map[string]string{"r1": rsaKey, "e1": edKey} {
		v, _ := DNSValue(k)
		records[sel+"._domainkey.example.test"] = v
	}
	raw := []byte("From: a@example.test\r\nTo: b@other.test\r\nSubject: Dual\r\nDate: Fri, 20 Nov 2026 10:00:00 +0100\r\n\r\nText\r\n")
	signed, err := SignAll(raw, "example.test", []SigningKey{{"r1", rsaKey}, {"e1", edKey}, {"broken", "not a key"}})
	if err != nil {
		t.Fatal(err)
	}
	if n := bytes.Count(signed, []byte("DKIM-Signature:")); n != 2 {
		t.Fatalf("signatures = %d, want 2", n)
	}
	verifs, err := dkim.VerifyWithOptions(bytes.NewReader(signed), &dkim.VerifyOptions{
		LookupTXT: func(name string) ([]string, error) { return []string{records[name]}, nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(verifs) != 2 {
		t.Fatalf("verifications = %d", len(verifs))
	}
	for _, v := range verifs {
		if v.Err != nil {
			t.Fatalf("verification failed: %v", v.Err)
		}
	}
	for _, k := range []string{"a=rsa-sha256", "a=ed25519-sha256"} {
		if !bytes.Contains(signed, []byte(k)) {
			t.Errorf("missing %s", k)
		}
	}
	if _, err := SignAll(raw, "example.test", []SigningKey{{"x", "junk"}}); err == nil {
		t.Fatal("expected error without usable key")
	}
}

func TestCheckPublished(t *testing.T) {
	dns := fakeDNS{}
	dns.install(t)
	key := dns.addKey(t, "example.test", "pw1", AlgRSA)
	if p := CheckPublished(context.Background(), "example.test", "pw1", key); !p.Published || p.Name != "pw1._domainkey.example.test" {
		t.Fatalf("published: %+v", p)
	}
	other, _ := GenerateKeyFor(AlgRSA)
	if p := CheckPublished(context.Background(), "example.test", "pw1", other); p.Published || p.Error == "" || len(p.Found) != 1 {
		t.Fatalf("other key: %+v", p)
	}
	if p := CheckPublished(context.Background(), "example.test", "missing", key); p.Published || !strings.Contains(p.Error, "Kein TXT-Eintrag") {
		t.Fatalf("missing: %+v", p)
	}
	// Whitespace inside the published value is ignored.
	v := dns["pw1._domainkey.example.test"]
	dns["pw2._domainkey.example.test"] = strings.Replace(v, "p=", "p= ", 1)
	if p := CheckPublished(context.Background(), "example.test", "pw2", key); !p.Published {
		t.Fatalf("whitespace: %+v", p)
	}
}
