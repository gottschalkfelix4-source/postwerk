package imapd

import (
	"bytes"
	"io"
	"strings"
	"time"

	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapserver"
	gomessage "github.com/emersion/go-message"
	"github.com/emersion/go-message/mail"
)

// searchMsg evaluates criteria lazily: the message is only read from disk
// when a header or text criterion needs it.
type searchMsg struct {
	v      *view
	e      *entry
	seqNum uint32
	raw    []byte
	loaded bool
}

func (m *searchMsg) bytes() []byte {
	if !m.loaded {
		m.raw, m.loaded = m.v.load(m.e), true
	}
	return m.raw
}

func (m *searchMsg) entity() *gomessage.Entity {
	ent, _ := gomessage.Read(bytes.NewReader(m.bytes()))
	if ent == nil {
		ent, _ = gomessage.New(gomessage.Header{}, bytes.NewReader(nil))
	}
	return ent
}

func (v *view) search(kind imapserver.NumKind, criteria *imap.SearchCriteria, options *imap.SearchOptions) (*imap.SearchData, error) {
	v.staticCriteria(criteria)
	var (
		data   imap.SearchData
		seqSet imap.SeqSet
		uidSet imap.UIDSet
	)
	for i, e := range v.msgs {
		if e.gone {
			continue
		}
		m := &searchMsg{v: v, e: e, seqNum: uint32(i + 1)}
		if !m.match(criteria) {
			continue
		}
		uidSet.AddNum(e.uid)
		var num uint32
		if kind == imapserver.NumKindUID {
			num = uint32(e.uid)
		} else {
			seqSet.AddNum(m.seqNum)
			num = m.seqNum
		}
		if data.Min == 0 || num < data.Min {
			data.Min = num
		}
		if num > data.Max {
			data.Max = num
		}
		data.Count++
	}
	if kind == imapserver.NumKindUID {
		data.All = uidSet
	} else {
		data.All = seqSet
	}
	if options != nil && options.ReturnSave {
		v.searchRes = uidSet
	}
	return &data, nil
}

// staticCriteria resolves "*" and "$" in the number sets of the criteria.
func (v *view) staticCriteria(c *imap.SearchCriteria) {
	var seqs []imap.SeqSet
	for _, set := range c.SeqNum {
		if imap.IsSearchRes(set) {
			c.UID = append(c.UID, v.searchRes)
			continue
		}
		seqs = append(seqs, staticSeq(set, uint32(len(v.msgs))))
	}
	c.SeqNum = seqs
	var max imap.UID
	if n := len(v.msgs); n > 0 {
		max = v.msgs[n-1].uid
	}
	for i, set := range c.UID {
		if imap.IsSearchRes(set) {
			c.UID[i] = v.searchRes
		} else {
			c.UID[i] = staticUID(set, max)
		}
	}
	for i := range c.Not {
		v.staticCriteria(&c.Not[i])
	}
	for i := range c.Or {
		v.staticCriteria(&c.Or[i][0])
		v.staticCriteria(&c.Or[i][1])
	}
}

func (m *searchMsg) match(c *imap.SearchCriteria) bool {
	for _, set := range c.SeqNum {
		if !set.Contains(m.seqNum) {
			return false
		}
	}
	for _, set := range c.UID {
		if !set.Contains(m.e.uid) {
			return false
		}
	}
	if !matchDate(m.e.received, c.Since, c.Before) {
		return false
	}
	for _, f := range c.Flag {
		if !hasFlag(m.e.flags, f) {
			return false
		}
	}
	for _, f := range c.NotFlag {
		if hasFlag(m.e.flags, f) {
			return false
		}
	}
	if c.Larger != 0 && m.e.size <= c.Larger {
		return false
	}
	if c.Smaller != 0 && m.e.size >= c.Smaller {
		return false
	}
	if len(c.Header) > 0 || !c.SentSince.IsZero() || !c.SentBefore.IsZero() {
		header := mail.Header{Header: m.entity().Header}
		for _, hc := range c.Header {
			if !matchHeaderFields(header.FieldsByKey(hc.Key), hc.Value) {
				return false
			}
		}
		if !c.SentSince.IsZero() || !c.SentBefore.IsZero() {
			t, err := header.Date()
			if err != nil || !matchDate(t, c.SentSince, c.SentBefore) {
				return false
			}
		}
	}
	for _, text := range c.Text {
		if !matchEntity(m.entity(), text, true) {
			return false
		}
	}
	for _, body := range c.Body {
		if !matchEntity(m.entity(), body, false) {
			return false
		}
	}
	for i := range c.Not {
		if m.match(&c.Not[i]) {
			return false
		}
	}
	for i := range c.Or {
		if !m.match(&c.Or[i][0]) && !m.match(&c.Or[i][1]) {
			return false
		}
	}
	return true
}

// matchDate compares dates without time zone, as RFC 3501 requires.
func matchDate(t, since, before time.Time) bool {
	t = time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.UTC)
	if !since.IsZero() && t.Before(since) {
		return false
	}
	if !before.IsZero() && !t.Before(before) {
		return false
	}
	return true
}

func matchHeaderFields(fields gomessage.HeaderFields, pattern string) bool {
	if pattern == "" {
		return fields.Len() > 0
	}
	pattern = strings.ToLower(pattern)
	for fields.Next() {
		v, _ := fields.Text()
		if strings.Contains(strings.ToLower(v), pattern) {
			return true
		}
	}
	return false
}

func matchEntity(e *gomessage.Entity, pattern string, includeHeader bool) bool {
	if pattern == "" {
		return true
	}
	if includeHeader && matchHeaderFields(e.Header.Fields(), pattern) {
		return true
	}
	if mr := e.MultipartReader(); mr != nil {
		for {
			part, err := mr.NextPart()
			if err != nil {
				return false
			}
			if matchEntity(part, pattern, includeHeader) {
				return true
			}
		}
	}
	t, _, err := e.Header.ContentType()
	if err != nil {
		t = "text/plain"
	}
	if !strings.HasPrefix(t, "text/") && !strings.HasPrefix(t, "message/") {
		return false
	}
	buf, err := io.ReadAll(io.LimitReader(e.Body, 10<<20))
	if err != nil {
		return false
	}
	return bytes.Contains(bytes.ToLower(buf), bytes.ToLower([]byte(pattern)))
}
