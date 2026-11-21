package sieve

import (
	"net/mail"
	"regexp"
	"strings"
)

// extensions lists every capability a script may require.
var extensions = map[string]bool{
	"fileinto": true, "reject": true, "ereject": true, "envelope": true, "imap4flags": true,
	"copy": true, "body": true, "variables": true, "vacation": true, "relational": true,
	"regex": true, "subaddress": true, "date": true, "mailbox": true,
	"comparator-i;ascii-numeric": true, "comparator-i;octet": true, "comparator-i;ascii-casemap": true,
}

// Capabilities returns the extensions advertised to clients (ManageSieve
// SIEVE capability), without the implicitly available comparators.
func Capabilities() []string {
	return []string{"fileinto", "reject", "ereject", "envelope", "imap4flags", "copy", "body", "variables",
		"vacation", "relational", "regex", "subaddress", "date", "mailbox", "comparator-i;ascii-numeric"}
}

// MaxRedirects is the maximum number of redirect actions per message.
const MaxRedirects = 5

// Script is a compiled, validated Sieve script.
type Script struct {
	cmds     []command
	caps     map[string]bool
	requires []string
}

// Requires returns the extensions the script declared.
func (s *Script) Requires() []string { return append([]string(nil), s.requires...) }

// ---- compiled nodes ----

type command interface{}

type ifBranch struct {
	test  test
	block []command
}

type cmdIf struct {
	branches []ifBranch
	elseBody []command
	hasElse  bool
}

type cmdStop struct{}
type cmdDiscard struct{}

type cmdKeep struct {
	flags    []string // nil: use the internal flag variable
	hasFlags bool
}

type cmdFileinto struct {
	mailbox  string
	copy     bool
	create   bool
	flags    []string
	hasFlags bool
}

type cmdRedirect struct {
	addr string
	copy bool
}

type cmdReject struct {
	reason  string
	ereject bool
}

type cmdFlags struct {
	op      string // setflag, addflag, removeflag
	varName string // "" is the internal variable
	flags   []string
}

type cmdSet struct {
	name  string
	value string
	mods  []string
}

type cmdVacation struct {
	days      int64
	hasDays   bool
	subject   *string
	from      *string
	addresses []string
	handle    *string
	mime      bool
	reason    string
}

type test interface{}

type testConst struct{ v bool }
type testNot struct{ t test }
type testAllof struct{ ts []test }
type testAnyof struct{ ts []test }
type testExists struct{ headers []string }
type testSize struct {
	over  bool
	limit int64
}

type matchSpec struct {
	comparator string
	typ        string // is, contains, matches, regex, count, value
	rel        string // relational operator for count/value
}

type testHeader struct {
	m       matchSpec
	headers []string
	keys    []string
}

type testAddress struct {
	m        matchSpec
	part     string // all, localpart, domain, user, detail
	headers  []string
	keys     []string
	envelope bool
}

type testBody struct {
	m         matchSpec
	transform string // text, raw, content
	types     []string
	keys      []string
}

type testString struct {
	m       matchSpec
	sources []string
	keys    []string
}

type testHasflag struct {
	m    matchSpec
	vars []string
	keys []string
}

type testDate struct {
	m            matchSpec
	current      bool
	zone         string
	originalZone bool
	header       string
	part         string
	keys         []string
}

type testMailboxExists struct{ names []string }

// ---- compiler ----

type compiler struct {
	caps     map[string]bool
	requires []string
	regexes  int
}

// Compile parses and validates a script. Errors are *Error values with
// German messages and source positions.
func Compile(src string) (*Script, error) {
	if len(src) > MaxScriptSize {
		return nil, &Error{Msg: "Das Skript ist zu groß (höchstens 64 KB)"}
	}
	nodes, perr := parse(src)
	if perr != nil {
		return nil, perr
	}
	c := &compiler{caps: map[string]bool{}}
	cmds, err := c.block(nodes, true)
	if err != nil {
		return nil, err
	}
	return &Script{cmds: cmds, caps: c.caps, requires: c.requires}, nil
}

// Check validates a script and returns nil or an *Error.
func Check(src string) error {
	_, err := Compile(src)
	return err
}

func (c *compiler) need(p Pos, ext, what string) *Error {
	if !c.caps[ext] {
		return errAt(p, "„%s“ benötigt require \"%s\"", what, ext)
	}
	return nil
}

