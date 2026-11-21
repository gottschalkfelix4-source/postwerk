package sieve

import (
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

// Execution limits keep a run cheap regardless of the script.
const (
	defaultMaxActions = 32
	maxSteps          = 20000
	maxVariables      = 128
	maxVariableLen    = 4096
	maxExpandedLen    = 64 << 10
	maxRuntimeRegexes = 32
	maxMatchVars      = 10
	// Vacation :days bounds (RFC 5230 section 4.1).
	defaultVacationDays = 7
	maxVacationDays     = 30
)

// Action is one result of a script run.
type Action struct {
	// Type is keep, fileinto, redirect, discard, reject, ereject or vacation.
	Type     string    `json:"type"`
	Mailbox  string    `json:"mailbox,omitempty"`
	Create   bool      `json:"create,omitempty"`
	Copy     bool      `json:"copy,omitempty"`
	Flags    []string  `json:"flags,omitempty"`
	Address  string    `json:"address,omitempty"`
	Reason   string    `json:"reason,omitempty"`
	Vacation *Vacation `json:"vacation,omitempty"`
	// Implicit marks the implicit keep.
	Implicit bool `json:"implicit,omitempty"`
}

// Vacation holds the parameters of a vacation action (RFC 5230).
type Vacation struct {
	Days      int      `json:"days"`
	Subject   string   `json:"subject,omitempty"`
	From      string   `json:"from,omitempty"`
	Addresses []string `json:"addresses,omitempty"`
	Handle    string   `json:"handle,omitempty"`
	Mime      bool     `json:"mime,omitempty"`
	Reason    string   `json:"reason"`
}

// Result is the outcome of a script run.
type Result struct {
	// Actions are the explicit actions in execution order.
	Actions []Action
	// ImplicitKeep is true when no action cancelled the implicit keep;
	// KeepFlags are the flags it stores the message with.
	ImplicitKeep bool
	KeepFlags    []string
}

// Final returns the explicit actions followed by the implicit keep, if any.
func (r *Result) Final() []Action {
	out := append([]Action(nil), r.Actions...)
	if r.ImplicitKeep {
		out = append(out, Action{Type: "keep", Implicit: true, Flags: r.KeepFlags})
	}
	return out
}

// Env provides the run-time environment of a script.
type Env struct {
	// Now is the current time for currentdate (zero: time.Now()).
	Now time.Time
	// MailboxExists answers the mailboxexists test (nil: no mailbox exists).
	MailboxExists func(name string) bool
	// MaxActions bounds the number of actions (zero: 32).
	MaxActions int
}

type state struct {
	s            *Script
	m            *Message
	env          Env
	vars         map[string]string
	matchVars    []string
	flags        map[string][]string
	actions      []Action
	implicitKeep bool
	redirects    int
	steps        int
	stopped      bool
	vacation     bool
	rejected     bool
	regexes      map[string]*regexp.Regexp
}

func rtErr(format string, args ...any) *Error { return errAt(Pos{}, format, args...) }

// Execute runs a compiled script against a message. On error the result is
// nil and the caller must fall back to the implicit keep (RFC 5228 2.10.6).
func Execute(s *Script, m *Message, env Env) (*Result, error) {
	if env.Now.IsZero() {
		env.Now = time.Now()
	}
	if env.MaxActions <= 0 {
		env.MaxActions = defaultMaxActions
	}
	st := &state{s: s, m: m, env: env, vars: map[string]string{}, flags: map[string][]string{},
		implicitKeep: true, regexes: map[string]*regexp.Regexp{}}
	if err := st.run(s.cmds); err != nil {
		return nil, err
	}
	res := &Result{Actions: st.actions, ImplicitKeep: st.implicitKeep}
	if st.implicitKeep {
		res.KeepFlags = append([]string(nil), st.flags[""]...)
	}
	return res, nil
}

func (st *state) step() error {
	st.steps++
	if st.steps > maxSteps {
		return rtErr("Das Skript ist zu aufwendig (Ausführung abgebrochen)")
	}
	return nil
}

func (st *state) run(cmds []command) error {
	for _, c := range cmds {
		if st.stopped {
			return nil
		}
		if err := st.step(); err != nil {
			return err
		}
		if err := st.exec(c); err != nil {
			return err
		}
	}
	return nil
}

func (st *state) exec(c command) error {
	switch c := c.(type) {
	case *cmdIf:
		for _, b := range c.branches {
			ok, err := st.eval(b.test)
			if err != nil {
				return err
			}
			if ok {
				return st.run(b.block)
			}
		}
		if c.hasElse {
			return st.run(c.elseBody)
		}
	case cmdStop:
		st.stopped = true
	case cmdDiscard:
		st.implicitKeep = false
		return st.add(Action{Type: "discard"})
	case cmdKeep:
		flags := st.actionFlags(c.hasFlags, c.flags)
		st.implicitKeep = false
		return st.add(Action{Type: "keep", Flags: flags})
	case cmdFileinto:
		mb := st.expand(c.mailbox)
		if mb == "" {
			return rtErr("fileinto: leerer Ordnername")
		}
		flags := st.actionFlags(c.hasFlags, c.flags)
		if !c.copy {
			st.implicitKeep = false
		}
		return st.add(Action{Type: "fileinto", Mailbox: mb, Create: c.create, Copy: c.copy, Flags: flags})
	case cmdRedirect:
		addr := strings.TrimSpace(st.expand(c.addr))
		if !validAddress(addr) {
			return rtErr("redirect: „%s“ ist keine gültige E-Mail-Adresse", addr)
		}
		if !c.copy {
			st.implicitKeep = false
		}
		for _, a := range st.actions {
			if a.Type == "redirect" && strings.EqualFold(a.Address, addr) {
				return nil // RFC 5228 4.2: redirecting twice to the same address is a no-op
			}
		}
		st.redirects++
		if st.redirects > MaxRedirects {
			return rtErr("Zu viele Weiterleitungen (höchstens %d pro Nachricht)", MaxRedirects)
		}
		return st.add(Action{Type: "redirect", Address: addr, Copy: c.copy})
	case cmdReject:
		t := "reject"
		if c.ereject {
			t = "ereject"
		}
		if st.rejected {
			return nil
		}
		st.rejected = true
		st.implicitKeep = false
		return st.add(Action{Type: t, Reason: st.expand(c.reason)})
	case cmdFlags:
		list := normFlags(st.expandAll(c.flags))
		cur := st.flags[c.varName]
		switch c.op {
		case "setflag":
			cur = list
		case "addflag":
			cur = normFlags(append(append([]string(nil), cur...), list...))
		case "removeflag":
			var out []string
			for _, f := range cur {
				if !containsFold(list, f) {
					out = append(out, f)
				}
			}
			cur = out
		}
		st.flags[c.varName] = cur
		if c.varName != "" {
			return st.setVar(c.varName, strings.Join(cur, " "))
		}
	case cmdSet:
		return st.setVar(c.name, applyModifiers(st.expand(c.value), c.mods))
	case cmdVacation:
		if st.vacation {
			return rtErr("vacation darf pro Nachricht nur einmal ausgeführt werden")
		}
		st.vacation = true
		v := &Vacation{Days: defaultVacationDays, Reason: st.expand(c.reason), Mime: c.mime}
		if c.hasDays {
			v.Days = int(min(max(c.days, 1), maxVacationDays))
		}
		if c.subject != nil {
			v.Subject = st.expand(*c.subject)
		}
		if c.from != nil {
			v.From = st.expand(*c.from)
		}
		if c.handle != nil {
			v.Handle = st.expand(*c.handle)
		}
		v.Addresses = st.expandAll(c.addresses)
		return st.add(Action{Type: "vacation", Vacation: v})
	}
	return nil
}

// actionFlags returns the flags for keep/fileinto: the explicit :flags list
// or the internal variable.
func (st *state) actionFlags(has bool, explicit []string) []string {
	if has {
		return normFlags(st.expandAll(explicit))
	}
	if !st.s.caps["imap4flags"] {
		return nil
	}
	return append([]string(nil), st.flags[""]...)
}

func (st *state) add(a Action) error {
	if len(st.actions) >= st.env.MaxActions {
		return rtErr("Zu viele Aktionen (höchstens %d)", st.env.MaxActions)
	}
	conflict := func(t string) bool {
		return t == "keep" || t == "fileinto" || t == "redirect" || t == "vacation"
	}
	for _, prev := range st.actions {
		r := prev.Type == "reject" || prev.Type == "ereject"
		if (r && conflict(a.Type)) || ((a.Type == "reject" || a.Type == "ereject") && conflict(prev.Type)) {
			return rtErr("reject kann nicht mit keep, fileinto, redirect oder vacation kombiniert werden")
		}
	}
	st.actions = append(st.actions, a)
	return nil
}

// ---- variables ----

func (st *state) setVar(name, value string) error {
	if _, ok := st.vars[name]; !ok && len(st.vars) >= maxVariables {
		return rtErr("Zu viele Variablen (höchstens %d)", maxVariables)
	}
	st.vars[name] = truncate(value, maxVariableLen)
	return nil
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n]
}

