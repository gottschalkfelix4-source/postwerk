package sieve

import (
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"
)

// TestEditorScript compiles and runs a script produced by the web rule
// editor (testdata/editor.sieve).
func TestEditorScript(t *testing.T) {
	src, err := os.ReadFile("testdata/editor.sieve")
	if err != nil {
		t.Fatal(err)
	}
	raw := "From: buchhaltung@firma.de\r\nTo: a*b\\cx@example.test\r\nList-Id: y\r\nSubject: Rechnung 4711\r\n\r\n" + strings.Repeat("x", 600<<10)
	r := runMsg(t, string(src), ParseMessage([]byte(raw)))
	if got := summary(r); got != `fileinto:Rechnungen/2026[\Seen \Flagged]` {
		t.Fatalf("editor script: %s", got)
	}
	if got := summary(run(t, string(src))); got != "redirect:x@y.de(copy),discard" {
		t.Fatalf("editor script fallback rule: %s", got)
	}
}

const testMsg = "Return-Path: <sender@example.org>\r\n" +
	"From: \"Max Müller\" <max+news@example.org>\r\n" +
	"To: alice@example.test, \"Bob\" <bob@example.test>\r\n" +
	"Cc: team+dev@example.test\r\n" +
	"Subject: =?utf-8?q?Gr=C3=BC=C3=9Fe_aus_Berlin?=\r\n" +
	"X-Spam-Score: 7.5\r\n" +
	"X-Mailing-List: dev@lists.example.org\r\n" +
	"Date: Fri, 20 Nov 2026 10:30:00 +0100\r\n" +
	"List-Id: Developers <dev.lists.example.org>\r\n" +
	"\r\n" +
	"Hallo Alice,\r\nhier ist der Bericht zum Projekt Sonnenschein.\r\n"

func msg() *Message {
	m := ParseMessage([]byte(testMsg))
	m.EnvelopeFrom = "sender@example.org"
	m.EnvelopeTo = "alice+filter@example.test"
	return m
}

func run(t *testing.T, script string) *Result {
	t.Helper()
	return runMsg(t, script, msg())
}

func runMsg(t *testing.T, script string, m *Message) *Result {
	t.Helper()
	s, err := Compile(script)
	if err != nil {
		t.Fatalf("compile: %v\n%s", err, script)
	}
	res, err := Execute(s, m, Env{Now: time.Date(2026, 11, 21, 12, 0, 0, 0, time.UTC),
		MailboxExists: func(n string) bool { return n == "Archiv" }})
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	return res
}

// summary renders the final actions compactly for comparisons.
func summary(r *Result) string {
	var parts []string
	for _, a := range r.Final() {
		s := a.Type
		switch a.Type {
		case "fileinto":
			s += ":" + a.Mailbox
		case "redirect":
			s += ":" + a.Address
		case "reject", "ereject":
			s += ":" + a.Reason
		}
		if a.Copy {
			s += "(copy)"
		}
		if len(a.Flags) > 0 {
			s += "[" + strings.Join(a.Flags, " ") + "]"
		}
		parts = append(parts, s)
	}
	return strings.Join(parts, ",")
}

// cond runs `if <test> { fileinto "Y"; }` and reports whether it matched.
func cond(t *testing.T, require, testExpr string) bool {
	t.Helper()
	script := `require ["fileinto"` + require + `]; if ` + testExpr + ` { fileinto "Y"; }`
	return summary(run(t, script)) == "fileinto:Y"
}