func (c *compiler) block(nodes []*cmdNode, top bool) ([]command, *Error) {
	var out []command
	requireAllowed := top
	for i := 0; i < len(nodes); i++ {
		n := nodes[i]
		if n.name == "require" {
			if !requireAllowed {
				return nil, errAt(n.pos, "„require“ ist nur am Anfang des Skripts erlaubt")
			}
			if err := c.require(n); err != nil {
				return nil, err
			}
			continue
		}
		requireAllowed = false
		switch n.name {
		case "if":
			node := &cmdIf{}
			cur := n
			for {
				t, b, err := c.conditional(cur)
				if err != nil {
					return nil, err
				}
				node.branches = append(node.branches, ifBranch{t, b})
				if i+1 < len(nodes) && nodes[i+1].name == "elsif" {
					i++
					cur = nodes[i]
					continue
				}
				break
			}
			if i+1 < len(nodes) && nodes[i+1].name == "else" {
				i++
				e := nodes[i]
				if len(e.args) > 0 || len(e.tests) > 0 {
					return nil, errAt(e.pos, "„else“ erwartet keine Argumente")
				}
				if !e.hasBlock {
					return nil, errAt(e.pos, "„else“ benötigt einen Block in „{ … }“")
				}
				b, err := c.block(e.block, false)
				if err != nil {
					return nil, err
				}
				node.elseBody, node.hasElse = b, true
			}
			out = append(out, node)
		case "elsif", "else":
			return nil, errAt(n.pos, "„%s“ ohne vorangehendes „if“", n.name)
		default:
			if n.hasBlock {
				return nil, errAt(n.pos, "„%s“ erwartet keinen Block", n.name)
			}
			if len(n.tests) > 0 {
				return nil, errAt(n.tests[0].pos, "„%s“ erwartet keinen Test", n.name)
			}
			cmd, err := c.action(n)
			if err != nil {
				return nil, err
			}
			out = append(out, cmd)
		}
	}
	return out, nil
}

func (c *compiler) require(n *cmdNode) *Error {
	if len(n.args) != 1 || n.args[0].kind != argStrings || len(n.tests) > 0 || n.hasBlock {
		return errAt(n.pos, "„require“ erwartet eine Zeichenkette oder Liste von Erweiterungen")
	}
	for _, e := range n.args[0].strs {
		e = strings.ToLower(e)
		if !extensions[e] {
			return errAt(n.args[0].pos, "Erweiterung „%s“ wird nicht unterstützt", e)
		}
		if !c.caps[e] {
			c.caps[e] = true
			c.requires = append(c.requires, e)
		}
	}
	return nil
}

func (c *compiler) conditional(n *cmdNode) (test, []command, *Error) {
	if len(n.args) > 0 {
		return nil, nil, errAt(n.args[0].pos, "„%s“ erwartet einen Test, keine Argumente", n.name)
	}
	if len(n.tests) != 1 {
		return nil, nil, errAt(n.pos, "„%s“ erwartet genau einen Test", n.name)
	}
	if !n.hasBlock {
		return nil, nil, errAt(n.pos, "„%s“ benötigt einen Block in „{ … }“", n.name)
	}
	t, err := c.test(n.tests[0])
	if err != nil {
		return nil, nil, err
	}
	b, err := c.block(n.block, false)
	if err != nil {
		return nil, nil, err
	}
	return t, b, nil
}

// ---- argument helpers ----

type paramKind int

const (
	pNone paramKind = iota
	pString
	pStrings
	pNumber
)

// splitTags separates tagged arguments (which must come first) from the
// positional ones. The map holds each tag's parameter, or the tag itself
// for tags without a parameter.
func splitTags(name string, args []arg, spec map[string]paramKind) (map[string]arg, []arg, *Error) {
	tags := map[string]arg{}
	i := 0
	for ; i < len(args) && args[i].kind == argTag; i++ {
		a := args[i]
		kind, ok := spec[a.tag]
		if !ok {
			return nil, nil, errAt(a.pos, "Unbekannter Parameter „:%s“ für „%s“", a.tag, name)
		}
		if _, dup := tags[a.tag]; dup {
			return nil, nil, errAt(a.pos, "Parameter „:%s“ ist doppelt angegeben", a.tag)
		}
		if kind == pNone {
			tags[a.tag] = a
			continue
		}
		if i+1 >= len(args) {
			return nil, nil, errAt(a.pos, "Parameter „:%s“ benötigt einen Wert", a.tag)
		}
		v := args[i+1]
		switch {
		case kind == pNumber && v.kind != argNumber:
			return nil, nil, errAt(v.pos, "Parameter „:%s“ erwartet eine Zahl", a.tag)
		case kind == pString && (v.kind != argStrings || len(v.strs) != 1):
			return nil, nil, errAt(v.pos, "Parameter „:%s“ erwartet eine Zeichenkette", a.tag)
		case kind == pStrings && v.kind != argStrings:
			return nil, nil, errAt(v.pos, "Parameter „:%s“ erwartet eine Liste von Zeichenketten", a.tag)
		}
		tags[a.tag] = v
		i++
	}
	rest := args[i:]
	for _, a := range rest {
		if a.kind == argTag {
			return nil, nil, errAt(a.pos, "Parameter „:%s“ muss vor den übrigen Argumenten von „%s“ stehen", a.tag, name)
		}
	}
	return tags, rest, nil
}

