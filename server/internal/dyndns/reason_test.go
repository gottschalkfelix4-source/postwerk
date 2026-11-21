package dyndns

import (
	"strings"
	"testing"
)

func TestShortReason(t *testing.T) {
	if got := shortReason("good 1.2.3.4\nnochg 1.2.3.4"); got != "good 1.2.3.4" {
		t.Errorf("first line = %q", got)
	}
	if got := shortReason("<html><body>secret</body></html>"); strings.Contains(got, "secret") {
		t.Errorf("html echoed: %q", got)
	}
	if got := shortReason(strings.Repeat("x", 500)); len([]rune(got)) > 61 {
		t.Errorf("not truncated: %d", len(got))
	}
	if got := shortReason("a\x00b\x1bc"); got != "abc" {
		t.Errorf("control characters kept: %q", got)
	}
	_, msg, _ := interpret(200, "intern: db-passwort=geheim")
	if strings.Contains(msg, "<") || len(msg) > 100 {
		t.Errorf("unknown answer not shortened: %q", msg)
	}
}
