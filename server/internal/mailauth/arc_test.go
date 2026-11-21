package mailauth

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net"
	"strings"
	"testing"

	"github.com/emersion/go-msgauth/dkim"
)

// fakeDNS serves DKIM key records for tests.
type fakeDNS map[string]string

func (f fakeDNS) install(t *testing.T) {
	t.Helper()
	old := lookupTXT
	lookupTXT = func(_ context.Context, name string) ([]string, error) {
		if v, ok := f[strings.ToLower(name)]; ok {
			return []string{v}, nil
		}
		return nil, &net.DNSError{Err: "no such host", Name: name, IsNotFound: true}
	}
	t.Cleanup(func() { lookupTXT = old })
}

func (f fakeDNS) addKey(t *testing.T, domain, selector, alg string) string {
	t.Helper()
	key, err := GenerateKeyFor(alg)
	if err != nil {
		t.Fatal(err)
	}
	v, err := DNSValue(key)
	if err != nil {
		t.Fatal(err)
	}
	f[selector+"._domainkey."+domain] = v
	return key
}

const arcTestMsg = "From: Alice <alice@origin.test>\r\n" +
	"To: list@forwarder.test\r\n" +
	"Subject: ARC test\r\n" +
	"Date: Fri, 20 Nov 2026 10:00:00 +0100\r\n" +
	"Message-ID: <arc-1@origin.test>\r\n" +
	"MIME-Version: 1.0\r\n" +
	"Content-Type: text/plain; charset=utf-8\r\n" +
	"\r\n" +
	"Hello   world  \r\n" +
	"second line\r\n" +
	"\r\n\r\n"

func seal(t *testing.T, raw []byte, domain, selector, key, cv string) []byte {
	t.Helper()
	out, err := SealARC(raw, ARCSealOptions{
		Domain: domain, Selector: selector, PrivateKey: key, AuthServID: "mx." + domain,
		AuthResults: "spf=pass smtp.mailfrom=origin.test; dkim=pass header.d=origin.test; dmarc=pass header.from=origin.test", ChainValidation: cv,
	})
	if err != nil {
		t.Fatalf("seal: %v", err)
	}
	return out
}

func TestARCSealVerifyRoundTrip(t *testing.T) {
	dns := fakeDNS{}
	dns.install(t)
	key := dns.addKey(t, "forwarder.test", "arc1", AlgRSA)

	sealed := seal(t, []byte(arcTestMsg), "forwarder.test", "arc1", key, "none")
	if !bytes.HasPrefix(sealed, []byte("ARC-Seal: i=1;")) {
		t.Fatalf("seal not prepended: %q", sealed[:60])
	}
	res := VerifyARC(context.Background(), sealed)
	if res.Result != "pass" {
		t.Fatalf("verify: %+v", res)
	}
	if len(res.Sets) != 1 || res.Sets[0].SealDomain != "forwarder.test" || res.Sets[0].CV != "none" {
		t.Fatalf("sets: %+v", res.Sets)
	}
	if !strings.Contains(res.Sets[0].AuthResults, "dmarc=pass header.from=origin.test") {
		t.Fatalf("aar payload: %q", res.Sets[0].AuthResults)
	}

	// Headers added on top later (Received, our Authentication-Results)
	// and whitespace changes under relaxed canonicalization do not matter.
	relayed := append([]byte("Received: from x by y; Fri, 20 Nov 2026 10:01:00 +0100\r\nAuthentication-Results: mx.final.test; arc=pass\r\n"), sealed...)
	relayed = bytes.Replace(relayed, []byte("Subject: ARC test"), []byte("Subject:   ARC    test"), 1)
	if res := VerifyARC(context.Background(), relayed); res.Result != "pass" {
		t.Fatalf("relayed: %+v", res)
	}

	// Bare LF line endings are normalized.
	lf := bytes.ReplaceAll(sealed, []byte("\r\n"), []byte("\n"))
	if res := VerifyARC(context.Background(), lf); res.Result != "pass" {
		t.Fatalf("lf: %+v", res)
	}
}

func TestARCEd25519(t *testing.T) {
	dns := fakeDNS{}
	dns.install(t)
	key := dns.addKey(t, "forwarder.test", "ed", AlgEd25519)
	sealed := seal(t, []byte(arcTestMsg), "forwarder.test", "ed", key, "none")
	if !bytes.Contains(sealed, []byte("a=ed25519-sha256")) {
		t.Fatal("expected ed25519 algorithm")
	}
	if res := VerifyARC(context.Background(), sealed); res.Result != "pass" {
		t.Fatalf("verify: %+v", res)
	}
}

