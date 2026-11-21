package api

import (
	"context"
	"errors"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"postwerk/internal/store"
)

func TestMTASTSPolicyModes(t *testing.T) {
	s, _, admin := mailTestServer(t)
	ctx := context.Background()
	w, r := req(t, "POST", map[string]string{"name": "sts.test"}, nil)
	if _, err := s.createDomain(w, r, admin); err != nil {
		t.Fatal(err)
	}
	if got := s.Settings.Get().MailAuth.MTASTS; got.Mode != "testing" || got.MaxAge != 604800 {
		t.Fatalf("defaults = %+v", got)
	}
	serve := func() string {
		t.Helper()
		rec := httptest.NewRecorder()
		s.mtaSTS(rec, httptest.NewRequest("GET", "https://mta-sts.sts.test/.well-known/mta-sts.txt", nil))
		if rec.Code != 200 {
			t.Fatalf("status %d", rec.Code)
		}
		return rec.Body.String()
	}
	txtID := func() string {
		t.Helper()
		d, err := s.Store.GetDomain(ctx, "sts.test")
		if err != nil {
			t.Fatal(err)
		}
		recs, err := s.dnsRecords(d)
		if err != nil {
			t.Fatal(err)
		}
		for _, rr := range recs {
			if rr.Key == "mta-sts-txt" {
				return strings.TrimPrefix(rr.Value, "v=STSv1; id=")
			}
		}
		t.Fatal("no mta-sts-txt record")
		return ""
	}

	ids := map[string]string{}
	for _, tc := range []struct {
		mode   string
		maxAge int
	}{{"none", 86400}, {"testing", 604800}, {"enforce", 1209600}} {
		cur := s.Settings.Get()
		cur.MailAuth.MTASTS = store.MTASTSSettings{Mode: tc.mode, MaxAge: tc.maxAge}
		if err := validateMTASTS(&cur.MailAuth.MTASTS); err != nil {
			t.Fatal(err)
		}
		if err := s.Settings.Save(ctx, cur); err != nil {
			t.Fatal(err)
		}
		host := cur.Server.Hostname
		want := "version: STSv1\r\nmode: " + tc.mode + "\r\nmx: " + host + "\r\nmax_age: " + strconv.Itoa(tc.maxAge) + "\r\n"
		if got := serve(); got != want {
			t.Fatalf("%s policy = %q, want %q", tc.mode, got, want)
		}
		id := txtID()
		if id == "" || len(id) > 32 || id != s.mtaSTSID(cur) {
			t.Fatalf("%s id = %q", tc.mode, id)
		}
		for m, other := range ids {
			if other == id {
				t.Fatalf("id of %s equals id of %s", tc.mode, m)
			}
		}
		ids[tc.mode] = id
	}
	// The same policy keeps its id.
	cur := s.Settings.Get()
	if s.mtaSTSID(cur) != ids["enforce"] {
		t.Fatal("id must be stable for an unchanged policy")
	}
	// A different MX host changes the policy and so its id.
	cur.Server.Hostname = "mx2.example.test"
	if s.mtaSTSID(cur) == ids["enforce"] {
		t.Fatal("id must change with the MX host")
	}
}

func TestValidateMTASTS(t *testing.T) {
	c := store.MTASTSSettings{}
	if err := validateMTASTS(&c); err != nil || c.Mode != "testing" || c.MaxAge != 604800 {
		t.Fatalf("empty = %+v, %v", c, err)
	}
	c = store.MTASTSSettings{Mode: " Enforce ", MaxAge: 86400}
	if err := validateMTASTS(&c); err != nil || c.Mode != "enforce" {
		t.Fatalf("enforce = %+v, %v", c, err)
	}
	for _, bad := range []store.MTASTSSettings{{Mode: "strict", MaxAge: 86400}, {Mode: "testing", MaxAge: 60}, {Mode: "testing", MaxAge: 40000000}} {
		b := bad
		var ae *apiError
		if err := validateMTASTS(&b); !errors.As(err, &ae) {
			t.Fatalf("%+v accepted: %v", bad, err)
		}
	}
}

func TestMTASTSStatus(t *testing.T) {
	s, _, admin := mailTestServer(t)
	orig := mtaSTSProbe
	t.Cleanup(func() { mtaSTSProbe = orig })
	mtaSTSProbe = func(ctx context.Context, domain string) error { return nil }

	w, r := req(t, "GET", nil, nil)
	out, err := s.mtaSTSStatusHandler(w, r, admin)
	if err != nil {
		t.Fatal(err)
	}
	st := out.(mtaSTSStatus)
	if st.Mode != "testing" || !strings.Contains(st.Policy, "mode: testing") || st.ID == "" {
		t.Fatalf("status = %+v", st)
	}
	if len(st.Checks) != 2 || st.Checks[0].Purpose != "mx" || st.Checks[1].Host != "mta-sts.example.test" || !st.Checks[1].OK {
		t.Fatalf("checks = %+v", st.Checks)
	}
	// Without certificate management there is no trusted certificate for
	// the MX host, so enforce is not ready.
	if st.Checks[0].OK || st.EnforceReady {
		t.Fatalf("mx check = %+v ready=%v", st.Checks[0], st.EnforceReady)
	}
}
