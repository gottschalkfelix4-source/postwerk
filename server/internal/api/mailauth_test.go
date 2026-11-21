package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"postwerk/internal/store"
)

// req builds a handler request with a JSON body and path values.
func req(t *testing.T, method string, body any, path map[string]string) (*httptest.ResponseRecorder, *http.Request) {
	t.Helper()
	var b bytes.Buffer
	if body != nil {
		_ = json.NewEncoder(&b).Encode(body)
	} else {
		b.WriteString("{}")
	}
	r := httptest.NewRequest(method, "/api/test", &b)
	for k, v := range path {
		r.SetPathValue(k, v)
	}
	return httptest.NewRecorder(), r
}

func TestDKIMRotationAPI(t *testing.T) {
	s, _, admin := mailTestServer(t)
	ctx := context.Background()

	w, r := req(t, "POST", map[string]string{"name": "rot.test"}, nil)
	if _, err := s.createDomain(w, r, admin); err != nil {
		t.Fatal(err)
	}
	list := func() []dkimKeyView {
		t.Helper()
		w, r := req(t, "GET", nil, map[string]string{"name": "rot.test"})
		out, err := s.listDKIMKeys(w, r, admin)
		if err != nil {
			t.Fatal(err)
		}
		return out.([]dkimKeyView)
	}
	keys := list()
	if len(keys) != 2 || keys[0].State != store.DKIMActive || keys[0].Algorithm != store.DKIMAlgRSA ||
		keys[1].State != store.DKIMPending || keys[1].Algorithm != store.DKIMAlgEd25519 {
		t.Fatalf("new domain keys: %+v", keys)
	}
	if !strings.HasPrefix(keys[1].DNSValue, "v=DKIM1; k=ed25519; p=") || !strings.HasSuffix(keys[1].DNSName, "._domainkey.rot.test.") {
		t.Fatalf("dns record: %s %s", keys[1].DNSName, keys[1].DNSValue)
	}

	// New RSA key; a second pending RSA key is refused.
	w, r = req(t, "POST", map[string]string{"algorithm": "rsa"}, map[string]string{"name": "rot.test"})
	out, err := s.createDKIMKey(w, r, admin)
	if err != nil {
		t.Fatal(err)
	}
	pending := out.(dkimKeyView)
	if pending.State != store.DKIMPending || pending.Selector == keys[0].Selector {
		t.Fatalf("pending: %+v", pending)
	}
	w, r = req(t, "POST", map[string]string{"algorithm": "rsa"}, map[string]string{"name": "rot.test"})
	var ae *apiError
	if _, err := s.createDKIMKey(w, r, admin); !errors.As(err, &ae) || ae.Status != 409 {
		t.Fatalf("second pending: %v", err)
	}
	w, r = req(t, "POST", map[string]string{"algorithm": "dsa"}, map[string]string{"name": "rot.test"})
	if _, err := s.createDKIMKey(w, r, admin); !errors.As(err, &ae) || ae.Status != 400 {
		t.Fatalf("invalid algorithm: %v", err)
	}

	// The DNS guide lists the pending keys besides the primary record.
	d, _ := s.Store.GetDomain(ctx, "rot.test")
	recs, err := s.dnsRecords(d)
	if err != nil {
		t.Fatal(err)
	}
	found := map[string]dnsRecord{}
	for _, rec := range recs {
		found[rec.Key] = rec
	}
	if found["dkim"].Name != keys[0].DNSName || found["dkim-"+pending.Selector].Required || found["dkim-"+keys[1].Selector].Title == "" {
		t.Fatalf("dns records: %+v", found)
	}
	if recs[4].Key[:5] != "dkim-" {
		t.Fatalf("extra dkim records must follow the primary one: %s", recs[4].Key)
	}

	// Activation (forced, no DNS in tests) switches the primary selector
	// and retires the old RSA key.
	id := strconv.FormatInt(pending.ID, 10)
	w, r = req(t, "POST", map[string]bool{"force": true}, map[string]string{"name": "rot.test", "id": id})
	if _, err := s.activateDKIMKey(w, r, admin); err != nil {
		t.Fatal(err)
	}
	d, _ = s.Store.GetDomain(ctx, "rot.test")
	if d.DKIMSelector != pending.Selector {
		t.Fatalf("primary selector = %s", d.DKIMSelector)
	}
	keys = list()
	var retired dkimKeyView
	for _, k := range keys {
		if k.Selector == found["dkim"].Name[:strings.Index(found["dkim"].Name, ".")] {
			retired = k
		}
	}
	if retired.State != store.DKIMRetired || retired.RemovableAt == nil || retired.RemovableAt.Before(time.Now().Add(6*24*time.Hour)) {
		t.Fatalf("retired key: %+v", retired)
	}
	recs, _ = s.dnsRecords(d)
	for _, rec := range recs {
		if rec.Key == "dkim-"+retired.Selector && (rec.Required || !strings.Contains(rec.Purpose, "veröffentlicht")) {
			t.Fatalf("retired record: %+v", rec)
		}
	}
	// Activating again or deleting the retired key early is refused.
	w, r = req(t, "POST", map[string]bool{"force": true}, map[string]string{"name": "rot.test", "id": id})
	if _, err := s.activateDKIMKey(w, r, admin); !errors.As(err, &ae) || ae.Status != 409 {
		t.Fatalf("activate active: %v", err)
	}
	w, r = req(t, "DELETE", nil, map[string]string{"name": "rot.test", "id": strconv.FormatInt(retired.ID, 10)})
	if _, err := s.deleteDKIMKey(w, r, admin); !errors.As(err, &ae) || ae.Status != 409 {
		t.Fatalf("delete retired: %v", err)
	}
	// Pending keys can be discarded; keys of other domains are not reachable.
	w, r = req(t, "DELETE", nil, map[string]string{"name": "example.test", "id": strconv.FormatInt(keys[len(keys)-1].ID, 10)})
	if _, err := s.deleteDKIMKey(w, r, admin); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("foreign domain: %v", err)
	}
}

