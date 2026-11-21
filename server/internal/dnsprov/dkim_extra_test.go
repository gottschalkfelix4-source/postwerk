package dnsprov

import "testing"

func TestDesiredRecordsExtraDKIM(t *testing.T) {
	d := DesiredRecords("example.test", "mail.example.test", "pw1", "v=DKIM1; k=rsa; p=AAA", "192.0.2.1", "", Options{
		ExtraDKIM: []DKIMRecord{
			{Selector: "pw1e", Value: "v=DKIM1; k=ed25519; p=BBB", Purpose: "Ed25519", Required: true},
			{Selector: "pw2", Value: "v=DKIM1; k=rsa; p=CCC", Purpose: "neu"},
		},
	})
	byKey := map[string]Desired{}
	for _, r := range d {
		byKey[r.Key] = r
	}
	if r := byKey["dkim"]; r.Record.Name != "pw1._domainkey.example.test" {
		t.Fatalf("primary: %+v", r)
	}
	ed := byKey["dkim-pw1e"]
	if ed.Record.Name != "pw1e._domainkey.example.test" || ed.Record.Value != "v=DKIM1; k=ed25519; p=BBB" || !ed.Required || ed.Match != "v=DKIM1" {
		t.Fatalf("ed25519: %+v", ed)
	}
	if p := byKey["dkim-pw2"]; p.Required || p.Record.Type != "TXT" {
		t.Fatalf("pending: %+v", p)
	}
}