// positional checks the number and kinds of positional arguments.
func positional(name string, p Pos, args []arg, kinds ...paramKind) *Error {
	if len(args) != len(kinds) {
		return errAt(p, "„%s“ erwartet %d Argument(e), gefunden: %d", name, len(kinds), len(args))
	}
	for i, k := range kinds {
		a := args[i]
		switch k {
		case pNumber:
			if a.kind != argNumber {
				return errAt(a.pos, "„%s“ erwartet hier eine Zahl", name)
			}
		case pString:
			if a.kind != argStrings || len(a.strs) != 1 || a.list {
				return errAt(a.pos, "„%s“ erwartet hier eine einzelne Zeichenkette", name)
			}
		case pStrings:
			if a.kind != argStrings {
				return errAt(a.pos, "„%s“ erwartet hier eine Zeichenkette oder Liste", name)
			}
		}
	}
	return nil
}

var matchTags = map[string]paramKind{
	"comparator": pString, "is": pNone, "contains": pNone, "matches": pNone, "regex": pNone,
	"count": pString, "value": pString,
}

func withTags(base map[string]paramKind, more map[string]paramKind) map[string]paramKind {
	out := map[string]paramKind{}
	for k, v := range base {
		out[k] = v
	}
	for k, v := range more {
		out[k] = v
	}
	return out
}

var relOps = map[string]bool{"gt": true, "ge": true, "lt": true, "le": true, "eq": true, "ne": true}

func (c *compiler) matchSpec(name string, p Pos, tags map[string]arg) (matchSpec, *Error) {
	m := matchSpec{comparator: "i;ascii-casemap", typ: "is"}
	n := 0
	for _, t := range []string{"is", "contains", "matches", "regex", "count", "value"} {
		a, ok := tags[t]
		if !ok {
			continue
		}
		n++
		m.typ = t
		switch t {
		case "regex":
			if err := c.need(a.pos, "regex", ":regex"); err != nil {
				return m, err
			}
		case "count", "value":
			if err := c.need(a.pos, "relational", ":"+t); err != nil {
				return m, err
			}
			m.rel = strings.ToLower(a.strs[0])
			if !relOps[m.rel] {
				return m, errAt(a.pos, "Unbekannter Vergleichsoperator „%s“ (erlaubt: gt, ge, lt, le, eq, ne)", a.strs[0])
			}
		}
	}
	if n > 1 {
		return m, errAt(p, "„%s“: nur eine Vergleichsart (:is, :contains, :matches, …) ist erlaubt", name)
	}
	if a, ok := tags["comparator"]; ok {
		m.comparator = strings.ToLower(a.strs[0])
		switch m.comparator {
		case "i;octet", "i;ascii-casemap":
		case "i;ascii-numeric":
			if err := c.need(a.pos, "comparator-i;ascii-numeric", "i;ascii-numeric"); err != nil {
				return m, err
			}
			if m.typ == "contains" || m.typ == "matches" || m.typ == "regex" {
				return m, errAt(a.pos, "Der Vergleicher „i;ascii-numeric“ unterstützt nur :is, :count und :value")
			}
		default:
			return m, errAt(a.pos, "Unbekannter Vergleicher „%s“", a.strs[0])
		}
	}
	return m, nil
}

