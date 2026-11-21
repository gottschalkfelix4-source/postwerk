package jmap

import (
	"bufio"
	"encoding/json"
	"html"
	"io"
	"strings"
	"time"
	"unicode"

	"github.com/emersion/go-message/textproto"

	"postwerk/internal/message"
	"postwerk/internal/store"
)

// maxQueryLimit caps the number of ids one Email/query returns.
const maxQueryLimit = 1000

// maxHeaderScan bounds the number of messages a header filter reads.
const maxHeaderScan = 20000

var emailSortOptions = []string{"receivedAt", "sentAt", "size", "from", "to", "subject", "hasKeyword"}

type emailFilter struct {
	c       *call
	terms   []string // positive text terms, used by SearchSnippet/get
	headers map[string][]any
}

func likeEscape(s string) string {
	return "%" + strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(strings.ToLower(s)) + "%"
}

// keywordCond returns an SQL condition testing a keyword on table alias t.
func keywordCond(t, kw string) (string, []any) {
	switch strings.ToLower(kw) {
	case "$seen":
		return t + ".seen = 1", nil
	case "$flagged":
		return t + ".flagged = 1", nil
	case "$answered":
		return t + ".answered = 1", nil
	case "$draft":
		return t + ".draft = 1", nil
	}
	return "EXISTS (SELECT 1 FROM json_each(" + t + ".keywords_json) WHERE lower(value) = ?)", []any{strings.ToLower(kw)}
}

func threadKeyword(mode, kw string) (string, []any) {
	cond, args := keywordCond("t", kw)
	sub := "SELECT 1 FROM messages t WHERE t.account_id = m.account_id AND t.thread_id = m.thread_id AND "
	switch mode {
	case "all":
		return "NOT EXISTS (" + sub + "NOT (" + cond + "))", args
	case "some":
		return "EXISTS (" + sub + cond + ")", args
	}
	return "NOT EXISTS (" + sub + cond + ")", args
}