func TestARCNone(t *testing.T) {
	if res := VerifyARC(context.Background(), []byte(arcTestMsg)); res.Result != "none" {
		t.Fatalf("got %+v", res)
	}
}

func TestARCTampered(t *testing.T) {
	dns := fakeDNS{}
	dns.install(t)
	key := dns.addKey(t, "forwarder.test", "arc1", AlgRSA)
	sealed := seal(t, []byte(arcTestMsg), "forwarder.test", "arc1", key, "none")

	body := bytes.Replace(sealed, []byte("second line"), []byte("second LINE"), 1)
	res := VerifyARC(context.Background(), body)
	if res.Result != "fail" || !strings.Contains(res.Reason, "body hash") {
		t.Fatalf("tampered body: %+v", res)
	}

	hdr := bytes.Replace(sealed, []byte("Subject: ARC test"), []byte("Subject: ARC test!"), 1)
	if res := VerifyARC(context.Background(), hdr); res.Result != "fail" || !strings.Contains(res.Reason, "ams") {
		t.Fatalf("tampered header: %+v", res)
	}

	aar := bytes.Replace(sealed, []byte("dmarc=pass header.from=origin.test"), []byte("dmarc=pass header.from=evil.test"), 1)
	if res := VerifyARC(context.Background(), aar); res.Result != "fail" || !strings.Contains(res.Reason, "seal") {
		t.Fatalf("tampered aar: %+v", res)
	}

	// Unknown key.
	delete(dns, "arc1._domainkey.forwarder.test")
	if res := VerifyARC(context.Background(), sealed); res.Result != "fail" {
		t.Fatalf("missing key: %+v", res)
	}
}

func TestARCTwoHops(t *testing.T) {
	dns := fakeDNS{}
	dns.install(t)
	k1 := dns.addKey(t, "forwarder.test", "s1", AlgRSA)
	k2 := dns.addKey(t, "list.test", "s2", AlgRSA)

	hop1 := seal(t, []byte(arcTestMsg), "forwarder.test", "s1", k1, "none")
	// The list adds a footer-free header and re-seals.
	hop1 = append([]byte("Received: from forwarder.test by list.test\r\n"), hop1...)
	r1 := VerifyARC(context.Background(), hop1)
	if r1.Result != "pass" {
		t.Fatalf("hop1: %+v", r1)
	}
	hop2 := seal(t, hop1, "list.test", "s2", k2, r1.Result)
	if !bytes.HasPrefix(hop2, []byte("ARC-Seal: i=2;")) || !bytes.Contains(hop2[:200], []byte("cv=pass")) {
		t.Fatalf("hop2 seal: %q", hop2[:200])
	}
	r2 := VerifyARC(context.Background(), hop2)
	if r2.Result != "pass" || len(r2.Sets) != 2 || r2.Sets[1].SealDomain != "list.test" || r2.Sets[1].CV != "pass" {
		t.Fatalf("hop2: %+v", r2)
	}

	// Modifying the first seal breaks the chain.
	broken := bytes.Replace(hop2, []byte("d=forwarder.test; s=s1"), []byte("d=forwarder.test;   s=s1"), 1)
	if r := VerifyARC(context.Background(), broken); r.Result != "pass" {
		// relaxed canonicalization tolerates whitespace changes
		t.Fatalf("whitespace change in seal: %+v", r)
	}
	broken = bytes.Replace(hop2, []byte("cv=none"), []byte("cv=pass"), 1)
	if r := VerifyARC(context.Background(), broken); r.Result != "fail" {
		t.Fatalf("modified cv: %+v", r)
	}
}

func TestARCFailPropagation(t *testing.T) {
	dns := fakeDNS{}
	dns.install(t)
	k1 := dns.addKey(t, "forwarder.test", "s1", AlgRSA)
	k2 := dns.addKey(t, "list.test", "s2", AlgRSA)
	k3 := dns.addKey(t, "third.test", "s3", AlgRSA)

	hop1 := seal(t, []byte(arcTestMsg), "forwarder.test", "s1", k1, "none")
	// Someone changes the body between hop 1 and hop 2.
	hop1 = bytes.Replace(hop1, []byte("Hello"), []byte("Hallo"), 1)
	r1 := VerifyARC(context.Background(), hop1)
	if r1.Result != "fail" {
		t.Fatalf("expected fail, got %+v", r1)
	}
	hop2 := seal(t, hop1, "list.test", "s2", k2, r1.Result)
	if !bytes.Contains(hop2[:200], []byte("cv=fail")) {
		t.Fatalf("cv=fail not set: %q", hop2[:200])
	}
	r2 := VerifyARC(context.Background(), hop2)
	if r2.Result != "fail" || !strings.Contains(r2.Reason, "chain marked as failed") {
		t.Fatalf("hop2: %+v", r2)
	}
	// A failed chain is not sealed again.
	if _, err := SealARC(hop2, ARCSealOptions{Domain: "third.test", Selector: "s3", PrivateKey: k3, ChainValidation: "fail"}); !errors.Is(err, ErrARCChainFailed) {
		t.Fatalf("expected ErrARCChainFailed, got %v", err)
	}
}