func TestTests(t *testing.T) {
	cases := []struct {
		req, test string
		want      bool
	}{
		{"", `header :contains "subject" "GRÜßE"`, false}, // ascii-casemap does not fold Ü/ü
		{"", `header :contains "subject" "grüße"`, true},
		{"", `header :contains "subject" "Grüße"`, true},
		{"", `header :is "Subject" "Grüße aus Berlin"`, true},
		{"", `header :is "subject" "grüße aus berlin"`, true},
		{"", `header :is "subject" "Grüße aus"`, false},
		{"", `header :is "subject" "GRüßE AUS BERLIN"`, true},
		{"", `header :comparator "i;octet" :is "subject" "GRüßE AUS BERLIN"`, false},
		{"", `header :comparator "i;octet" :contains "subject" "Berlin"`, true},
		{"", `header :matches "subject" "Gr*Berlin"`, true},
		{"", `header :matches "subject" "Gr??e*"`, true},
		{"", `header :matches "subject" "Gr?e*"`, false},
		{"", `header :matches "x-mailing-list" "*@lists.example.org"`, true},
		{"", `header :matches "subject" "\\*"`, false},
		{"", `header :contains ["to", "cc"] "team+dev"`, true},
		{"", `header :contains "x-missing" ""`, false},
		{"", `address :is "from" "max+news@example.org"`, true},
		{"", `address :all :is "from" "MAX+NEWS@EXAMPLE.ORG"`, true},
		{"", `address :domain :is "from" "example.org"`, true},
		{"", `address :localpart :is "from" "max+news"`, true},
		{"", `address :domain :is ["to","cc"] "example.test"`, true},
		{"", `address :is "to" "bob@example.test"`, true},
		{"", `address :contains "to" "Bob"`, true},
		{"", `address :is "to" "Bob"`, false},
		{`, "subaddress"`, `address :user :is "from" "max"`, true},
		{`, "subaddress"`, `address :detail :is "from" "news"`, true},
		{`, "subaddress"`, `address :detail :is "to" ""`, false},
		{`, "envelope"`, `envelope :is "from" "sender@example.org"`, true},
		{`, "envelope"`, `envelope :domain :is "to" "example.test"`, true},
		{`, "envelope", "subaddress"`, `envelope :detail :is "to" "filter"`, true},
		{`, "envelope", "subaddress"`, `envelope :user :is "to" "alice"`, true},
		{"", `exists "list-id"`, true},
		{"", `exists ["list-id", "x-nope"]`, false},
		{"", `size :over 100`, true},
		{"", `size :under 1K`, true},
		{"", `size :over 1M`, false},
		{"", `not exists "x-nope"`, true},
		{"", `allof (exists "from", header :contains "subject" "Berlin")`, true},
		{"", `allof (exists "from", header :contains "subject" "Hamburg")`, false},
		{"", `anyof (false, header :contains "subject" "Berlin")`, true},
		{"", `anyof (false, not true)`, false},
		{"", `true`, true},
		{`, "body"`, `body :contains "sonnenschein"`, true},
		{`, "body"`, `body :text :contains "Bericht"`, true},
		{`, "body"`, `body :raw :matches "*Projekt*"`, true},
		{`, "body"`, `body :content "image" :contains "Bericht"`, false},
		{`, "relational", "comparator-i;ascii-numeric"`, `header :value "gt" :comparator "i;ascii-numeric" "x-spam-score" "5"`, true},
		{`, "relational", "comparator-i;ascii-numeric"`, `header :value "lt" :comparator "i;ascii-numeric" "x-spam-score" "5"`, false},
		{`, "relational", "comparator-i;ascii-numeric"`, `address :count "eq" :comparator "i;ascii-numeric" ["to","cc"] "3"`, true},
		{`, "relational", "comparator-i;ascii-numeric"`, `header :count "ge" :comparator "i;ascii-numeric" "received" "1"`, false},
		{`, "regex"`, `header :regex "subject" "^Gr.*e aus [A-Z][a-z]+$"`, true},
		{`, "regex"`, `header :regex "x-spam-score" "^[0-6]\\."`, false},
		{`, "date"`, `date :is "date" "year" "2026"`, true},
		{`, "date"`, `date :zone "+0100" :is "date" "hour" "10"`, true},
		{`, "date"`, `date :originalzone :is "date" "zone" "+0100"`, true},
		{`, "date"`, `currentdate :zone "+0000" :is "date" "2026-11-21"`, true},
		{`, "date", "relational"`, `currentdate :zone "+0000" :value "ge" "date" "2026-12-01"`, false},
		{`, "mailbox"`, `mailboxexists "Archiv"`, true},
		{`, "mailbox"`, `mailboxexists ["Archiv", "Fehlt"]`, false},
	}
	for _, c := range cases {
		if got := cond(t, c.req, c.test); got != c.want {
			t.Errorf("%s: got %v, want %v", c.test, got, c.want)
		}
	}
}

