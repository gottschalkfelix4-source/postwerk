package spam

import (
	"context"
	"net"
	"sync"
	"testing"
)

func TestReverseIP(t *testing.T) {
	for in, want := range map[string]string{
		"192.0.2.99":                "99.2.0.192",
		"::ffff:192.0.2.1":          "1.2.0.192",
		"2001:db8::1":               "1.0.0.0.0.0.0.0.0.0.0.0.0.0.0.0.0.0.0.0.0.0.0.0.8.b.d.0.1.0.0.2",
		"2001:db8:1:2:3:4:567:89ab": "b.a.9.8.7.6.5.0.4.0.0.0.3.0.0.0.2.0.0.0.1.0.0.0.8.b.d.0.1.0.0.2",
	} {
		if got := ReverseIP(net.ParseIP(in)); got != want {
			t.Errorf("ReverseIP(%s) = %s, want %s", in, got, want)
		}
	}
	if got := ReverseIP(nil); got != "" {
		t.Errorf("ReverseIP(nil) = %q", got)
	}
}

func TestDNSBLListed(t *testing.T) {
	cases := map[string]bool{
		"127.0.0.2":       true,
		"127.0.0.11":      true,
		"127.255.255.254": false, // Spamhaus: query via public resolver refused
		"127.255.255.252": false,
		"192.0.2.1":       false,
		"::1":             false,
	}
	for a, want := range cases {
		if got := DNSBLListed([]string{a}); got != want {
			t.Errorf("DNSBLListed(%s) = %v", a, got)
		}
	}
	if DNSBLListed(nil) {
		t.Error("empty answer must not count as listed")
	}
}

func TestCheckDNSBLIPv6(t *testing.T) {
	const rev = "1.0.0.0.0.0.0.0.0.0.0.0.0.0.0.0.0.0.0.0.0.0.0.0.8.b.d.0.1.0.0.2"
	var mu sync.Mutex
	var queried []string
	old := lookupHost
	t.Cleanup(func() { lookupHost = old })
	lookupHost = func(_ context.Context, name string) ([]string, error) {
		mu.Lock()
		queried = append(queried, name)
		mu.Unlock()
		switch name {
		case rev + ".listed.test":
			return []string{"127.0.0.2"}, nil
		case rev + ".refused.test":
			return []string{"127.255.255.254"}, nil
		}
		return nil, &net.DNSError{Err: "no such host", Name: name, IsNotFound: true}
	}
	hits := checkDNSBL(context.Background(), net.ParseIP("2001:db8::1"), []string{"listed.test", "refused.test", "nov6.test", " "})
	if len(hits) != 1 || hits[0] != "listed.test" {
		t.Fatalf("hits = %v", hits)
	}
	if len(queried) != 3 {
		t.Errorf("queries = %v", queried)
	}

	// IPv4 keeps working as before.
	lookupHost = func(_ context.Context, name string) ([]string, error) {
		if name == "10.2.0.192.listed.test" {
			return []string{"127.0.0.4"}, nil
		}
		return nil, &net.DNSError{IsNotFound: true}
	}
	if hits := checkDNSBL(context.Background(), net.ParseIP("192.0.2.10"), []string{"listed.test", "other.test"}); len(hits) != 1 {
		t.Fatalf("v4 hits = %v", hits)
	}
}