// compile turns a FilterOperator/FilterCondition into an SQL expression on
// the messages table (alias m).
func (f *emailFilter) compile(raw json.RawMessage, negated bool, depth int) (string, []any, error) {
	if depth > 10 {
		return "", nil, methodErr("unsupportedFilter", "filter nested too deeply")
	}
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(raw, &obj); err != nil {
		return "", nil, invalidArgs("filter must be an object")
	}
	if opRaw, ok := obj["operator"]; ok {
		var op string
		var conds []json.RawMessage
		_ = json.Unmarshal(opRaw, &op)
		if err := json.Unmarshal(obj["conditions"], &conds); err != nil {
			return "", nil, invalidArgs("conditions must be a list")
		}
		var parts []string
		var args []any
		for _, cr := range conds {
			w, a, err := f.compile(cr, negated != (op == "NOT"), depth+1)
			if err != nil {
				return "", nil, err
			}
			parts = append(parts, "("+w+")")
			args = append(args, a...)
		}
		if len(parts) == 0 {
			if op == "OR" {
				return "0", nil, nil
			}
			return "1", nil, nil
		}
		switch op {
		case "AND":
			return strings.Join(parts, " AND "), args, nil
		case "OR":
			return strings.Join(parts, " OR "), args, nil
		case "NOT":
			return "NOT (" + strings.Join(parts, " OR ") + ")", args, nil
		}
		return "", nil, methodErr("unsupportedFilter", "unknown operator %q", op)
	}
	var where []string
	var args []any
	add := func(w string, a ...any) {
		where = append(where, w)
		args = append(args, a...)
	}
	str := func(v json.RawMessage) string {
		var s string
		_ = json.Unmarshal(v, &s)
		return s
	}
	date := func(k string, v json.RawMessage) (int64, error) {
		t, err := time.Parse(time.RFC3339, str(v))
		if err != nil {
			return 0, invalidArgs("%s must be a UTCDate", k)
		}
		return t.Unix(), nil
	}
	for k, v := range obj {
		switch k {
		case "inMailbox":
			id, _ := f.mailboxID(str(v))
			add("m.mailbox_id = ?", id)
		case "inMailboxOtherThan":
			var ids []string
			if err := json.Unmarshal(v, &ids); err != nil {
				return "", nil, invalidArgs("inMailboxOtherThan must be a list of ids")
			}
			if len(ids) == 0 {
				continue
			}
			var ph []string
			for _, s := range ids {
				id, _ := f.mailboxID(s)
				ph = append(ph, "?")
				args = append(args, id)
			}
			where = append(where, "m.mailbox_id NOT IN ("+strings.Join(ph, ",")+")")
		case "before":
			t, err := date(k, v)
			if err != nil {
				return "", nil, err
			}
			add("m.received_at < ?", t)
		case "after":
			t, err := date(k, v)
			if err != nil {
				return "", nil, err
			}
			add("m.received_at >= ?", t)
		case "minSize":
			var n int64
			_ = json.Unmarshal(v, &n)
			add("m.size >= ?", n)
		case "maxSize":
			var n int64
			_ = json.Unmarshal(v, &n)
			add("m.size < ?", n)
		case "hasKeyword", "notKeyword":
			kw := str(v)
			if !validKeyword(kw) {
				return "", nil, invalidArgs("invalid keyword %q", kw)
			}
			cond, a := keywordCond("m", kw)
			if k == "notKeyword" {
				cond = "NOT (" + cond + ")"
			}
			add(cond, a...)
		case "allInThreadHaveKeyword", "someInThreadHaveKeyword", "noneInThreadHaveKeyword":
			kw := str(v)
			if !validKeyword(kw) {
				return "", nil, invalidArgs("invalid keyword %q", kw)
			}
			cond, a := threadKeyword(strings.TrimSuffix(k, "InThreadHaveKeyword"), kw)
			add(cond, a...)
		case "hasAttachment":
			var b bool
			_ = json.Unmarshal(v, &b)
			add("m.has_attachments = ?", b)
		case "text", "body":
			terms := strings.Fields(str(v))
			if !negated {
				f.terms = append(f.terms, terms...)
			}
			for _, t := range terms {
				add(`m.search_text LIKE ? ESCAPE '\'`, likeEscape(t))
			}
		case "from":
			s := likeEscape(str(v))
			add(`(lower(m.from_name) LIKE ? ESCAPE '\' OR lower(m.from_email) LIKE ? ESCAPE '\')`, s, s)
		case "to":
			add(`lower(m.to_json) LIKE ? ESCAPE '\'`, likeEscape(str(v)))
		case "cc":
			add(`lower(m.cc_json) LIKE ? ESCAPE '\'`, likeEscape(str(v)))
		case "subject":
			if !negated {
				f.terms = append(f.terms, strings.Fields(str(v))...)
			}
			add(`lower(m.subject) LIKE ? ESCAPE '\'`, likeEscape(str(v)))
		case "bcc", "header":
			name, value := "Bcc", str(v)
			if k == "header" {
				var hv []string
				if err := json.Unmarshal(v, &hv); err != nil || len(hv) == 0 || len(hv) > 2 || hv[0] == "" {
					return "", nil, invalidArgs("header must be [name] or [name, value]")
				}
				name = hv[0]
				value = ""
				if len(hv) == 2 {
					value = hv[1]
				}
			}
			ids, err := f.headerMatches(name, value)
			if err != nil {
				return "", nil, err
			}
			if len(ids) == 0 {
				add("0")
				continue
			}
			add("m.id IN ("+strings.TrimSuffix(strings.Repeat("?,", len(ids)), ",")+")", ids...)
		default:
			return "", nil, methodErr("unsupportedFilter", "unsupported filter property %q", k)
		}
	}
	if len(where) == 0 {
		return "1", nil, nil
	}
	return strings.Join(where, " AND "), args, nil
}

func (f *emailFilter) mailboxID(s string) (int64, bool) {
	rid, _ := f.c.resolveCreated(s)
	return parseID(prefixMailbox, rid)
}

