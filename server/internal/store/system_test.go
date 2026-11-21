package store

import (
	"context"
	"testing"
	"time"
)

func TestPostmasterRouting(t *testing.T) {
	st := openTest(t)
	ctx := context.Background()
	settings, err := st.LoadSettings(ctx, "mail.example.test")
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range []string{"example.test", "second.test"} {
		if err := st.CreateDomain(ctx, d, "pw1", ""); err != nil {
			t.Fatal(err)
		}
	}
	resolve := func(addr string) ([]string, []string) {
		t.Helper()
		l, e, err := st.ResolveRecipient(ctx, addr)
		if err != nil {
			t.Fatal(err)
		}
		return l, e
	}
	// Without any admin there is nowhere to route to.
	if l, e := resolve("postmaster@example.test"); len(l)+len(e) != 0 {
		t.Fatalf("no admin: %v %v", l, e)
	}
	if err := st.CreateAccount(ctx, &Account{Email: "user@example.test", PasswordHash: "x", Role: "user"}); err != nil {
		t.Fatal(err)
	}
	if err := st.CreateAccount(ctx, &Account{Email: "chef@second.test", PasswordHash: "x", Role: "admin"}); err != nil {
		t.Fatal(err)
	}
	for _, a := range []string{"postmaster@example.test", "ABUSE@example.test", "postmaster@second.test"} {
		if l, _ := resolve(a); len(l) != 1 || l[0] != "chef@second.test" {
			t.Fatalf("%s -> %v", a, l)
		}
	}
	// Other unknown addresses are still unknown; foreign domains stay external.
	if l, e := resolve("nobody@example.test"); len(l)+len(e) != 0 {
		t.Fatalf("unknown address: %v %v", l, e)
	}
	if _, e := resolve("postmaster@foreign.test"); len(e) != 1 {
		t.Fatalf("foreign postmaster: %v", e)
	}

	// An explicit alias wins over the implicit one.
	if _, err := st.CreateAlias(ctx, "postmaster@example.test", []string{"user@example.test"}); err != nil {
		t.Fatal(err)
	}
	if l, _ := resolve("postmaster@example.test"); len(l) != 1 || l[0] != "user@example.test" {
		t.Fatalf("explicit alias: %v", l)
	}
	sys, err := st.ListSystemAliases(ctx)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]bool{"abuse@example.test": true, "postmaster@second.test": true, "abuse@second.test": true}
	if len(sys) != 3 {
		t.Fatalf("system aliases: %+v", sys)
	}
	for _, a := range sys {
		if !want[a.Address] || !a.System || len(a.Targets) != 1 || a.Targets[0] != "chef@second.test" || a.Configured {
			t.Fatalf("system alias: %+v", a)
		}
	}

	// A configured address takes precedence over the first admin.
	cur := settings.Get()
	cur.MailAuth.PostmasterAddress = "Ops@External.test"
	if err := settings.Save(ctx, cur); err != nil {
		t.Fatal(err)
	}
	if l, e := resolve("abuse@second.test"); len(l) != 0 || len(e) != 1 || e[0] != "ops@external.test" {
		t.Fatalf("configured target: %v %v", l, e)
	}
	sys, _ = st.ListSystemAliases(ctx)
	if !sys[0].Configured || sys[0].Targets[0] != "ops@external.test" {
		t.Fatalf("configured system alias: %+v", sys[0])
	}
	// A disabled admin is skipped.
	cur.MailAuth.PostmasterAddress = ""
	_ = settings.Save(ctx, cur)
	chef, _ := st.GetAccountByEmail(ctx, "chef@second.test")
	disabled := true
	_ = st.UpdateAccount(ctx, chef.ID, AccountPatch{Disabled: &disabled})
	if l, e := resolve("abuse@second.test"); len(l)+len(e) != 0 {
		t.Fatalf("disabled admin: %v %v", l, e)
	}
}