// expand replaces ${name} and ${digit} references (RFC 5229 section 3).
func (st *state) expand(s string) string {
	if !st.s.caps["variables"] || !strings.Contains(s, "${") {
		return s
	}
	var b strings.Builder
	for {
		i := strings.Index(s, "${")
		if i < 0 {
			b.WriteString(s)
			break
		}
		b.WriteString(s[:i])
		rest := s[i+2:]
		j := strings.IndexByte(rest, '}')
		if j < 0 {
			b.WriteString(s[i:])
			break
		}
		name := strings.ToLower(rest[:j])
		switch {
		case name != "" && strings.Trim(name, "0123456789") == "":
			if n, err := strconv.Atoi(name); err == nil && n < len(st.matchVars) {
				b.WriteString(st.matchVars[n])
			}
			s = rest[j+1:]
		case validVarName(name):
			b.WriteString(st.vars[name])
			s = rest[j+1:]
		default:
			// Not a variable reference: keep "${" literally.
			b.WriteString("${")
			s = rest
		}
		if b.Len() > maxExpandedLen {
			break
		}
	}
	return truncate(b.String(), maxExpandedLen)
}

func (st *state) expandAll(list []string) []string {
	if len(list) == 0 {
		return nil
	}
	out := make([]string, len(list))
	for i, s := range list {
		out[i] = st.expand(s)
	}
	return out
}