func TestActions(t *testing.T) {
	cases := []struct{ script, want string }{
		{``, "keep"},
		{`keep;`, "keep"},
		{`discard;`, "discard"},
		{`require "fileinto"; fileinto "Archiv";`, "fileinto:Archiv"},
		{`require ["fileinto", "copy"]; fileinto :copy "Archiv";`, "fileinto:Archiv(copy),keep"},
		{`redirect "other@example.net";`, "redirect:other@example.net"},
		{`require "copy"; redirect :copy "other@example.net";`, "redirect:other@example.net(copy),keep"},
		{`redirect "a@example.net"; redirect "A@example.net";`, "redirect:a@example.net"},
		{`require "reject"; reject "Nein danke";`, "reject:Nein danke"},
		{`require "ereject"; ereject "Nein";`, "ereject:Nein"},
		{`require "fileinto"; if header :contains "subject" "Berlin" { fileinto "Berlin"; stop; } fileinto "Rest";`, "fileinto:Berlin"},
		{`require "fileinto"; if header :contains "subject" "Hamburg" { fileinto "HH"; } elsif exists "list-id" { fileinto "Listen"; } else { discard; }`, "fileinto:Listen"},
		{`require "fileinto"; if false { fileinto "A"; } elsif false { fileinto "B"; } else { fileinto "C"; }`, "fileinto:C"},
		{`stop; discard;`, "keep"},
		{`require "imap4flags"; addflag "\\Seen"; addflag ["\\Flagged", "\\seen"];`, `keep[\Seen \Flagged]`},
		{`require "imap4flags"; setflag "\\Seen \\Answered"; removeflag "\\seen"; keep;`, `keep[\Answered]`},
		{`require ["imap4flags", "fileinto"]; addflag "\\Seen"; fileinto :flags "$Work" "Arbeit"; fileinto "Rest";`, `fileinto:Arbeit[$Work],fileinto:Rest[\Seen]`},
		{`require ["imap4flags", "fileinto"]; if not hasflag "\\Seen" { addflag "\\Flagged"; } if hasflag :contains "flag" { fileinto "F"; }`, `fileinto:F[\Flagged]`},
		{`require ["fileinto", "variables"]; if header :matches "x-mailing-list" "*@lists.*" { fileinto "Listen/${1}"; }`, "fileinto:Listen/dev"},
		{`require ["fileinto", "variables"]; if header :matches "subject" "* aus *" { set :upper "ort" "${2}"; fileinto "${ort}-${0}"; }`, "fileinto:BERLIN-Grüße aus Berlin"},
		{`require ["fileinto", "variables", "regex"]; if header :regex "list-id" "<([a-z]+)\\." { fileinto "L-${1}"; }`, "fileinto:L-dev"},
		{`require ["fileinto", "variables"]; set "a" "x"; set :length "n" "Grüße"; fileinto "${a}${n}${unbekannt}${}";`, "fileinto:x5${}"},
		{`require ["fileinto", "variables"]; set :quotewildcard "p" "a*b"; if string :matches "a*b" "${p}" { fileinto "Q"; }`, "fileinto:Q"},
		{`require ["fileinto", "variables", "envelope", "subaddress"]; if envelope :detail :matches "to" "*" { fileinto "Tags/${1}"; }`, "fileinto:Tags/filter"},
		{`require ["fileinto", "mailbox"]; fileinto :create "Neu";`, "fileinto:Neu"},
	}
	for _, c := range cases {
		if got := summary(run(t, c.script)); got != c.want {
			t.Errorf("%s\n got  %s\n want %s", c.script, got, c.want)
		}
	}
}

func TestVacationAction(t *testing.T) {
	r := run(t, `require ["vacation", "variables"]; set "s" "Urlaub"; vacation :days 100 :subject "${s}" :from "alice@example.test" :addresses ["a@example.test"] :handle "h1" "Bin weg.";`)
	a := r.Final()
	if len(a) != 2 || a[0].Type != "vacation" || a[1].Type != "keep" {
		t.Fatalf("actions = %+v", a)
	}
	v := a[0].Vacation
	if v.Days != 30 || v.Subject != "Urlaub" || v.From != "alice@example.test" || v.Handle != "h1" || v.Reason != "Bin weg." || len(v.Addresses) != 1 {
		t.Fatalf("vacation = %+v", v)
	}
	r = run(t, `require "vacation"; vacation :days 0 text:
Mehrzeilig
..mit Punkt
.
;`)
	if v := r.Actions[0].Vacation; v.Days != 1 || v.Reason != "Mehrzeilig\r\n.mit Punkt\r\n" {
		t.Fatalf("vacation = %+v", v)
	}
}

