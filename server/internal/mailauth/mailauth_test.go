package mailauth

import (
	"bytes"
	"strings"
	"testing"

	"github.com/emersion/go-msgauth/dkim"
	"github.com/emersion/go-msgauth/dmarc"
)

func pct(n int) *int { return &n }

func TestEvaluateDMARCPolicies(t *testing.T) {
	defer func(f func() int) { randPercent = f }(randPercent)

	failing := func() Result {
		return Result{SPF: "pass", SPFDomain: "attacker.test", DKIM: "pass", DKIMDomains: []string{"attacker.test"}, FromDomain: "bank.test"}
	}

	r := failing()
	evaluateDMARC(&r, &dmarc.Record{Policy: dmarc.PolicyReject}, false)
	if r.DMARC != "fail" || r.DMARCPolicy != "reject" {
		t.Fatalf("reject: %+v", r)
	}

	// sp= applies when the record was found at the organizational domain.
	r = failing()
	r.FromDomain = "mail.bank.test"
	evaluateDMARC(&r, &dmarc.Record{Policy: dmarc.PolicyReject, SubdomainPolicy: dmarc.PolicyQuarantine}, true)
	if r.DMARCPolicy != "quarantine" {
		t.Fatalf("sp: %+v", r)
	}
	r = failing()
	evaluateDMARC(&r, &dmarc.Record{Policy: dmarc.PolicyReject, SubdomainPolicy: dmarc.PolicyNone}, false)
	if r.DMARCPolicy != "reject" {
		t.Fatalf("sp must not apply to the domain itself: %+v", r)
	}

	// pct=: sampled messages get the policy, the rest the next weaker one.
	randPercent = func() int { return 10 }
	r = failing()
	evaluateDMARC(&r, &dmarc.Record{Policy: dmarc.PolicyReject, Percent: pct(20)}, false)
	if r.DMARCPolicy != "reject" {
		t.Fatalf("pct sampled: %+v", r)
	}
	randPercent = func() int { return 50 }
	r = failing()
	evaluateDMARC(&r, &dmarc.Record{Policy: dmarc.PolicyReject, Percent: pct(20)}, false)
	if r.DMARCPolicy != "quarantine" {
		t.Fatalf("pct not sampled (reject): %+v", r)
	}
	r = failing()
	evaluateDMARC(&r, &dmarc.Record{Policy: dmarc.PolicyQuarantine, Percent: pct(0)}, false)
	if r.DMARCPolicy != "none" {
		t.Fatalf("pct not sampled (quarantine): %+v", r)
	}

	// Aligned DKIM passes.
	r = Result{SPF: "fail", SPFDomain: "bounce.mailer.test", DKIM: "pass", DKIMDomains: []string{"news.bank.test"}, FromDomain: "bank.test"}
	evaluateDMARC(&r, &dmarc.Record{Policy: dmarc.PolicyReject}, false)
	if r.DMARC != "pass" {
		t.Fatalf("relaxed dkim alignment: %+v", r)
	}
	r = Result{SPF: "fail", DKIM: "pass", DKIMDomains: []string{"news.bank.test"}, FromDomain: "bank.test"}
	evaluateDMARC(&r, &dmarc.Record{Policy: dmarc.PolicyReject, DKIMAlignment: dmarc.AlignmentStrict}, false)
	if r.DMARC != "fail" {
		t.Fatalf("strict dkim alignment: %+v", r)
	}
}

func TestAuthenticatedFor(t *testing.T) {
	cases := []struct {
		r    Result
		want bool
	}{
		{Result{SPF: "pass", SPFDomain: "example.test"}, true},
		{Result{SPF: "pass", SPFDomain: "bounces.example.test"}, true},
		{Result{SPF: "pass", SPFDomain: "attacker.test"}, false},
		{Result{SPF: "softfail", SPFDomain: "example.test"}, false},
		{Result{SPF: "fail", DKIMDomains: []string{"example.test"}}, true},
		{Result{SPF: "fail", DKIMDomains: []string{"notexample.test"}}, false},
		{Result{SPF: "pass", SPFDomain: "evil-example.test", DKIMDomains: []string{"other.test"}}, false},
	}
	for i, c := range cases {
		if got := c.r.AuthenticatedFor("example.test"); got != c.want {
			t.Errorf("case %d: got %v want %v (%+v)", i, got, c.want, c.r)
		}
	}
	// A sibling under a shared parent never vouches for a hosted domain.
	if (Result{DKIMDomains: []string{"attacker.dyn.test"}}).AuthenticatedFor("felix.dyn.test") {
		t.Error("sibling domain accepted")
	}
}

func TestSignOversignsHeaders(t *testing.T) {
	key, err := GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	raw := []byte("From: a@example.test\r\nTo: b@example.test\r\nSubject: Hallo\r\nDate: Fri, 20 Nov 2026 10:00:00 +0100\r\nMessage-ID: <1@example.test>\r\n\r\nText\r\n")
	signed, err := Sign(raw, "example.test", "pw1", key)
	if err != nil {
		t.Fatal(err)
	}
	sig := string(signed[:bytes.Index(signed, []byte("\r\nFrom:"))])
	h := strings.ToLower(strings.Join(strings.Fields(sig), ""))
	for _, k := range []string{"from:from", "subject:subject", "to:to", "date:date", "message-id:message-id"} {
		if !strings.Contains(h, k) {
			t.Errorf("h= does not oversign %s: %s", k, sig)
		}
	}
	txt, err := DNSValue(key)
	if err != nil {
		t.Fatal(err)
	}
	verify := func(msg []byte) error {
		v, err := dkim.VerifyWithOptions(bytes.NewReader(msg), &dkim.VerifyOptions{
			LookupTXT: func(string) ([]string, error) { return []string{txt}, nil },
		})
		if err != nil {
			return err
		}
		if len(v) != 1 {
			t.Fatalf("verifications: %d", len(v))
		}
		return v[0].Err
	}
	if err := verify(signed); err != nil {
		t.Fatalf("signature does not verify: %v", err)
	}
	// Prepending a second From header after signing must break it.
	i := bytes.Index(signed, []byte("\r\nFrom:")) + 2
	forged := append(append(append([]byte{}, signed[:i]...), []byte("From: evil@attacker.test\r\n")...), signed[i:]...)
	if err := verify(forged); err == nil {
		t.Fatal("signature still valid after adding a From header")
	}
}
