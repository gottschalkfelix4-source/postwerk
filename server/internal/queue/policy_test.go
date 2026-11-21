package queue

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/sha512"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"errors"
	"math/big"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/miekg/dns"
)

// ---- MTA-STS ----

func TestParseSTSRecord(t *testing.T) {
	cases := []struct {
		txt []string
		id  string
		ok  bool
	}{
		{[]string{"v=STSv1; id=20240101T000000;"}, "20240101T000000", true},
		{[]string{"v=STSv1;id=abc"}, "abc", true},
		{[]string{"v=spf1 -all", "v=STSv1; id=x1"}, "x1", true},
		{[]string{"v=STSv1; id=a", "v=STSv1; id=b"}, "", false}, // two records: ignore
		{[]string{"v=STSv1;"}, "", false},
		{[]string{"v=STSv1; id=bad-id"}, "", false},
		{nil, "", false},
	}
	for _, c := range cases {
		id, ok := parseSTSRecord(c.txt)
		if id != c.id || ok != c.ok {
			t.Errorf("parseSTSRecord(%q) = %q, %v", c.txt, id, ok)
		}
	}
}

func TestParseSTSPolicy(t *testing.T) {
	p, err := parseSTSPolicy("version: STSv1\r\nmode: enforce\r\nmx: mail.example.com\r\nmx: *.example.net\r\nmax_age: 86400\r\n")
	if err != nil || p.Mode != "enforce" || len(p.MX) != 2 || p.MaxAge != 86400 {
		t.Fatalf("policy = %+v, %v", p, err)
	}
	for _, bad := range []string{
		"mode: enforce\nmx: a\nmax_age: 1\n",                  // no version
		"version: STSv1\nmode: enforce\nmax_age: 1\n",         // no mx
		"version: STSv1\nmode: strict\nmx: a\nmax_age: 1\n",   // bad mode
		"version: STSv1\nmode: testing\nmx: a\n",              // no max_age
		"version: STSv1\nmode: testing\nmx: a\nmax_age: -5\n", // negative
	} {
		if _, err := parseSTSPolicy(bad); err == nil {
			t.Errorf("accepted %q", bad)
		}
	}
	p, _ = parseSTSPolicy("version: STSv1\nmode: none\nmax_age: 99999999999\n")
	if p == nil || p.MaxAge != stsMaxAge {
		t.Errorf("max_age not capped: %+v", p)
	}
}

func TestSTSMatches(t *testing.T) {
	p := &STSPolicy{MX: []string{"mail.example.com", "*.example.net"}}
	for host, want := range map[string]bool{
		"mail.example.com":   true,
		"MAIL.Example.COM.":  true,
		"mx1.example.net":    true,
		"example.net":        false,
		"a.b.example.net":    false,
		"evil.com":           false,
		"mail.example.com.x": false,
	} {
		if got := p.Matches(host); got != want {
			t.Errorf("Matches(%q) = %v", host, got)
		}
	}
}

type memKV struct {
	mu sync.Mutex
	m  map[string]string
}

func (k *memKV) KV(_ context.Context, key string) (string, error) {
	k.mu.Lock()
	defer k.mu.Unlock()
	return k.m[key], nil
}

func (k *memKV) SetKV(_ context.Context, key, v string) error {
	k.mu.Lock()
	defer k.mu.Unlock()
	if k.m == nil {
		k.m = map[string]string{}
	}
	k.m[key] = v
	return nil
}