func TestRuntimeErrors(t *testing.T) {
	cases := []struct{ script, want string }{
		{`require ["reject", "fileinto"]; fileinto "A"; reject "x";`, "reject kann nicht"},
		{`require ["reject"]; reject "x"; keep;`, "reject kann nicht"},
		{`require "vacation"; vacation "a"; vacation "b";`, "nur einmal"},
		{`redirect "a@x.de"; redirect "b@x.de"; redirect "c@x.de"; redirect "d@x.de"; redirect "e@x.de"; redirect "f@x.de";`, "Zu viele Weiterleitungen"},
		{`require "variables"; set "a" "kein-at"; redirect "${a}";`, "keine gültige"},
	}
	for _, c := range cases {
		s, err := Compile(c.script)
		if err != nil {
			t.Fatalf("compile %s: %v", c.script, err)
		}
		_, err = Execute(s, msg(), Env{})
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: got %v, want %q", c.script, err, c.want)
		}
	}
	// Too many actions.
	var b strings.Builder
	b.WriteString(`require ["fileinto", "copy"];`)
	for i := 0; i < 40; i++ {
		fmt.Fprintf(&b, `fileinto :copy "F%d";`, i)
	}
	s, err := Compile(b.String())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Execute(s, msg(), Env{}); err == nil || !strings.Contains(err.Error(), "Zu viele Aktionen") {
		t.Fatalf("expected action limit, got %v", err)
	}
}

func TestCompileErrors(t *testing.T) {
	cases := []struct {
		script     string
		line, col  int
		msgContain string
	}{
		{`fileinto "A";`, 1, 1, `require "fileinto"`},
		{"keep;\nrequire \"fileinto\";", 2, 1, "nur am Anfang"},
		{`require "nonsense";`, 1, 9, "nicht unterstützt"},
		{"if true {\n  keep\n}", 3, 1, "„;“ fehlt nach „keep“"},
		{`if true { keep; `, 1, 1, "nicht mit „}“ geschlossen"},
		{`keep; }`, 1, 7, "ohne passendes"},
		{`foo;`, 1, 1, "Unbekannter Befehl „foo“"},
		{`if bar { keep; }`, 1, 4, "Unbekannter Test „bar“"},
		{`if header :foo "a" "b" { keep; }`, 1, 11, "Unbekannter Parameter „:foo“"},
		{`if header :is :contains "a" "b" { keep; }`, 1, 4, "nur eine Vergleichsart"},
		{`if header "a" { keep; }`, 1, 4, "erwartet 2 Argument"},
		{`if header :comparator "i;foo" "a" "b" { keep; }`, 1, 23, "Unbekannter Vergleicher"},
		{`if header :regex "a" "b" { keep; }`, 1, 11, `require "regex"`},
		{`require "regex"; if header :regex "a" "(" { keep; }`, 1, 39, "Ungültiger regulärer Ausdruck"},
		{`if size 10 { keep; }`, 1, 4, ":over oder :under"},
		{`elsif true { keep; }`, 1, 1, "ohne vorangehendes „if“"},
		{`if true keep;`, 1, 1, "benötigt einen Block"},
		{`redirect "keine adresse";`, 1, 10, "keine gültige E-Mail-Adresse"},
		{"keep;\n  \"unterminated", 2, 3, "nicht mit „\"“ abgeschlossen"},
		{"/* offen", 1, 1, "Kommentar"},
		{`keep; @`, 1, 7, "Unerwartetes Zeichen „@“"},
		{`require "relational"; if header :value "xx" "a" "b" { keep; }`, 1, 40, "Unbekannter Vergleichsoperator"},
		{`require "comparator-i;ascii-numeric"; if header :contains :comparator "i;ascii-numeric" "a" "b" { keep; }`, 1, 71, "unterstützt nur"},
		{`if not { keep; }`, 1, 4, "„not“ erwartet genau einen Test"},
		{`require "date"; if date "date" "century" "x" { keep; }`, 1, 20, "Unbekannter Datumsteil"},
	}
	for _, c := range cases {
		_, err := Compile(c.script)
		var se *Error
		if !errors.As(err, &se) {
			t.Errorf("%q: expected *Error, got %v", c.script, err)
			continue
		}
		if se.Line != c.line || se.Column != c.col || !strings.Contains(se.Msg, c.msgContain) {
			t.Errorf("%q: got %d:%d %q, want %d:%d containing %q", c.script, se.Line, se.Column, se.Msg, c.line, c.col, c.msgContain)
		}
		if !strings.HasPrefix(se.Error(), fmt.Sprintf("Zeile %d, Spalte %d: ", c.line, c.col)) {
			t.Errorf("Error() = %q", se.Error())
		}
	}
	if _, err := Compile(strings.Repeat("#", MaxScriptSize+1)); err == nil {
		t.Fatal("oversized script accepted")
	}
	deep := strings.Repeat("if true {", 100) + strings.Repeat("}", 100)
	if _, err := Compile(deep); err == nil || !strings.Contains(err.Error(), "verschachtelt") {
		t.Fatalf("deep nesting: %v", err)
	}
}