func TestARCStructure(t *testing.T) {
	dns := fakeDNS{}
	dns.install(t)
	k1 := dns.addKey(t, "forwarder.test", "s1", AlgRSA)
	sealed := seal(t, []byte(arcTestMsg), "forwarder.test", "s1", k1, "none")

	// Missing ARC-Seal.
	fields, body := splitMessage(sealed)
	var noSeal bytes.Buffer
	for _, f := range fields {
		if f.key != "arc-seal" {
			noSeal.WriteString(f.raw)
		}
	}
	noSeal.WriteString("\r\n")
	noSeal.Write(body)
	if r := VerifyARC(context.Background(), noSeal.Bytes()); r.Result != "fail" || !strings.Contains(r.Reason, "incomplete") {
		t.Fatalf("missing seal: %+v", r)
	}

	// Duplicate AAR.
	dup := append([]byte("ARC-Authentication-Results: i=1; x; none\r\n"), sealed...)
	if r := VerifyARC(context.Background(), dup); r.Result != "fail" || !strings.Contains(r.Reason, "duplicate") {
		t.Fatalf("duplicate: %+v", r)
	}

	// Instance out of range.
	big := append([]byte("ARC-Seal: i=51; a=rsa-sha256; cv=pass; d=x.test; s=s; b=AA==\r\n"), sealed...)
	if r := VerifyARC(context.Background(), big); r.Result != "fail" {
		t.Fatalf("i=51: %+v", r)
	}

	// Gap in the chain (i=1 and i=3).
	gap := append([]byte("ARC-Seal: i=3; a=rsa-sha256; cv=pass; d=x.test; s=s; b=AA==\r\nARC-Message-Signature: i=3; a=rsa-sha256; d=x.test; s=s; h=from; bh=AA==; b=AA==\r\nARC-Authentication-Results: i=3; x; none\r\n"), sealed...)
	if r := VerifyARC(context.Background(), gap); r.Result != "fail" {
		t.Fatalf("gap: %+v", r)
	}

	// The first set must carry cv=none.
	wrongCV := bytes.Replace(sealed, []byte("cv=none"), []byte("cv=pass"), 1)
	if r := VerifyARC(context.Background(), wrongCV); r.Result != "fail" || !strings.Contains(r.Reason, "cv=") {
		t.Fatalf("cv: %+v", r)
	}
}

func TestARCSealLimit(t *testing.T) {
	key, _ := GenerateKeyFor(AlgRSA)
	var hdr strings.Builder
	for i := 1; i <= MaxARCInstances; i++ {
		fmt.Fprintf(&hdr, "ARC-Seal: i=%d; a=rsa-sha256; cv=pass; d=x.test; s=s; b=AA==\r\n", i)
		fmt.Fprintf(&hdr, "ARC-Message-Signature: i=%d; a=rsa-sha256; d=x.test; s=s; h=from; bh=AA==; b=AA==\r\n", i)
		fmt.Fprintf(&hdr, "ARC-Authentication-Results: i=%d; x; none\r\n", i)
	}
	_, err := SealARC([]byte(hdr.String()+arcTestMsg), ARCSealOptions{Domain: "y.test", Selector: "s", PrivateKey: key, ChainValidation: "pass"})
	if !errors.Is(err, ErrARCLimit) {
		t.Fatalf("expected ErrARCLimit, got %v", err)
	}
}

