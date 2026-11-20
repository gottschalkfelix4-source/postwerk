package message

import (
	"testing"

	"postwerk/internal/store"
)

func TestUmlautBody(t *testing.T) {
	raw := Build(Draft{From: store.Address{Email: "a@b.test"}, To: []store.Address{{Email: "c@d.test"}}, Subject: "x", Text: "Grüße aus Köln – Felix", MessageID: "1@b.test"})
	p, err := Parse(raw, false)
	if err != nil {
		t.Fatal(err)
	}
	if p.Text != "Grüße aus Köln – Felix\r\n" && p.Text != "Grüße aus Köln – Felix" {
		t.Fatalf("text = %q\nraw:\n%s", p.Text, raw)
	}
}