func TestLexerDetails(t *testing.T) {
	script := "# Kommentar\r\nrequire [\"fileinto\", /* inline */ \"copy\"];\r\n" +
		"if header :contains \"subject\" \"a\\\"b\\\\c\\q\" { fileinto \"X\"; }\r\n"
	s, err := Compile(script)
	if err != nil {
		t.Fatal(err)
	}
	if got := s.Requires(); len(got) != 2 || got[0] != "fileinto" || got[1] != "copy" {
		t.Fatalf("requires = %v", got)
	}
	m := ParseMessage([]byte("Subject: x a\"b\\cq y\r\n\r\n"))
	if r := runMsg(t, script, m); summary(r) != "fileinto:X" {
		t.Fatalf("escape handling: %s", summary(r))
	}
	if _, err := Compile(`if size :over 10G { keep; }`); err != nil {
		t.Fatal(err)
	}
}

func TestGlobMatch(t *testing.T) {
	cases := []struct {
		pat, s string
		ok     bool
		caps   []string
	}{
		{"*", "", true, []string{""}},
		{"a*c", "abbc", true, []string{"bb"}},
		{"*.*", "a.b.c", true, []string{"a", "b.c"}},
		{"*@*", "user@host", true, []string{"user", "host"}},
		{"?b?", "abc", true, []string{"a", "c"}},
		{"a**b", "axxb", true, []string{"xx", ""}},
		{`a\*`, "a*", true, nil},
		{`a\*`, "ab", false, nil},
		{"A*", "abc", true, []string{"bc"}},
		{"*x", "abc", false, nil},
		{"ä?", "äö", true, []string{"ö"}},
	}
	for _, c := range cases {
		ok, caps := globMatch(c.pat, c.s, true)
		if ok != c.ok || (c.caps != nil && strings.Join(caps, "|") != strings.Join(c.caps, "|")) {
			t.Errorf("%q ~ %q: got %v %q", c.pat, c.s, ok, caps)
		}
	}
	// Long values use the linear matcher and must stay fast.
	long := strings.Repeat("a", 200000) + "b"
	start := time.Now()
	if ok, _ := globMatch("*a*a*a*a*b", long, false); !ok {
		t.Fatal("long match failed")
	}
	if ok, _ := globMatch("*a*a*a*a*c", long, false); ok {
		t.Fatal("long non-match succeeded")
	}
	if time.Since(start) > 2*time.Second {
		t.Fatal("glob matching too slow")
	}
}

func TestStepLimit(t *testing.T) {
	// Many tests over many headers must hit the step limit, not run forever.
	var hb strings.Builder
	for i := 0; i < 1500; i++ {
		hb.WriteString("X-A: v\r\n")
	}
	m := ParseMessage([]byte(hb.String() + "\r\nbody"))
	keys := make([]string, 50)
	for i := range keys {
		keys[i] = fmt.Sprintf("%q", fmt.Sprintf("k%d", i))
	}
	script := `if header :contains "x-a" [` + strings.Join(keys, ",") + `] { keep; }`
	s, err := Compile(script)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Execute(s, m, Env{}); err == nil || !strings.Contains(err.Error(), "aufwendig") {
		t.Fatalf("expected step limit, got %v", err)
	}
}

func TestComparators(t *testing.T) {
	if compare("i;ascii-numeric", "007", "7") != 0 || compare("i;ascii-numeric", "10", "9") <= 0 ||
		compare("i;ascii-numeric", "abc", "999") <= 0 || compare("i;ascii-numeric", "x", "y") != 0 {
		t.Fatal("ascii-numeric ordering")
	}
	if compare("i;ascii-casemap", "ABC", "abc") != 0 || compare("i;octet", "ABC", "abc") == 0 {
		t.Fatal("casemap/octet")
	}
}

func TestAddressParsing(t *testing.T) {
	got := addressList(`"Doe, John" <john@example.com>, jane@example.com`)
	if strings.Join(got, ",") != "john@example.com,jane@example.com" {
		t.Fatalf("got %v", got)
	}
	// Malformed header falls back to a lenient split.
	got = addressList(`kaputt <a@b.c>, x@y.z (`)
	if len(got) != 2 || got[0] != "a@b.c" {
		t.Fatalf("lenient: %v", got)
	}
}