func TestReportStore(t *testing.T) {
	st := openTest(t)
	ctx := context.Background()
	day := time.Now().Add(-24 * time.Hour).UTC().Truncate(time.Second)
	rep := &DMARCReport{
		Domain: "example.test", OrgName: "google.com", ReportID: "r1", Begin: day.Add(-24 * time.Hour), End: day,
		Policy: DMARCPolicyPublished{Domain: "example.test", P: "quarantine"},
		Records: []DMARCRecord{
			{SourceIP: "203.0.113.10", Count: 40, DKIM: "pass", SPF: "pass", Disposition: "none", HeaderFrom: "example.test"},
			{SourceIP: "198.51.100.7", Count: 3, DKIM: "fail", SPF: "fail", Disposition: "quarantine", HeaderFrom: "example.test",
				SPFResults: []DMARCAuthResult{{Domain: "spoofer.test", Result: "softfail"}}},
		},
	}
	if ok, err := st.SaveDMARCReport(ctx, rep); err != nil || !ok {
		t.Fatalf("save: %v %v", ok, err)
	}
	if rep.Messages != 43 || rep.Failed != 3 {
		t.Fatalf("totals: %d %d", rep.Messages, rep.Failed)
	}
	// The same report again (e.g. resent) is ignored.
	dup := *rep
	if ok, err := st.SaveDMARCReport(ctx, &dup); err != nil || ok {
		t.Fatalf("duplicate: %v %v", ok, err)
	}
	rep2 := &DMARCReport{Domain: "example.test", OrgName: "Mail.Ru, LLC", ReportID: "r2", Begin: day.Add(-24 * time.Hour), End: day,
		Records: []DMARCRecord{{SourceIP: "198.51.100.7", Count: 2, DKIM: "fail", SPF: "fail", Disposition: "reject"}}}
	if _, err := st.SaveDMARCReport(ctx, rep2); err != nil {
		t.Fatal(err)
	}
	f := ReportFilter{Since: time.Now().Add(-30 * 24 * time.Hour)}
	sum, err := st.DMARCSummary(ctx, f)
	if err != nil || len(sum) != 1 || sum[0].Reports != 2 || sum[0].Messages != 45 || sum[0].Failed != 5 || sum[0].Passed != 40 {
		t.Fatalf("summary: %+v %v", sum, err)
	}
	if len(sum[0].Reporters) != 2 || sum[0].Reporters[0] != "Mail.Ru, LLC" {
		t.Fatalf("reporters: %q", sum[0].Reporters)
	}
	src, err := st.DMARCSources(ctx, f, 100)
	if err != nil || len(src) != 2 {
		t.Fatalf("sources: %+v %v", src, err)
	}
	// Failing sources come first.
	if src[0].SourceIP != "198.51.100.7" || src[0].Count != 5 || src[0].DMARCPass != 0 || src[0].Quarantined != 3 || src[0].Rejected != 2 || len(src[0].Reporters) != 2 {
		t.Fatalf("failing source: %+v", src[0])
	}
	if src[1].DMARCPass != 40 || src[1].SPFPass != 40 {
		t.Fatalf("passing source: %+v", src[1])
	}
	// Filters by domain and period.
	if s, _ := st.DMARCSummary(ctx, ReportFilter{Domain: "other.test", Since: f.Since}); len(s) != 0 {
		t.Fatalf("domain filter: %+v", s)
	}
	if s, _ := st.DMARCSummary(ctx, ReportFilter{Since: time.Now()}); len(s) != 0 {
		t.Fatalf("period filter: %+v", s)
	}
	list, _ := st.ListDMARCReports(ctx, f, 10)
	if len(list) != 2 {
		t.Fatalf("list: %+v", list)
	}
	full, err := st.GetDMARCReport(ctx, rep.ID)
	if err != nil || len(full.Records) != 2 || full.Records[1].SPFResults[0].Domain != "spoofer.test" || full.Policy.P != "quarantine" {
		t.Fatalf("detail: %+v %v", full, err)
	}
	if err := st.DeleteDMARCReport(ctx, rep.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := st.GetDMARCReport(ctx, rep.ID); err != ErrNotFound {
		t.Fatalf("deleted: %v", err)
	}

	tls := &TLSReport{Domain: "example.test", OrgName: "Company-X", ReportID: "t1", Begin: day.Add(-24 * time.Hour), End: day,
		PolicyType: "sts", SuccessCount: 100, FailureCount: 7,
		Failures: []TLSFailure{
			{ResultType: "certificate-expired", ReceivingMXHostname: "mail.example.test", SendingMTAIP: "192.0.2.1", FailedSessionCount: 5},
			{ResultType: "certificate-expired", ReceivingMXHostname: "MAIL.example.test", SendingMTAIP: "192.0.2.2", FailedSessionCount: 1},
			{ResultType: "starttls-not-supported", ReceivingMXHostname: "mail.example.test", FailedSessionCount: 1},
		}}
	if ok, err := st.SaveTLSReport(ctx, tls); err != nil || !ok {
		t.Fatalf("save tls: %v %v", ok, err)
	}
	if ok, _ := st.SaveTLSReport(ctx, tls); ok {
		t.Fatal("duplicate tls report stored")
	}
	ts, _ := st.TLSSummary(ctx, f)
	if len(ts) != 1 || ts[0].Success != 100 || ts[0].Failure != 7 || ts[0].Reports != 1 {
		t.Fatalf("tls summary: %+v", ts)
	}
	groups, _ := st.TLSFailures(ctx, f)
	if len(groups) != 2 || groups[0].ResultType != "certificate-expired" || groups[0].Sessions != 6 || len(groups[0].SendingIPs) != 2 {
		t.Fatalf("tls failures: %+v", groups)
	}
	reps, _ := st.ListTLSReports(ctx, f, 10)
	if len(reps) != 1 || len(reps[0].Failures) != 3 {
		t.Fatalf("tls list: %+v", reps)
	}
	if err := st.DeleteTLSReport(ctx, reps[0].ID); err != nil {
		t.Fatal(err)
	}
}