// checkKeys validates constant regular expressions at compile time.
func (c *compiler) checkKeys(m matchSpec, keys arg) *Error {
	if m.typ != "regex" {
		return nil
	}
	for _, k := range keys.strs {
		if len(k) > 1024 {
			return errAt(keys.pos, "Regulärer Ausdruck ist zu lang")
		}
		if c.caps["variables"] && strings.Contains(k, "${") {
			continue
		}
		c.regexes++
		if c.regexes > 64 {
			return errAt(keys.pos, "Zu viele reguläre Ausdrücke im Skript")
		}
		if _, err := compileRegex(k, m.comparator); err != nil {
			return errAt(keys.pos, "Ungültiger regulärer Ausdruck „%s“", k)
		}
	}
	return nil
}

func compileRegex(expr, comparator string) (*regexp.Regexp, error) {
	if comparator == "i;ascii-casemap" {
		expr = "(?i)" + expr
	}
	return regexp.Compile(expr)
}

// ---- actions ----

func (c *compiler) action(n *cmdNode) (command, *Error) {
	switch n.name {
	case "stop", "discard":
		if len(n.args) > 0 {
			return nil, errAt(n.args[0].pos, "„%s“ erwartet keine Argumente", n.name)
		}
		if n.name == "stop" {
			return cmdStop{}, nil
		}
		return cmdDiscard{}, nil
	case "keep":
		tags, rest, err := splitTags(n.name, n.args, map[string]paramKind{"flags": pStrings})
		if err != nil {
			return nil, err
		}
		if err := positional(n.name, n.pos, rest); err != nil {
			return nil, err
		}
		k := cmdKeep{}
		if a, ok := tags["flags"]; ok {
			if err := c.need(a.pos, "imap4flags", ":flags"); err != nil {
				return nil, err
			}
			k.flags, k.hasFlags = a.strs, true
		}
		return k, nil
	case "fileinto":
		if err := c.need(n.pos, "fileinto", "fileinto"); err != nil {
			return nil, err
		}
		tags, rest, err := splitTags(n.name, n.args, map[string]paramKind{"copy": pNone, "create": pNone, "flags": pStrings})
		if err != nil {
			return nil, err
		}
		if err := positional(n.name, n.pos, rest, pString); err != nil {
			return nil, err
		}
		f := cmdFileinto{mailbox: rest[0].strs[0]}
		if a, ok := tags["copy"]; ok {
			if err := c.need(a.pos, "copy", ":copy"); err != nil {
				return nil, err
			}
			f.copy = true
		}
		if a, ok := tags["create"]; ok {
			if err := c.need(a.pos, "mailbox", ":create"); err != nil {
				return nil, err
			}
			f.create = true
		}
		if a, ok := tags["flags"]; ok {
			if err := c.need(a.pos, "imap4flags", ":flags"); err != nil {
				return nil, err
			}
			f.flags, f.hasFlags = a.strs, true
		}
		if f.mailbox == "" {
			return nil, errAt(rest[0].pos, "Der Ordnername darf nicht leer sein")
		}
		return f, nil
	case "redirect":
		tags, rest, err := splitTags(n.name, n.args, map[string]paramKind{"copy": pNone})
		if err != nil {
			return nil, err
		}
		if err := positional(n.name, n.pos, rest, pString); err != nil {
			return nil, err
		}
		r := cmdRedirect{addr: rest[0].strs[0]}
		if a, ok := tags["copy"]; ok {
			if err := c.need(a.pos, "copy", ":copy"); err != nil {
				return nil, err
			}
			r.copy = true
		}
		if !(c.caps["variables"] && strings.Contains(r.addr, "${")) && !validAddress(r.addr) {
			return nil, errAt(rest[0].pos, "„%s“ ist keine gültige E-Mail-Adresse", r.addr)
		}
		return r, nil
	case "reject", "ereject":
		if err := c.need(n.pos, n.name, n.name); err != nil {
			return nil, err
		}
		if err := positional(n.name, n.pos, n.args, pString); err != nil {
			return nil, err
		}
		return cmdReject{reason: n.args[0].strs[0], ereject: n.name == "ereject"}, nil
	case "setflag", "addflag", "removeflag":
		if err := c.need(n.pos, "imap4flags", n.name); err != nil {
			return nil, err
		}
		f := cmdFlags{op: n.name}
		switch len(n.args) {
		case 1:
			if err := positional(n.name, n.pos, n.args, pStrings); err != nil {
				return nil, err
			}
			f.flags = n.args[0].strs
		case 2:
			if err := positional(n.name, n.pos, n.args, pString, pStrings); err != nil {
				return nil, err
			}
			if err := c.need(n.args[0].pos, "variables", "Variablennamen in "+n.name); err != nil {
				return nil, err
			}
			f.varName = strings.ToLower(n.args[0].strs[0])
			if !validVarName(f.varName) {
				return nil, errAt(n.args[0].pos, "Ungültiger Variablenname „%s“", n.args[0].strs[0])
			}
			f.flags = n.args[1].strs
		default:
			return nil, errAt(n.pos, "„%s“ erwartet [Variablenname] und eine Liste von Flags", n.name)
		}
		return f, nil
	case "set":
		if err := c.need(n.pos, "variables", "set"); err != nil {
			return nil, err
		}
		mods := map[string]paramKind{"lower": pNone, "upper": pNone, "lowerfirst": pNone, "upperfirst": pNone,
			"quotewildcard": pNone, "quoteregex": pNone, "length": pNone}
		tags, rest, err := splitTags(n.name, n.args, mods)
		if err != nil {
			return nil, err
		}
		if err := positional(n.name, n.pos, rest, pString, pString); err != nil {
			return nil, err
		}
		s := cmdSet{name: strings.ToLower(rest[0].strs[0]), value: rest[1].strs[0]}
		if !validVarName(s.name) {
			return nil, errAt(rest[0].pos, "Ungültiger Variablenname „%s“", rest[0].strs[0])
		}
		if tags["lower"].tag != "" && tags["upper"].tag != "" || tags["lowerfirst"].tag != "" && tags["upperfirst"].tag != "" {
			return nil, errAt(n.pos, "„set“: widersprüchliche Modifikatoren")
		}
		// Modifiers apply in the order of RFC 5229 section 4.1 precedence.
		for _, m := range []string{"lower", "upper", "lowerfirst", "upperfirst", "quotewildcard", "quoteregex", "length"} {
			if _, ok := tags[m]; ok {
				s.mods = append(s.mods, m)
			}
		}
		return s, nil
	case "vacation":
		if err := c.need(n.pos, "vacation", "vacation"); err != nil {
			return nil, err
		}
		tags, rest, err := splitTags(n.name, n.args, map[string]paramKind{
			"days": pNumber, "subject": pString, "from": pString, "addresses": pStrings, "mime": pNone, "handle": pString,
		})
		if err != nil {
			return nil, err
		}
		if err := positional(n.name, n.pos, rest, pString); err != nil {
			return nil, err
		}
		v := cmdVacation{reason: rest[0].strs[0]}
		if a, ok := tags["days"]; ok {
			v.days, v.hasDays = a.num, true
		}
		if a, ok := tags["subject"]; ok {
			v.subject = &a.strs[0]
		}
		if a, ok := tags["from"]; ok {
			v.from = &a.strs[0]
			if !(c.caps["variables"] && strings.Contains(a.strs[0], "${")) && !validAddress(a.strs[0]) {
				return nil, errAt(a.pos, "„%s“ ist keine gültige Absenderadresse", a.strs[0])
			}
		}
		if a, ok := tags["addresses"]; ok {
			v.addresses = a.strs
		}
		if a, ok := tags["handle"]; ok {
			v.handle = &a.strs[0]
		}
		_, v.mime = tags["mime"]
		return v, nil
	case "if", "elsif", "else", "require":
		// handled by block
	}
	return nil, errAt(n.pos, "Unbekannter Befehl „%s“", n.name)
}