// headerMatches scans message headers for a header field (and a value it
// contains, case-insensitively).
func (f *emailFilter) headerMatches(name, value string) ([]any, error) {
	key := strings.ToLower(name) + "\x00" + strings.ToLower(value)
	if ids, ok := f.headers[key]; ok {
		return ids, nil
	}
	c := f.c
	var n int
	if err := c.srv.Store.DB.QueryRowContext(c.ctx, `SELECT COUNT(*) FROM messages WHERE account_id = ?`, c.acct.ID).Scan(&n); err != nil {
		return nil, err
	}
	if n > maxHeaderScan {
		return nil, methodErr("unsupportedFilter", "header filters are limited to accounts with at most %d messages", maxHeaderScan)
	}
	rows, err := c.srv.Store.DB.QueryContext(c.ctx, `SELECT id, blob_hash FROM messages WHERE account_id = ?`, c.acct.ID)
	if err != nil {
		return nil, err
	}
	type ref struct {
		id   int64
		hash string
	}
	var refs []ref
	for rows.Next() {
		var r ref
		if rows.Scan(&r.id, &r.hash) == nil {
			refs = append(refs, r)
		}
	}
	rows.Close()
	needle := strings.ToLower(value)
	ids := []any{}
	for _, r := range refs {
		if c.ctx.Err() != nil {
			return nil, c.ctx.Err()
		}
		rc, err := c.srv.Store.Blobs.Open(r.hash)
		if err != nil {
			continue
		}
		h, err := textproto.ReadHeader(bufio.NewReader(io.LimitReader(rc, 256<<10)))
		rc.Close()
		if err != nil {
			continue
		}
		for fs := h.FieldsByKey(name); fs.Next(); {
			if needle == "" || strings.Contains(strings.ToLower(asText(fs.Value())), needle) {
				ids = append(ids, r.id)
				break
			}
		}
	}
	if f.headers == nil {
		f.headers = map[string][]any{}
	}
	f.headers[key] = ids
	return ids, nil
}

func sortExpr(s comparator) (string, []any, error) {
	switch s.Property {
	case "receivedAt", "sentAt":
		return "m.received_at", nil, nil
	case "size":
		return "m.size", nil, nil
	case "from":
		return "lower(COALESCE(NULLIF(m.from_name, ''), m.from_email))", nil, nil
	case "to":
		return "lower(m.to_json)", nil, nil
	case "subject":
		return "lower(m.subject)", nil, nil
	case "hasKeyword":
		if !validKeyword(s.Keyword) {
			return "", nil, invalidArgs("hasKeyword sort needs a valid keyword")
		}
		cond, args := keywordCond("m", s.Keyword)
		return "(" + cond + ")", args, nil
	}
	return "", nil, methodErr("unsupportedSort", "cannot sort by %q", s.Property)
}

