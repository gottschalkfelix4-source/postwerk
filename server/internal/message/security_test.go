package message

import (
	"errors"
	"strings"
	"testing"

	"postwerk/internal/store"
)

func headerSection(raw []byte) string {
	s := string(raw)
	if i := strings.Index(s, "\r\n\r\n"); i >= 0 {
		return s[:i+2]
	}
	return s
}

func TestBuildRejectsHeaderInjection(t *testing.T) {
	raw := Build(Draft{
		From:      store.Address{Name: "Eve\r\nBcc: victim@evil.test", Email: "eve@example.test"},
		To:        []store.Address{{Name: "Bob, <x@evil.test>", Email: "bob@example.test"}},
		Subject:   "Hallo\r\nX-Injected: 1",
		MessageID: "id\r\nX-Injected: 2@example.test",
		InReplyTo: "orig@example.test>\r\nX-Injected: 3",
		References: []string{
			"a@example.test",
			"b@x>\nX-Injected: 4",
		},
		Extra: map[string]string{
			"Auto-Submitted": "auto-replied\r\nX-Injected: 5",
			"Bad\r\nName":    "x",
			"X-Null":         "a\x00b",
			"Precedence":     "bulk",
		},
		Text: "Body",
	})
	hdr := headerSection(raw)
	for _, line := range strings.Split(hdr, "\r\n") {
		if strings.HasPrefix(line, "X-Injected") || strings.HasPrefix(line, "Bcc:") || strings.HasPrefix(line, "Name:") {
			t.Fatalf("header injection succeeded: %q\n%s", line, hdr)
		}
	}
	if strings.ContainsRune(hdr, 0) {
		t.Fatal("NUL byte in header")
	}
	if strings.Count(hdr, "\n") != strings.Count(hdr, "\r\n") {
		t.Fatal("bare LF in header")
	}
	p, err := Parse(raw, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(p.To) != 1 || p.To[0].Email != "bob@example.test" {
		t.Fatalf("display name injected extra recipients: %+v", p.To)
	}
	if p.From.Email != "eve@example.test" {
		t.Fatalf("from = %+v", p.From)
	}
	if strings.ContainsAny(p.MessageID, "\r\n :<>") || strings.ContainsAny(p.InReplyTo, "\r\n :<>") {
		t.Fatalf("ids not sanitized: %q %q", p.MessageID, p.InReplyTo)
	}
}

func TestCleanMessageID(t *testing.T) {
	cases := map[string]string{
		"<abc.def@example.test>":           "abc.def@example.test",
		"  <a+b=c@[127.0.0.1]>  ":          "a+b=c@[127.0.0.1]",
		"<x@y>\r\nBcc: evil@evil.test":     "x@y",
		"evil\r\nX-Header: 1@example.test": "evilX-Header1@example.test",
		"<\"quoted id\"@example.test>":     "quotedid@example.test",
		"":                                 "",
	}
	for in, want := range cases {
		if got := CleanMessageID(in); got != want {
			t.Errorf("CleanMessageID(%q) = %q, want %q", in, got, want)
		}
	}
	if got := CleanMessageID(strings.Repeat("a", 1000)); len(got) > 250 {
		t.Errorf("long id not truncated: %d", len(got))
	}
}

func TestParseSanitizesForeignIDs(t *testing.T) {
	raw := "From: a@b.test\r\nTo: c@d.test\r\nSubject: x\r\nMessage-ID: <id\"<>@b.test>\r\nIn-Reply-To: <p@q> <r@s>\r\nReferences: <r1@x> junk\" <r2@x>\r\n\r\nbody\r\n"
	p, err := Parse([]byte(raw), false)
	if err != nil {
		t.Fatal(err)
	}
	if p.MessageID != "id" && strings.ContainsAny(p.MessageID, "\"<>") {
		t.Fatalf("message id = %q", p.MessageID)
	}
	if p.InReplyTo != "p@q" {
		t.Fatalf("in-reply-to = %q", p.InReplyTo)
	}
	for _, r := range p.References {
		if strings.ContainsAny(r, "\"<> ") {
			t.Fatalf("reference not sanitized: %q", r)
		}
	}
}

func TestAttachmentContentTypeAndFilename(t *testing.T) {
	raw := Build(Draft{
		From: store.Address{Email: "a@b.test"}, To: []store.Address{{Email: "c@d.test"}}, Subject: "x", MessageID: "1@b.test",
		Attach: []OutAttachment{
			{Filename: "rechnung‮fdp.exe", ContentType: "text/plain\r\nX-Injected: 1", Data: []byte("a")},
			{Filename: "Prüfbericht \"final\".pdf", ContentType: "application/pdf; foo=\"bar", Data: []byte("b")},
			{Filename: "../../etc/passwd", ContentType: "image/png", Data: []byte("c")},
		},
	})
	s := string(raw)
	if strings.Contains(s, "\r\nX-Injected") {
		t.Fatalf("content type injection:\n%s", s)
	}
	p, err := Parse(raw, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Attachments) != 3 {
		t.Fatalf("attachments = %+v", p.Attachments)
	}
	if p.Attachments[0].Filename != "rechnungfdp.exe" || p.Attachments[0].ContentType != "application/octet-stream" {
		t.Errorf("att0 = %+v", p.Attachments[0])
	}
	if p.Attachments[1].Filename != "Prüfbericht \"final\".pdf" || p.Attachments[1].ContentType != "application/octet-stream" {
		t.Errorf("att1 = %+v", p.Attachments[1])
	}
	if p.Attachments[2].Filename != ".._.._etc_passwd" || p.Attachments[2].ContentType != "image/png" {
		t.Errorf("att2 = %+v", p.Attachments[2])
	}
}

func TestSanitizeFilename(t *testing.T) {
	cases := map[string]string{
		"invoice‮txt.exe":         "invoicetxt.exe",
		"a⁦b⁩c‎d‏":                "abcd",
		"tab\there\x00\x1b\u0085": "tabhere",
		"  normal name.pdf  ":     "normal name.pdf",
		"dir/sub\\file":           "dir_sub_file",
		"Grüße.txt":               "Grüße.txt",
	}
	for in, want := range cases {
		if got := SanitizeFilename(in); got != want {
			t.Errorf("SanitizeFilename(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestParsedAttachmentNameSanitized(t *testing.T) {
	raw := "From: a@b.test\r\nTo: c@d.test\r\nSubject: x\r\nMIME-Version: 1.0\r\nContent-Type: multipart/mixed; boundary=XX\r\n\r\n" +
		"--XX\r\nContent-Type: text/plain\r\n\r\nhi\r\n" +
		"--XX\r\nContent-Type: application/octet-stream\r\nContent-Disposition: attachment; filename*=utf-8''bild%E2%80%AEgnp.exe\r\n\r\nxyz\r\n--XX--\r\n"
	p, err := Parse([]byte(raw), false)
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Attachments) != 1 || p.Attachments[0].Filename != "bildgnp.exe" {
		t.Fatalf("attachments = %+v", p.Attachments)
	}
}

func TestFromAddresses(t *testing.T) {
	parse := func(hdr string) *Parsed {
		p, err := Parse([]byte(hdr+"To: x@y.test\r\nSubject: s\r\n\r\nbody\r\n"), false)
		if err != nil {
			t.Fatal(err)
		}
		return p
	}
	if p := parse("From: Alice <alice@example.test>\r\n"); p.FromErr != nil || len(p.FromAll) != 1 || p.From.Email != "alice@example.test" {
		t.Fatalf("valid from: %+v %v", p.FromAll, p.FromErr)
	}
	if p := parse(""); !errors.Is(p.FromErr, ErrFromMissing) {
		t.Fatalf("missing from: %v", p.FromErr)
	}
	if p := parse("From: a@example.test\r\nFrom: b@evil.test\r\n"); !errors.Is(p.FromErr, ErrFromMultiple) {
		t.Fatalf("duplicate from: %v", p.FromErr)
	}
	if p := parse("From: <<<broken\r\n"); !errors.Is(p.FromErr, ErrFromMalformed) || p.From.Email != "" {
		t.Fatalf("malformed from: %v %+v", p.FromErr, p.From)
	}
	if p := parse("From: a@example.test, b@evil.test\r\n"); p.FromErr != nil || len(p.FromAll) != 2 {
		t.Fatalf("multi address from: %+v %v", p.FromAll, p.FromErr)
	}
	p := parse("From: a@example.test\r\nSender: s@example.test\r\n")
	if s, err := SenderAddress(p.Header); err != nil || s == nil || s.Email != "s@example.test" {
		t.Fatalf("sender: %+v %v", s, err)
	}
	p = parse("From: a@example.test\r\nSender: s@example.test, t@example.test\r\n")
	if _, err := SenderAddress(p.Header); err == nil {
		t.Fatal("multi-address sender must fail")
	}
	p = parse("From: a@example.test\r\n")
	if s, err := SenderAddress(p.Header); err != nil || s != nil {
		t.Fatalf("no sender: %+v %v", s, err)
	}
}

func TestStripHeadersAndCount(t *testing.T) {
	raw := []byte("Received: from a\r\n\tby b\r\nX-Spam-Status: Yes\r\nAuthentication-Results: mail.example.test;\r\n spf=pass\r\nAuthentication-Results: other.test; spf=fail\r\nX-Postwerk-Foo: 1\r\nSubject: hi\r\nReceived: from c\r\n\r\nX-Spam-Status: body line\r\n")
	out := StripHeaders(raw, func(name, value string) bool {
		n := strings.ToLower(name)
		return strings.HasPrefix(n, "x-spam-") || strings.HasPrefix(n, "x-postwerk-") ||
			(n == "authentication-results" && strings.HasPrefix(value, "mail.example.test;"))
	})
	want := "Received: from a\r\n\tby b\r\nAuthentication-Results: other.test; spf=fail\r\nSubject: hi\r\nReceived: from c\r\n\r\nX-Spam-Status: body line\r\n"
	if string(out) != want {
		t.Fatalf("got:\n%q\nwant:\n%q", out, want)
	}
	if n := CountHeader(raw, "received"); n != 2 {
		t.Fatalf("received count = %d", n)
	}
	if n := CountHeader([]byte("Subject: x\n\nReceived: no\n"), "Received"); n != 0 {
		t.Fatalf("body counted: %d", n)
	}
}