// TestCanonicalizationMatchesDKIM cross-checks the relaxed and simple
// canonicalization against go-msgauth: a DKIM-Signature has the same
// structure as an ARC-Message-Signature, so verifyAMS must accept it.
func TestCanonicalizationMatchesDKIM(t *testing.T) {
	dns := fakeDNS{}
	dns.install(t)
	msg := "From:  Alice   <alice@origin.test> \r\n" +
		"To: bob@dest.test,\r\n\t carol@dest.test\r\n" +
		"Subject: \t Mixed   whitespace\t\r\n" +
		"Date: Fri, 20 Nov 2026 10:00:00 +0100\r\n" +
		"\r\n" +
		"Line with trailing space   \r\n" +
		"  indented\tline\r\n" +
		"\r\n\r\n"
	for _, alg := range []string{AlgRSA, AlgEd25519} {
		for _, c := range []dkim.Canonicalization{dkim.CanonicalizationRelaxed, dkim.CanonicalizationSimple} {
			key := dns.addKey(t, "origin.test", "dk", alg)
			signer, _, _ := parseSigner(key)
			var out bytes.Buffer
			err := dkim.Sign(&out, strings.NewReader(msg), &dkim.SignOptions{
				Domain: "origin.test", Selector: "dk", Signer: signer,
				HeaderCanonicalization: c, BodyCanonicalization: c,
				HeaderKeys: []string{"From", "To", "Subject", "Date"},
			})
			if err != nil {
				t.Fatal(err)
			}
			fields, body := splitMessage(out.Bytes())
			var sigField string
			for _, f := range fields {
				if f.key == "dkim-signature" {
					sigField = f.raw
				}
			}
			tags, err := parseTags(fieldValue(sigField))
			if err != nil {
				t.Fatal(err)
			}
			set := &arcRawSet{ams: sigField, amsTags: tags}
			if err := verifyAMS(context.Background(), fields, body, set); err != nil {
				t.Fatalf("%s/%s: %v", alg, c, err)
			}
		}
	}
}

func TestARCOverride(t *testing.T) {
	chain := ARCResult{Result: "pass", Sets: []ARCSet{
		{Instance: 1, SealDomain: "google.com", AuthResults: "mx.google.com; dkim=pass header.d=bank.test; spf=pass smtp.mailfrom=bank.test; dmarc=pass (p=REJECT) header.from=bank.test"},
		{Instance: 2, SealDomain: "lists.example.org", AuthResults: "lists.example.org; dmarc=fail header.from=bank.test"},
	}}
	r := Result{DMARC: "fail", DMARCPolicy: "reject", FromDomain: "bank.test", ARC: "pass", ARCChain: chain}

	if a, _ := r.ARCOverride(nil); a != "" {
		t.Fatalf("no trusted sealers: %q", a)
	}
	if a, s := r.ARCOverride([]string{"google.com"}); a != "accept" || s != "google.com" {
		t.Fatalf("trusted google: %q %q", a, s)
	}
	if a, s := r.ARCOverride([]string{"example.org"}); a != "quarantine" || s != "lists.example.org" {
		t.Fatalf("trusted list without pass: %q %q", a, s)
	}
	// Results for a different From domain do not count.
	other := r
	other.FromDomain = "other.test"
	if a, _ := other.ARCOverride([]string{"google.com"}); a != "quarantine" {
		t.Fatalf("other domain: %q", a)
	}
	// A failed chain or a passing DMARC never triggers an override.
	failed := r
	failed.ARC = "fail"
	if a, _ := failed.ARCOverride([]string{"google.com"}); a != "" {
		t.Fatalf("failed chain: %q", a)
	}
	passing := r
	passing.DMARC = "pass"
	if a, _ := passing.ARCOverride([]string{"google.com"}); a != "" {
		t.Fatalf("dmarc pass: %q", a)
	}
	// Look-alike domains are not trusted.
	evil := r
	evil.ARCChain = ARCResult{Result: "pass", Sets: []ARCSet{{Instance: 1, SealDomain: "notgoogle.com", AuthResults: "x; dmarc=pass header.from=bank.test"}}}
	if a, _ := evil.ARCOverride([]string{"google.com"}); a != "" {
		t.Fatalf("look-alike: %q", a)
	}
}

func TestResultsStringIncludesARC(t *testing.T) {
	r := Result{SPF: "pass", SPFDomain: "a.test", DKIM: "none", DMARC: "fail", FromDomain: "a.test", ARC: "pass", DMARCOverride: "arc=pass, sealed by google.com"}
	h := r.Header("mx.test")
	if !strings.Contains(h, "arc=pass") || !strings.Contains(h, "dmarc=fail (policy overridden: arc=pass, sealed by google.com)") {
		t.Fatalf("header: %s", h)
	}
	r.ARC, r.ARCChain.Reason = "fail", "seal i=1: bad (sig)"
	if h := r.Header("mx.test"); !strings.Contains(h, "arc=fail (seal i=1: bad sig)") {
		t.Fatalf("header: %s", h)
	}
}