func TestMTASTSFetchAndCache(t *testing.T) {
	var hits atomic.Int32
	var down atomic.Bool
	body := "version: STSv1\nmode: enforce\nmx: mx.example.test\nmax_age: 3600\n"
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		if down.Load() {
			w.WriteHeader(500)
			return
		}
		switch r.URL.Path {
		case "/.well-known/mta-sts.txt":
			w.Header().Set("Content-Type", "text/plain; charset=utf-8")
			w.Write([]byte(body))
		case "/redirect":
			http.Redirect(w, r, "/.well-known/mta-sts.txt", http.StatusFound)
		case "/html":
			w.Header().Set("Content-Type", "text/html")
			w.Write([]byte(body))
		}
	}))
	defer srv.Close()
	txtID := "v1"
	now := time.Now()
	kv := &memKV{}
	newSTS := func(path string) *MTASTS {
		m := NewMTASTS(kv, nil)
		m.HTTP.Transport = srv.Client().Transport
		m.PolicyURL = func(string) string { return srv.URL + path }
		m.LookupTXT = func(_ context.Context, name string) ([]string, error) {
			if name != "_mta-sts.example.test" {
				return nil, errors.New("unexpected name " + name)
			}
			if txtID == "" {
				return nil, errors.New("no record")
			}
			return []string{"v=STSv1; id=" + txtID}, nil
		}
		m.Now = func() time.Time { return now }
		return m
	}
	ctx := context.Background()
	m := newSTS("/.well-known/mta-sts.txt")
	p := m.Policy(ctx, "Example.test.")
	if p == nil || p.Mode != "enforce" || p.ID != "v1" || !p.Matches("mx.example.test") {
		t.Fatalf("policy = %+v", p)
	}
	// Same ID: served from cache.
	m.Policy(ctx, "example.test")
	if hits.Load() != 1 {
		t.Fatalf("hits = %d, want 1", hits.Load())
	}
	// Persisted: a fresh instance does not fetch either.
	m2 := newSTS("/.well-known/mta-sts.txt")
	if p := m2.Policy(ctx, "example.test"); p == nil || hits.Load() != 1 {
		t.Fatalf("persisted policy not used: %+v hits=%d", p, hits.Load())
	}
	// New ID but server down: the cached policy stays in force.
	txtID = "v2"
	down.Store(true)
	if p := m.Policy(ctx, "example.test"); p == nil || p.ID != "v1" {
		t.Fatalf("cached policy lost on fetch error: %+v", p)
	}
	// DNS record removed: cached policy still applies until it expires.
	txtID = ""
	if p := m.Policy(ctx, "example.test"); p == nil {
		t.Fatal("cached policy lost without TXT record")
	}
	now = now.Add(2 * time.Hour)
	if p := m.Policy(ctx, "example.test"); p != nil {
		t.Fatalf("expired policy still used: %+v", p)
	}
	// Redirects are not followed, HTML is rejected.
	down.Store(false)
	txtID = "v3"
	for _, path := range []string{"/redirect", "/html"} {
		kv2 := &memKV{}
		m3 := newSTS(path)
		m3.Store = kv2
		if p := m3.Policy(ctx, "other.example.test"); p != nil {
			t.Errorf("%s: accepted policy %+v", path, p)
		}
	}
}

func TestMTASTSRejectsUntrustedCert(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		w.Write([]byte("version: STSv1\nmode: enforce\nmx: a\nmax_age: 60\n"))
	}))
	defer srv.Close()
	m := NewMTASTS(nil, nil) // system roots: the test certificate is not trusted
	m.PolicyURL = func(string) string { return srv.URL }
	m.LookupTXT = func(context.Context, string) ([]string, error) { return []string{"v=STSv1; id=1"}, nil }
	if p := m.Policy(context.Background(), "example.test"); p != nil {
		t.Fatalf("policy fetched over unverified TLS: %+v", p)
	}
}

// ---- certificates ----

type testPKI struct {
	ca, leaf *x509.Certificate
	leafTLS  tls.Certificate
	selfLeaf *x509.Certificate
	selfTLS  tls.Certificate
}

func newPKI(t *testing.T, host string) *testPKI {
	t.Helper()
	caKey, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	caTpl := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "Test CA"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
		IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign}
	caDER, err := x509.CreateCertificate(rand.Reader, caTpl, caTpl, &caKey.PublicKey, caKey)
	if err != nil {
		t.Fatal(err)
	}
	ca, _ := x509.ParseCertificate(caDER)
	leafKey, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	leafTpl := &x509.Certificate{SerialNumber: big.NewInt(2), Subject: pkix.Name{CommonName: host}, DNSNames: []string{host},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}, KeyUsage: x509.KeyUsageDigitalSignature}
	leafDER, err := x509.CreateCertificate(rand.Reader, leafTpl, ca, &leafKey.PublicKey, caKey)
	if err != nil {
		t.Fatal(err)
	}
	leaf, _ := x509.ParseCertificate(leafDER)
	selfKey, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	selfTpl := &x509.Certificate{SerialNumber: big.NewInt(3), Subject: pkix.Name{CommonName: "whatever"},
		NotBefore: time.Now().Add(-48 * time.Hour), NotAfter: time.Now().Add(-24 * time.Hour)} // expired on purpose
	selfDER, _ := x509.CreateCertificate(rand.Reader, selfTpl, selfTpl, &selfKey.PublicKey, selfKey)
	self, _ := x509.ParseCertificate(selfDER)
	return &testPKI{
		ca: ca, leaf: leaf,
		leafTLS:  tls.Certificate{Certificate: [][]byte{leafDER, caDER}, PrivateKey: leafKey},
		selfLeaf: self,
		selfTLS:  tls.Certificate{Certificate: [][]byte{selfDER}, PrivateKey: selfKey},
	}
}

