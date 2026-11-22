package message

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

func nested(levels int) []byte {
	var b strings.Builder
	b.WriteString("From: a@example.org\r\nTo: b@example.org\r\nSubject: x\r\nMIME-Version: 1.0\r\n")
	for i := 0; i < levels; i++ {
		fmt.Fprintf(&b, "Content-Type: multipart/mixed; boundary=\"b%d\"\r\n\r\n--b%d\r\n", i, i)
	}
	b.WriteString("Content-Type: text/plain\r\n\r\nhallo\r\n")
	return []byte(b.String())
}

func TestTooComplex(t *testing.T) {
	if TooComplex(nested(3)) {
		t.Fatal("normal multipart flagged")
	}
	if !TooComplex(nested(MaxMultiparts + 1)) {
		t.Fatal("deep nesting not flagged")
	}
	if !TooComplex([]byte(strings.Repeat("Content-Type: multipart/mixed; BOUNDARY *= x\r\n", MaxMultiparts+1))) {
		t.Fatal("case and spacing variants not counted")
	}
}

func TestParseDeepNestingBounded(t *testing.T) {
	raw := nested(20000)
	start := time.Now()
	p, err := Parse(raw, false)
	if err != nil || p == nil {
		t.Fatalf("parse: %v", err)
	}
	if d := time.Since(start); d > 3*time.Second {
		t.Fatalf("parse took %v", d)
	}
	if p.Subject != "x" {
		t.Fatalf("header lost: %q", p.Subject)
	}
	if p, _ := Parse(nested(3), false); !strings.Contains(p.Text, "hallo") {
		t.Fatalf("normal message text = %q", p.Text)
	}
}