func TestValidateMailAuth(t *testing.T) {
	c := store.MailAuthSettings{TrustedARCSealers: []string{" Google.com. ", "google.com", "", "lists.example.org"}, PostmasterAddress: " Ops@Example.TEST "}
	if err := validateMailAuth(&c); err != nil {
		t.Fatal(err)
	}
	if len(c.TrustedARCSealers) != 2 || c.TrustedARCSealers[0] != "google.com" || c.PostmasterAddress != "ops@example.test" {
		t.Fatalf("normalized: %+v", c)
	}
	for _, bad := range []store.MailAuthSettings{
		{TrustedARCSealers: []string{"not a domain"}},
		{PostmasterAddress: "kein-at"},
		{PostmasterAddress: "postmaster@example.test"},
	} {
		if err := validateMailAuth(&bad); err == nil {
			t.Fatalf("accepted %+v", bad)
		}
	}
}

func TestSystemAliasesAndReportsAPI(t *testing.T) {
	s, _, admin := mailTestServer(t)
	ctx := context.Background()
	w, r := req(t, "GET", nil, nil)
	out, err := s.listSystemAliases(w, r, admin)
	if err != nil {
		t.Fatal(err)
	}
	sys := out.([]store.SystemAlias)
	if len(sys) != 2 || sys[0].Targets[0] != "admin@example.test" {
		t.Fatalf("system aliases: %+v", sys)
	}

	end := time.Now().Add(-time.Hour)
	if _, err := s.Store.SaveDMARCReport(ctx, &store.DMARCReport{Domain: "example.test", OrgName: "google.com", ReportID: "1", Begin: end.Add(-24 * time.Hour), End: end,
		Records: []store.DMARCRecord{{SourceIP: "192.0.2.1", Count: 2, DKIM: "fail", SPF: "fail"}}}); err != nil {
		t.Fatal(err)
	}
	r = httptest.NewRequest("GET", "/api/admin/reports/dmarc?domain=example.test&days=7", nil)
	out, err = s.dmarcReports(httptest.NewRecorder(), r, admin)
	if err != nil {
		t.Fatal(err)
	}
	m := out.(map[string]any)
	if sum := m["summary"].([]store.DMARCDomainSummary); len(sum) != 1 || sum[0].Failed != 2 {
		t.Fatalf("summary: %+v", m["summary"])
	}
	r = httptest.NewRequest("GET", "/api/admin/reports/tls?days=7", nil)
	out, err = s.tlsReports(httptest.NewRecorder(), r, admin)
	if err != nil {
		t.Fatal(err)
	}
	if sum := out.(map[string]any)["summary"].([]store.TLSDomainSummary); len(sum) != 0 {
		t.Fatalf("tls summary: %+v", sum)
	}
}