func rec(usage, sel, match uint8, c *x509.Certificate) tlsaRecord {
	data := c.Raw
	if sel == 1 {
		data = c.RawSubjectPublicKeyInfo
	}
	switch match {
	case 1:
		s := sha256.Sum256(data)
		data = s[:]
	case 2:
		s := sha512.Sum512(data)
		data = s[:]
	}
	return tlsaRecord{Usage: usage, Selector: sel, Matching: match, Data: data}
}

func TestVerifyDANE(t *testing.T) {
	const host = "mx.example.test"
	pki := newPKI(t, host)
	chain := tls.ConnectionState{PeerCertificates: []*x509.Certificate{pki.leaf, pki.ca}}
	self := tls.ConnectionState{PeerCertificates: []*x509.Certificate{pki.selfLeaf}}
	for sel := uint8(0); sel <= 1; sel++ {
		for m := uint8(0); m <= 2; m++ {
			// DANE-EE: leaf matches, no name or expiry checks (self is expired, wrong name).
			if err := verifyDANE(self, host, []tlsaRecord{rec(3, sel, m, pki.selfLeaf)}); err != nil {
				t.Errorf("DANE-EE %d %d: %v", sel, m, err)
			}
			// DANE-TA: CA in chain issues a leaf valid for host.
			if err := verifyDANE(chain, host, []tlsaRecord{rec(2, sel, m, pki.ca)}); err != nil {
				t.Errorf("DANE-TA %d %d: %v", sel, m, err)
			}
		}
	}
	// DANE-TA requires the host name.
	if err := verifyDANE(chain, "other.example.test", []tlsaRecord{rec(2, 1, 1, pki.ca)}); err == nil {
		t.Error("DANE-TA accepted wrong host")
	}
	// Non-matching data.
	if err := verifyDANE(chain, host, []tlsaRecord{rec(3, 1, 1, pki.selfLeaf)}); err == nil {
		t.Error("mismatching DANE-EE accepted")
	}
	// PKIX usages (0/1) are not used for SMTP.
	if err := verifyDANE(chain, host, []tlsaRecord{rec(1, 0, 1, pki.leaf)}); err == nil {
		t.Error("PKIX-EE record accepted")
	}
	// One matching record in a set is enough.
	if err := verifyDANE(chain, host, []tlsaRecord{rec(3, 1, 1, pki.selfLeaf), rec(3, 1, 2, pki.leaf)}); err != nil {
		t.Errorf("record set: %v", err)
	}
}

// ---- DNS ----

func fakeDNS(answers map[string]*dns.Msg) func(context.Context, *dns.Msg, string) (*dns.Msg, error) {
	return func(_ context.Context, m *dns.Msg, _ string) (*dns.Msg, error) {
		q := m.Question[0]
		if opt := m.IsEdns0(); opt == nil || !opt.Do() {
			return nil, errors.New("DO bit missing")
		}
		key := strings.ToLower(q.Name) + "/" + dns.TypeToString[q.Qtype]
		a, ok := answers[key]
		if !ok {
			r := new(dns.Msg)
			r.SetRcode(m, dns.RcodeNameError)
			return r, nil
		}
		r := a.Copy()
		r.SetReply(m)
		r.Rcode = a.Rcode
		r.AuthenticatedData = a.AuthenticatedData
		return r, nil
	}
}

func answer(ad bool, rcode int, rrs ...string) *dns.Msg {
	m := new(dns.Msg)
	m.Rcode = rcode
	m.AuthenticatedData = ad
	for _, s := range rrs {
		rr, err := dns.NewRR(s)
		if err != nil {
			panic(err)
		}
		m.Answer = append(m.Answer, rr)
	}
	return m
}