func (c *call) emailQuery(raw json.RawMessage) (any, error) {
	var a struct {
		AccountID       string          `json:"accountId"`
		Filter          json.RawMessage `json:"filter"`
		Sort            []comparator    `json:"sort"`
		CollapseThreads bool            `json:"collapseThreads"`
		queryWindow
	}
	if err := decodeArgs(raw, &a); err != nil {
		return nil, err
	}
	if err := c.checkAccount(a.AccountID); err != nil {
		return nil, err
	}
	state, err := c.state(store.TypeEmail)
	if err != nil {
		return nil, err
	}
	f := &emailFilter{c: c}
	where, args := "1", []any{}
	if len(a.Filter) > 0 && string(a.Filter) != "null" {
		if where, args, err = f.compile(a.Filter, false, 0); err != nil {
			return nil, err
		}
	}
	var order []string
	for _, s := range a.Sort {
		expr, sargs, err := sortExpr(s)
		if err != nil {
			return nil, err
		}
		dir := " DESC"
		if s.ascending() {
			dir = " ASC"
		}
		order = append(order, expr+dir)
		args = append(args, sargs...)
	}
	if len(order) == 0 {
		order = append(order, "m.received_at DESC")
	}
	order = append(order, "m.id DESC")
	rows, err := c.srv.Store.DB.QueryContext(c.ctx, `SELECT m.id, m.thread_id FROM messages m WHERE m.account_id = ? AND (`+where+`) ORDER BY `+
		strings.Join(order, ", "), append([]any{c.acct.ID}, args...)...)
	if err != nil {
		return nil, err
	}
	var ids []string
	seen := map[int64]bool{}
	for rows.Next() {
		var id, thread int64
		if err := rows.Scan(&id, &thread); err != nil {
			rows.Close()
			return nil, err
		}
		if a.CollapseThreads {
			if seen[thread] {
				continue
			}
			seen[thread] = true
		}
		ids = append(ids, fmtID(prefixEmail, id))
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	page, pos, limit, err := a.page(ids, maxQueryLimit)
	if err != nil {
		return nil, err
	}
	out := map[string]any{"accountId": a.AccountID, "queryState": state, "canCalculateChanges": false, "position": pos,
		"ids": page, "collapseThreads": a.CollapseThreads}
	if a.CalculateTotal {
		out["total"] = len(ids)
	}
	if a.Limit == nil || *a.Limit > limit {
		out["limit"] = limit
	}
	return out, nil
}

// ---- SearchSnippet/get ----

// highlight HTML-escapes s and wraps occurrences of the terms in <mark>.
func highlight(s string, terms []string) (string, bool) {
	runes := []rune(s)
	lower := make([]rune, len(runes))
	for i, r := range runes {
		lower[i] = unicode.ToLower(r)
	}
	marks := make([]bool, len(runes))
	found := false
	for _, t := range terms {
		tr := []rune(strings.ToLower(t))
		if len(tr) == 0 {
			continue
		}
		for i := 0; i+len(tr) <= len(lower); i++ {
			if string(lower[i:i+len(tr)]) == string(tr) {
				for j := i; j < i+len(tr); j++ {
					marks[j] = true
				}
				found = true
			}
		}
	}
	if !found {
		return "", false
	}
	var b strings.Builder
	in := false
	for i, r := range runes {
		if marks[i] != in {
			if marks[i] {
				b.WriteString("<mark>")
			} else {
				b.WriteString("</mark>")
			}
			in = marks[i]
		}
		b.WriteString(html.EscapeString(string(r)))
	}
	if in {
		b.WriteString("</mark>")
	}
	return b.String(), true
}

// snippet cuts a window of text around the first term occurrence.
func snippet(text string, terms []string) string {
	text = strings.Join(strings.Fields(text), " ")
	runes := []rune(text)
	lower := strings.ToLower(text)
	first := -1
	for _, t := range terms {
		if i := strings.Index(lower, strings.ToLower(t)); i >= 0 && (first < 0 || i < first) {
			first = i
		}
	}
	if first < 0 {
		return ""
	}
	// Convert the byte offset of the lower-cased text to a rune offset.
	pos := len([]rune(lower[:first]))
	start := max(pos-60, 0)
	end := min(start+200, len(runes))
	out := string(runes[start:end])
	if start > 0 {
		out = "…" + out
	}
	if end < len(runes) {
		out += "…"
	}
	return out
}

func (c *call) searchSnippetGet(raw json.RawMessage) (any, error) {
	var a struct {
		AccountID string          `json:"accountId"`
		Filter    json.RawMessage `json:"filter"`
		EmailIDs  []string        `json:"emailIds"`
	}
	if err := decodeArgs(raw, &a); err != nil {
		return nil, err
	}
	if err := c.checkAccount(a.AccountID); err != nil {
		return nil, err
	}
	if len(a.EmailIDs) > maxObjectsInGet {
		return nil, methodErr("requestTooLarge", "at most %d emails per call", maxObjectsInGet)
	}
	f := &emailFilter{c: c}
	if len(a.Filter) > 0 && string(a.Filter) != "null" {
		if _, _, err := f.compile(a.Filter, false, 0); err != nil {
			return nil, err
		}
	}
	var nums []int64
	for _, sid := range a.EmailIDs {
		if id, ok := parseID(prefixEmail, sid); ok {
			nums = append(nums, id)
		}
	}
	rows, err := c.loadEmails(nums)
	if err != nil {
		return nil, err
	}
	list := []any{}
	notFound := []string{}
	for _, sid := range a.EmailIDs {
		id, _ := parseID(prefixEmail, sid)
		e := rows[id]
		if e == nil {
			notFound = append(notFound, sid)
			continue
		}
		item := map[string]any{"emailId": sid, "subject": nil, "preview": nil}
		if len(f.terms) > 0 {
			if data, err := c.srv.Store.Blobs.Get(e.BlobHash); err == nil {
				tree := parseMessage(data)
				if subj, ok := tree.lastHeader("Subject"); ok {
					if h, ok := highlight(asText(subj), f.terms); ok {
						item["subject"] = h
					}
				}
				text, _, _ := tree.bodyLists()
				var body strings.Builder
				for _, p := range text {
					v := p.bodyValue(1 << 20)["value"].(string)
					if p.mediaType == "text/html" {
						v = message.HTMLToText(v)
					}
					body.WriteString(v + " ")
				}
				if sn := snippet(body.String(), f.terms); sn != "" {
					if h, ok := highlight(sn, f.terms); ok {
						item["preview"] = h
					}
				}
			}
		}
		list = append(list, item)
	}
	return map[string]any{"accountId": a.AccountID, "list": list, "notFound": notFound}, nil
}
