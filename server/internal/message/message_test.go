package message

import (
	"strings"
	"testing"

	"postwerk/internal/store"
)

func TestBuildParseRoundtrip(t *testing.T) {
	raw := Build(Draft{
		From:      store.Address{Name: "Jörg Müller", Email: "joerg@example.test"},
		To:        []store.Address{{Email: "anna@example.test"}},
		Subject:   "Nebenkostenabrechnung 2025 – Rückfrage",
		Text:      "Hallo Anna,\nanbei die Abrechnung.\n\nGrüße",
		HTML:      "<p>Hallo Anna,</p><p>anbei die Abrechnung.</p>",
		MessageID: "id1@example.test",
		InReplyTo: "orig@example.test",
		Attach:    []OutAttachment{{Filename: "abrechnung.pdf", ContentType: "application/pdf", Data: []byte("%PDF-1.4 test")}},
	})
	p, err := Parse(raw, true)
	if err != nil {
		t.Fatal(err)
	}
	if p.Subject != "Nebenkostenabrechnung 2025 – Rückfrage" {
		t.Errorf("subject = %q", p.Subject)
	}
	if p.From.Name != "Jörg Müller" || p.From.Email != "joerg@example.test" {
		t.Errorf("from = %+v", p.From)
	}
	if !strings.Contains(p.Text, "anbei die Abrechnung.") || !strings.Contains(p.HTML, "<p>Hallo Anna,</p>") {
		t.Errorf("bodies: text=%q html=%q", p.Text, p.HTML)
	}
	if p.MessageID != "id1@example.test" || p.InReplyTo != "orig@example.test" {
		t.Errorf("ids: %q %q", p.MessageID, p.InReplyTo)
	}
	if len(p.Attachments) != 1 || p.Attachments[0].Filename != "abrechnung.pdf" || string(p.Attachments[0].Data) != "%PDF-1.4 test" {
		t.Fatalf("attachments: %+v", p.Attachments)
	}
}

func TestHTMLToText(t *testing.T) {
	got := HTMLToText("<html><head><style>p{}</style></head><body><p>Hallo&nbsp;Welt</p><script>x()</script><div>Zeile 2</div></body></html>")
	if !strings.Contains(got, "Hallo Welt") || !strings.Contains(got, "Zeile 2") || strings.Contains(got, "x()") {
		t.Fatalf("got %q", got)
	}
}
