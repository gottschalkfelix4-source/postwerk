package mailauth

import (
	"strings"
	"testing"
)

func TestResultsStringNoInjection(t *testing.T) {
	r := Result{SPF: "none", SPFDomain: "evil.com;dmarc=pass", DKIM: "none", DMARC: "fail", FromDomain: "paypal.com"}
	s := r.ResultsString()
	if strings.Contains(s, "dmarc=pass") || !strings.Contains(s, "smtp.mailfrom=invalid") {
		t.Fatalf("results = %q", s)
	}
	if aarVouchesFor("i=1; mx.example; dmarc=pass", "paypal.com") {
		t.Fatal("dmarc=pass without header.from vouched")
	}
	if !aarVouchesFor("i=1; mx.example; dmarc=pass header.from=paypal.com", "paypal.com") {
		t.Fatal("aligned dmarc=pass not accepted")
	}
}