func applyModifiers(v string, mods []string) string {
	for _, m := range mods {
		switch m {
		case "lower":
			v = strings.ToLower(v)
		case "upper":
			v = strings.ToUpper(v)
		case "lowerfirst", "upperfirst":
			if r, n := utf8.DecodeRuneInString(v); n > 0 {
				if m == "lowerfirst" {
					v = string(unicode.ToLower(r)) + v[n:]
				} else {
					v = string(unicode.ToUpper(r)) + v[n:]
				}
			}
		case "quotewildcard":
			v = strings.NewReplacer(`\`, `\\`, `*`, `\*`, `?`, `\?`).Replace(v)
		case "quoteregex":
			v = regexp.QuoteMeta(v)
		case "length":
			v = strconv.Itoa(utf8.RuneCountInString(v))
		}
	}
	return v
}

// ---- flags ----

// normFlags splits space-separated flag lists and removes duplicates
// (case-insensitive, first spelling wins).
func normFlags(list []string) []string {
	var out []string
	for _, s := range list {
		for _, f := range strings.Fields(s) {
			if !containsFold(out, f) {
				out = append(out, f)
			}
		}
	}
	return out
}

func containsFold(list []string, s string) bool {
	for _, x := range list {
		if strings.EqualFold(x, s) {
			return true
		}
	}
	return false
}

// ---- tests ----

func (st *state) eval(t test) (bool, error) {
	if err := st.step(); err != nil {
		return false, err
	}
	switch t := t.(type) {
	case testConst:
		return t.v, nil
	case testNot:
		ok, err := st.eval(t.t)
		return !ok, err
	case testAllof:
		for _, sub := range t.ts {
			ok, err := st.eval(sub)
			if err != nil || !ok {
				return false, err
			}
		}
		return true, nil
	case testAnyof:
		for _, sub := range t.ts {
			ok, err := st.eval(sub)
			if err != nil || ok {
				return ok, err
			}
		}
		return false, nil
	case testExists:
		for _, h := range st.expandAll(t.headers) {
			if len(st.m.Header(h)) == 0 {
				return false, nil
			}
		}
		return true, nil
	case testSize:
		if t.over {
			return st.m.Size > t.limit, nil
		}
		return st.m.Size < t.limit, nil
	case testHeader:
		var values []string
		for _, h := range st.expandAll(t.headers) {
			values = append(values, st.m.Header(h)...)
		}
		return st.match(t.m, values, t.keys)
	case testAddress:
		var values []string
		for _, h := range st.expandAll(t.headers) {
			var addrs []string
			if t.envelope {
				switch strings.ToLower(h) {
				case "from":
					addrs = []string{st.m.EnvelopeFrom}
				case "to":
					addrs = []string{st.m.EnvelopeTo}
				}
			} else {
				for _, raw := range st.m.rawHeader(h) {
					addrs = append(addrs, addressList(raw)...)
				}
			}
			for _, a := range addrs {
				if t.envelope && a == "" && t.part != "all" {
					continue
				}
				if v, ok := addressPart(a, t.part); ok {
					values = append(values, v)
				}
			}
		}
		return st.match(t.m, values, t.keys)
	case testBody:
		var body string
		switch t.transform {
		case "raw":
			body = st.m.rawBody
		case "text":
			body = st.m.text()
		case "content":
			for _, ct := range st.expandAll(t.types) {
				ct = strings.ToLower(ct)
				if ct == "" || ct == "text" || strings.HasPrefix(ct, "text/") {
					body = st.m.text()
					break
				}
			}
		}
		return st.match(t.m, []string{body}, t.keys)
	case testString:
		values := st.expandAll(t.sources)
		if t.m.typ == "count" {
			var nonEmpty []string
			for _, v := range values {
				if v != "" {
					nonEmpty = append(nonEmpty, v)
				}
			}
			values = nonEmpty
		}
		return st.match(t.m, values, t.keys)
	case testHasflag:
		var values []string
		if len(t.vars) == 0 {
			values = st.flags[""]
		}
		for _, v := range t.vars {
			values = append(values, normFlags([]string{st.vars[v]})...)
		}
		keys := normFlags(st.expandAll(t.keys))
		if t.m.typ == "matches" || t.m.typ == "regex" {
			keys = st.expandAll(t.keys)
		}
		return st.matchExpanded(t.m, values, keys)
	case testDate:
		var tm time.Time
		if t.current {
			tm = st.env.Now
		} else {
			var ok bool
			if tm, ok = st.m.headerDate(st.expand(t.header)); !ok {
				return false, nil
			}
		}
		switch {
		case t.zone != "":
			loc, _ := parseZone(t.zone)
			tm = tm.In(loc)
		case !t.originalZone:
			tm = tm.In(time.Local)
		}
		return st.match(t.m, []string{datePart(tm, t.part)}, t.keys)
	case testMailboxExists:
		if st.env.MailboxExists == nil {
			return false, nil
		}
		for _, n := range st.expandAll(t.names) {
			if !st.env.MailboxExists(n) {
				return false, nil
			}
		}
		return true, nil
	}
	return false, rtErr("Unbekannter Test")
}

func (st *state) match(m matchSpec, values, keys []string) (bool, error) {
	return st.matchExpanded(m, values, st.expandAll(keys))
}

// matchExpanded applies the match type to values and already expanded keys.
func (st *state) matchExpanded(m matchSpec, values, keys []string) (bool, error) {
	switch m.typ {
	case "count":
		n := strconv.Itoa(len(values))
		for _, k := range keys {
			if relation(m.rel, compare(m.comparator, n, k)) {
				return true, nil
			}
		}
		return false, nil
	case "value":
		for _, v := range values {
			for _, k := range keys {
				if relation(m.rel, compare(m.comparator, v, k)) {
					return true, nil
				}
			}
		}
		return false, nil
	}
	for _, v := range values {
		for _, k := range keys {
			if err := st.step(); err != nil {
				return false, err
			}
			switch m.typ {
			case "is":
				if compare(m.comparator, v, k) == 0 {
					return true, nil
				}
			case "contains":
				if strings.Contains(fold(m.comparator, v), fold(m.comparator, k)) {
					return true, nil
				}
			case "matches":
				if ok, caps := globMatch(k, v, m.comparator == "i;ascii-casemap"); ok {
					st.setMatchVars(v, caps)
					return true, nil
				}
			case "regex":
				re, err := st.regex(k, m.comparator)
				if err != nil {
					return false, err
				}
				if sub := re.FindStringSubmatch(v); sub != nil {
					st.setMatchVars(sub[0], sub[1:])
					return true, nil
				}
			}
		}
	}
	return false, nil
}

func (st *state) setMatchVars(whole string, caps []string) {
	if !st.s.caps["variables"] {
		return
	}
	mv := append([]string{whole}, caps...)
	if len(mv) > maxMatchVars {
		mv = mv[:maxMatchVars]
	}
	for i := range mv {
		mv[i] = truncate(mv[i], maxVariableLen)
	}
	st.matchVars = mv
}

func (st *state) regex(expr, comparator string) (*regexp.Regexp, error) {
	key := comparator + "\x00" + expr
	if re, ok := st.regexes[key]; ok {
		return re, nil
	}
	if len(st.regexes) >= maxRuntimeRegexes || len(expr) > 1024 {
		return nil, rtErr("Zu viele oder zu lange reguläre Ausdrücke")
	}
	re, err := compileRegex(expr, comparator)
	if err != nil {
		return nil, rtErr("Ungültiger regulärer Ausdruck „%s“", expr)
	}
	st.regexes[key] = re
	return re, nil
}