func TestDANELookup(t *testing.T) {
	d := &DANE{Servers: []string{"127.0.0.1:53"}, Exchange: fakeDNS(map[string]*dns.Msg{
		"secure.test./MX":   answer(true, dns.RcodeSuccess, "secure.test. 300 IN MX 10 mx.secure.test."),
		"insecure.test./MX": answer(false, dns.RcodeSuccess, "insecure.test. 300 IN MX 10 mx.insecure.test."),
		"_25._tcp.mx.secure.test./TLSA": answer(true, dns.RcodeSuccess,
			"_25._tcp.mx.secure.test. 300 IN TLSA 3 1 1 0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"),
		"_25._tcp.mx.insecure.test./TLSA": answer(false, dns.RcodeSuccess,
			"_25._tcp.mx.insecure.test. 300 IN TLSA 3 1 1 0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"),
		"_25._tcp.mx.broken.test./TLSA": answer(false, dns.RcodeServerFailure),
	})}
	ctx := context.Background()
	if !d.MXSecure(ctx, "secure.test") || d.MXSecure(ctx, "insecure.test") || d.MXSecure(ctx, "missing.test") {
		t.Fatal("MXSecure wrong")
	}
	recs, secure, err := d.TLSA(ctx, "mx.secure.test")
	if err != nil || !secure || len(recs) != 1 || recs[0].Usage != 3 || len(recs[0].Data) != 32 {
		t.Fatalf("secure TLSA = %+v %v %v", recs, secure, err)
	}
	recs, secure, err = d.TLSA(ctx, "mx.insecure.test")
	if err != nil || secure || recs != nil {
		t.Fatalf("records without AD must be ignored: %+v %v %v", recs, secure, err)
	}
	if _, _, err := d.TLSA(ctx, "mx.broken.test"); err == nil {
		t.Fatal("SERVFAIL must be an error")
	}
	// NXDOMAIN without AD: simply no DANE.
	if recs, secure, err := d.TLSA(ctx, "mx.none.test"); err != nil || secure || recs != nil {
		t.Fatalf("nxdomain = %v %v %v", recs, secure, err)
	}
}

func TestHostPolicyPrecedence(t *testing.T) {
	const host = "mx.secure.test"
	q := &Queue{DANE: &DANE{Servers: []string{"x"}, Exchange: fakeDNS(map[string]*dns.Msg{
		"_25._tcp.mx.secure.test./TLSA": answer(true, dns.RcodeSuccess,
			"_25._tcp.mx.secure.test. 300 IN TLSA 3 1 1 0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"),
	})}}
	ctx := context.Background()
	enforce := &STSPolicy{Mode: "enforce", MX: []string{"mx.other.test"}}
	// DANE wins over a (non-matching) MTA-STS policy.
	tp, err := q.hostPolicy(ctx, domainTLS{domain: "secure.test", mxSecure: true, sts: enforce}, host)
	if err != nil || tp.label != "dane" || !tp.required || tp.config.VerifyConnection == nil {
		t.Fatalf("dane policy = %+v, %v", tp, err)
	}
	// Without a secure MX RRset DANE is skipped and MTA-STS enforce applies.
	if _, err := q.hostPolicy(ctx, domainTLS{domain: "secure.test", sts: enforce}, host); err == nil {
		t.Fatal("enforce must reject an MX outside the policy")
	}
	tp, err = q.hostPolicy(ctx, domainTLS{domain: "secure.test", sts: &STSPolicy{Mode: "enforce", MX: []string{"*.secure.test"}}}, host)
	if err != nil || tp.label != "mta-sts" || !tp.required || tp.config.InsecureSkipVerify || tp.config.ServerName != host {
		t.Fatalf("mta-sts policy = %+v, %v", tp, err)
	}
	tp, _ = q.hostPolicy(ctx, domainTLS{domain: "secure.test", sts: &STSPolicy{Mode: "testing", MX: []string{"x"}}}, host)
	if tp.label != "mta-sts-testing" || tp.required {
		t.Fatalf("testing policy = %+v", tp)
	}
	tp, _ = q.hostPolicy(ctx, domainTLS{domain: "plain.test"}, "mx.plain.test")
	if tp.label != "opportunistic" || tp.required || !tp.config.InsecureSkipVerify {
		t.Fatalf("default policy = %+v", tp)
	}
	// Only unusable TLSA records: TLS stays mandatory, without authentication.
	tp = daneTLS(host, []tlsaRecord{{Usage: 1, Selector: 0, Matching: 1, Data: []byte{1}}})
	if !tp.required || tp.config.VerifyConnection != nil {
		t.Fatalf("unusable TLSA set = %+v", tp)
	}
}
