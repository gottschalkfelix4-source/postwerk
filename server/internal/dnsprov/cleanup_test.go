package dnsprov

import (
	"context"
	"testing"
)

func TestPlanMTASTSIDFollowsPolicy(t *testing.T) {
	m := &memProvider{zone: "example.com", recs: []Record{
		{ID: "1", Name: "_mta-sts", Type: "TXT", Value: "v=STSv1; id=20240101"},
	}, next: 10}
	info := Info{ID: "mem", Name: "Mem", Supports: allTypes}
	ctx := context.Background()

	// Without a policy id the existing record is kept (legacy behaviour).
	plan, err := MakePlan(ctx, m, info, "example.com", desired("203.0.113.10"))
	if err != nil {
		t.Fatal(err)
	}
	if c := find1(t, plan, "mta-sts-txt"); c.Action != "unchanged" {
		t.Fatalf("legacy = %+v", c)
	}

	want := DesiredRecords("example.com", "mail.example.com", "pw1", longDKIM, "203.0.113.10", "", Options{MTASTSID: "abc123"})
	plan, err = MakePlan(ctx, m, info, "example.com", want)
	if err != nil {
		t.Fatal(err)
	}
	c := find1(t, plan, "mta-sts-txt")
	if c.Action != "update" || c.Desired != "v=STSv1; id=abc123" {
		t.Fatalf("changed policy = %+v", c)
	}
	if res := ApplyPlan(ctx, m, plan, []string{"mta-sts-txt"}); len(res.Failed) != 0 {
		t.Fatal(res.Failed)
	}
	plan, _ = MakePlan(ctx, m, info, "example.com", want)
	if c := find1(t, plan, "mta-sts-txt"); c.Action != "unchanged" {
		t.Fatalf("after apply = %+v", c)
	}
}

func TestPlanRemovesDeletedDKIMRecord(t *testing.T) {
	m := &memProvider{zone: "example.com", recs: []Record{
		{ID: "1", Name: "pw1._domainkey", Type: "TXT", Value: "v=DKIM1; k=rsa; p=OLD"},
		{ID: "2", Name: "pw1._domainkey", Type: "TXT", Value: "unrelated"},
	}, next: 10}
	info := Info{ID: "mem", Name: "Mem", Supports: allTypes}
	ctx := context.Background()
	want := DesiredRecords("example.com", "mail.example.com", "pw2", longDKIM, "203.0.113.10", "", Options{RemoveDKIM: []string{"pw1", "pw0"}})
	plan, err := MakePlan(ctx, m, info, "example.com", want)
	if err != nil {
		t.Fatal(err)
	}
	c := find1(t, plan, RemoveDKIMKey("pw1"))
	if c.Action != "delete" || len(c.Current) != 1 || c.Name != "pw1._domainkey.example.com." {
		t.Fatalf("removal = %+v", c)
	}
	for _, ch := range plan.Changes {
		if ch.Key == RemoveDKIMKey("pw0") {
			t.Fatal("absent record must not be planned")
		}
	}
	res := ApplyPlan(ctx, m, plan, []string{RemoveDKIMKey("pw1")})
	if len(res.Applied) != 1 || res.Applied[0] != RemoveDKIMKey("pw1") {
		t.Fatalf("apply = %+v", res)
	}
	if len(m.recs) != 1 || m.recs[0].Value != "unrelated" {
		t.Fatalf("records = %+v", m.recs)
	}
}