func validAddress(s string) bool {
	a, err := mail.ParseAddress(s)
	return err == nil && strings.Contains(a.Address, "@")
}

func validVarName(s string) bool {
	if s == "" || len(s) > 64 || !isIdentStart(s[0]) {
		return false
	}
	for i := 0; i < len(s); i++ {
		if !isIdentChar(s[i]) {
			return false
		}
	}
	return true
}

// ---- tests ----

func (c *compiler) test(n *testNode) (test, *Error) {
	noTests := func() *Error {
		if len(n.tests) > 0 {
			return errAt(n.tests[0].pos, "„%s“ erwartet keine untergeordneten Tests", n.name)
		}
		return nil
	}
	switch n.name {
	case "true", "false":
		if len(n.args) > 0 {
			return nil, errAt(n.args[0].pos, "„%s“ erwartet keine Argumente", n.name)
		}
		if err := noTests(); err != nil {
			return nil, err
		}
		return testConst{n.name == "true"}, nil
	case "not":
		if len(n.args) > 0 || len(n.tests) != 1 {
			return nil, errAt(n.pos, "„not“ erwartet genau einen Test")
		}
		t, err := c.test(n.tests[0])
		if err != nil {
			return nil, err
		}
		return testNot{t}, nil
	case "allof", "anyof":
		if len(n.args) > 0 || len(n.tests) == 0 {
			return nil, errAt(n.pos, "„%s“ erwartet eine Liste von Tests in „( … )“", n.name)
		}
		var ts []test
		for _, sub := range n.tests {
			t, err := c.test(sub)
			if err != nil {
				return nil, err
			}
			ts = append(ts, t)
		}
		if n.name == "allof" {
			return testAllof{ts}, nil
		}
		return testAnyof{ts}, nil
	}
	if err := noTests(); err != nil {
		return nil, err
	}
	switch n.name {
	case "exists":
		if err := positional(n.name, n.pos, n.args, pStrings); err != nil {
			return nil, err
		}
		return testExists{n.args[0].strs}, nil
	case "size":
		tags, rest, err := splitTags(n.name, n.args, map[string]paramKind{"over": pNone, "under": pNone})
		if err != nil {
			return nil, err
		}
		_, over := tags["over"]
		_, under := tags["under"]
		if over == under {
			return nil, errAt(n.pos, "„size“ erwartet entweder :over oder :under")
		}
		if err := positional(n.name, n.pos, rest, pNumber); err != nil {
			return nil, err
		}
		return testSize{over: over, limit: rest[0].num}, nil
	case "header":
		tags, rest, err := splitTags(n.name, n.args, matchTags)
		if err != nil {
			return nil, err
		}
		m, err := c.matchSpec(n.name, n.pos, tags)
		if err != nil {
			return nil, err
		}
		if err := positional(n.name, n.pos, rest, pStrings, pStrings); err != nil {
			return nil, err
		}
		if err := c.checkKeys(m, rest[1]); err != nil {
			return nil, err
		}
		return testHeader{m: m, headers: rest[0].strs, keys: rest[1].strs}, nil
	case "address", "envelope":
		if n.name == "envelope" {
			if err := c.need(n.pos, "envelope", "envelope"); err != nil {
				return nil, err
			}
		}
		tags, rest, err := splitTags(n.name, n.args, withTags(matchTags, map[string]paramKind{
			"all": pNone, "localpart": pNone, "domain": pNone, "user": pNone, "detail": pNone,
		}))
		if err != nil {
			return nil, err
		}
		m, err := c.matchSpec(n.name, n.pos, tags)
		if err != nil {
			return nil, err
		}
		t := testAddress{m: m, part: "all", envelope: n.name == "envelope"}
		parts := 0
		for _, p := range []string{"all", "localpart", "domain", "user", "detail"} {
			if a, ok := tags[p]; ok {
				parts++
				t.part = p
				if p == "user" || p == "detail" {
					if err := c.need(a.pos, "subaddress", ":"+p); err != nil {
						return nil, err
					}
				}
			}
		}
		if parts > 1 {
			return nil, errAt(n.pos, "„%s“: nur ein Adressteil (:all, :localpart, :domain, …) ist erlaubt", n.name)
		}
		if err := positional(n.name, n.pos, rest, pStrings, pStrings); err != nil {
			return nil, err
		}
		if err := c.checkKeys(m, rest[1]); err != nil {
			return nil, err
		}
		t.headers, t.keys = rest[0].strs, rest[1].strs
		return t, nil
	case "body":
		if err := c.need(n.pos, "body", "body"); err != nil {
			return nil, err
		}
		tags, rest, err := splitTags(n.name, n.args, withTags(matchTags, map[string]paramKind{"raw": pNone, "text": pNone, "content": pStrings}))
		if err != nil {
			return nil, err
		}
		m, err := c.matchSpec(n.name, n.pos, tags)
		if err != nil {
			return nil, err
		}
		t := testBody{m: m, transform: "text"}
		count := 0
		if _, ok := tags["raw"]; ok {
			t.transform = "raw"
			count++
		}
		if _, ok := tags["text"]; ok {
			count++
		}
		if a, ok := tags["content"]; ok {
			t.transform, t.types = "content", a.strs
			count++
		}
		if count > 1 {
			return nil, errAt(n.pos, "„body“: nur eine Umwandlung (:raw, :text, :content) ist erlaubt")
		}
		if err := positional(n.name, n.pos, rest, pStrings); err != nil {
			return nil, err
		}
		if err := c.checkKeys(m, rest[0]); err != nil {
			return nil, err
		}
		t.keys = rest[0].strs
		return t, nil
	case "string":
		if err := c.need(n.pos, "variables", "string"); err != nil {
			return nil, err
		}
		tags, rest, err := splitTags(n.name, n.args, matchTags)
		if err != nil {
			return nil, err
		}
		m, err := c.matchSpec(n.name, n.pos, tags)
		if err != nil {
			return nil, err
		}
		if err := positional(n.name, n.pos, rest, pStrings, pStrings); err != nil {
			return nil, err
		}
		if err := c.checkKeys(m, rest[1]); err != nil {
			return nil, err
		}
		return testString{m: m, sources: rest[0].strs, keys: rest[1].strs}, nil
	case "hasflag":
		if err := c.need(n.pos, "imap4flags", "hasflag"); err != nil {
			return nil, err
		}
		tags, rest, err := splitTags(n.name, n.args, matchTags)
		if err != nil {
			return nil, err
		}
		m, err := c.matchSpec(n.name, n.pos, tags)
		if err != nil {
			return nil, err
		}
		t := testHasflag{m: m}
		switch len(rest) {
		case 1:
			if err := positional(n.name, n.pos, rest, pStrings); err != nil {
				return nil, err
			}
			t.keys = rest[0].strs
		case 2:
			if err := positional(n.name, n.pos, rest, pStrings, pStrings); err != nil {
				return nil, err
			}
			if err := c.need(rest[0].pos, "variables", "Variablennamen in hasflag"); err != nil {
				return nil, err
			}
			for _, v := range rest[0].strs {
				v = strings.ToLower(v)
				if !validVarName(v) {
					return nil, errAt(rest[0].pos, "Ungültiger Variablenname „%s“", v)
				}
				t.vars = append(t.vars, v)
			}
			t.keys = rest[1].strs
		default:
			return nil, errAt(n.pos, "„hasflag“ erwartet [Variablennamen] und eine Liste von Flags")
		}
		if err := c.checkKeys(m, arg{pos: n.pos, strs: t.keys}); err != nil {
			return nil, err
		}
		return t, nil
	case "date", "currentdate":
		if err := c.need(n.pos, "date", n.name); err != nil {
			return nil, err
		}
		spec := withTags(matchTags, map[string]paramKind{"zone": pString})
		if n.name == "date" {
			spec["originalzone"] = pNone
		}
		tags, rest, err := splitTags(n.name, n.args, spec)
		if err != nil {
			return nil, err
		}
		m, err := c.matchSpec(n.name, n.pos, tags)
		if err != nil {
			return nil, err
		}
		t := testDate{m: m, current: n.name == "currentdate"}
		if a, ok := tags["zone"]; ok {
			t.zone = a.strs[0]
			if _, ok := parseZone(t.zone); !ok {
				return nil, errAt(a.pos, "Ungültige Zeitzone „%s“ (Format +hhmm oder -hhmm)", t.zone)
			}
		}
		if _, ok := tags["originalzone"]; ok {
			if t.zone != "" {
				return nil, errAt(n.pos, "„date“: :zone und :originalzone schließen sich aus")
			}
			t.originalZone = true
		}
		if t.current {
			if err := positional(n.name, n.pos, rest, pString, pStrings); err != nil {
				return nil, err
			}
			t.part, rest = strings.ToLower(rest[0].strs[0]), rest[1:]
		} else {
			if err := positional(n.name, n.pos, rest, pString, pString, pStrings); err != nil {
				return nil, err
			}
			t.header, t.part, rest = rest[0].strs[0], strings.ToLower(rest[1].strs[0]), rest[2:]
		}
		if !dateParts[t.part] {
			return nil, errAt(n.pos, "Unbekannter Datumsteil „%s“", t.part)
		}
		if err := c.checkKeys(m, rest[0]); err != nil {
			return nil, err
		}
		t.keys = rest[0].strs
		return t, nil
	case "mailboxexists":
		if err := c.need(n.pos, "mailbox", "mailboxexists"); err != nil {
			return nil, err
		}
		if err := positional(n.name, n.pos, n.args, pStrings); err != nil {
			return nil, err
		}
		return testMailboxExists{n.args[0].strs}, nil
	}
	return nil, errAt(n.pos, "Unbekannter Test „%s“", n.name)
}
