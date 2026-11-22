package smtp

import "testing"

func TestValidDomainAndAddress(t *testing.T) {
	for _, d := range []string{"mail.example.org", "my_pc", "[192.0.2.1]", "[IPv6:2001:db8::1]", "bücher.example"} {
		if !ValidDomain(d) {
			t.Errorf("rejected %q", d)
		}
	}
	for _, d := range []string{"", "evil.com;dmarc=pass", "a b", "x\rReceived: y", "[1.2.3.4;x]", "foo(bar)"} {
		if ValidDomain(d) {
			t.Errorf("accepted %q", d)
		}
	}
	if validAddress("a@evil.com;dmarc=pass") {
		t.Error("envelope domain with ';' accepted")
	}
	if !validAddress("a+tag@example.org") || !validAddress("postmaster") {
		t.Error("normal address rejected")
	}
}
