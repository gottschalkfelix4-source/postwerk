package dav

import (
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/emersion/go-ical"
	"github.com/emersion/go-vcard"

	"postwerk/internal/pim"
	"postwerk/internal/store"
)

func (q *request) report() error {
	body, err := parseBody(q.r)
	if err != nil {
		return err
	}
	if body == nil {
		return errStatus(http.StatusBadRequest, "REPORT ohne Body")
	}
	if q.t.kind != resCollection && q.t.kind != resObject {
		return errCond(http.StatusForbidden, nsDAV, "supported-report", "")
	}
	c, err := q.collection()
	if err != nil {
		return err
	}
	cal := c.Kind == store.KindCalendar
	switch {
	case body.Name == name(nsDAV, "sync-collection") && q.t.kind == resCollection:
		return q.syncCollection(c, body)
	case cal && body.Name == name(nsCalDAV, "calendar-multiget"), !cal && body.Name == name(nsCard, "addressbook-multiget"):
		return q.multiget(c, body)
	case cal && body.Name == name(nsCalDAV, "calendar-query"):
		return q.calendarQuery(c, body)
	case !cal && body.Name == name(nsCard, "addressbook-query"):
		return q.addressbookQuery(c, body)
	}
	return errCond(http.StatusForbidden, nsDAV, "supported-report", "")
}

// objectsInScope returns the objects addressed by the request (one object
// or the whole collection).
func (q *request) objectsInScope(c *store.Collection) ([]*store.DavObject, error) {
	if q.t.kind == resObject {
		o, err := q.h.Store.GetObject(q.ctx(), c, q.t.name)
		if err != nil {
			return nil, err
		}
		return []*store.DavObject{o}, nil
	}
	return q.h.Store.ListObjects(q.ctx(), c)
}

func (q *request) multiget(c *store.Collection, body *node) error {
	req := parsePropRequest(body)
	base := collPath(c, q.acct.Email)
	var resps []response
	for _, h := range body.children(nsDAV, "href") {
		raw := strings.TrimSpace(h.Text)
		u, err := url.Parse(raw)
		if err != nil {
			resps = append(resps, response{Href: raw, Status: http.StatusBadRequest})
			continue
		}
		t, ok := parsePath(u.Path)
		if !ok || t.kind != resObject || t.collKind != c.Kind || t.slug != c.Slug || t.email != store.Normalize(q.acct.Email) {
			resps = append(resps, response{Href: raw, Status: http.StatusNotFound})
			continue
		}
		o, err := q.h.Store.GetObject(q.ctx(), c, t.name)
		if err != nil {
			resps = append(resps, response{Href: base + url.PathEscape(t.name), Status: http.StatusNotFound})
			continue
		}
		resps = append(resps, q.objectProps(c, o).respond(objPath(c, q.acct.Email, o.Name), req))
	}
	writeMultistatus(q.w, resps, "")
	return nil
}

func (q *request) syncCollection(c *store.Collection, body *node) error {
	since, ok := parseSyncToken(c, body.child(nsDAV, "sync-token").textOrEmpty())
	if !ok {
		return errCond(http.StatusForbidden, nsDAV, "valid-sync-token", "")
	}
	// The token reflects the state read before the changes, so concurrent
	// writes are reported (again) in the next sync rather than lost.
	token := syncToken(c)
	changed, deleted, err := q.h.Store.DAVChanges(q.ctx(), c, since)
	if err != nil {
		return err
	}
	req := parsePropRequest(body)
	var resps []response
	for _, o := range changed {
		resps = append(resps, q.objectProps(c, o).respond(objPath(c, q.acct.Email, o.Name), req))
	}
	present := map[string]bool{}
	for _, o := range changed {
		present[o.Name] = true
	}
	for _, n := range deleted {
		if !present[n] {
			resps = append(resps, response{Href: objPath(c, q.acct.Email, n), Status: http.StatusNotFound})
		}
	}
	writeMultistatus(q.w, resps, token)
	return nil
}

func (n *node) textOrEmpty() string {
	if n == nil {
		return ""
	}
	return n.Text
}

// ---- calendar-query (RFC 4791 section 7.8) ----

func (q *request) calendarQuery(c *store.Collection, body *node) error {
	filter := body.child(nsCalDAV, "filter")
	var root *node
	if filter != nil {
		root = filter.child(nsCalDAV, "comp-filter")
	}
	objs, err := q.objectsInScope(c)
	if err != nil {
		return err
	}
	req := parsePropRequest(body)
	var resps []response
	for _, o := range objs {
		if root != nil {
			cal, err := pim.ParseCalendar(o.Data)
			if err != nil {
				continue
			}
			if !matchComp(root, cal.Component, cal) {
				continue
			}
		}
		resps = append(resps, q.objectProps(c, o).respond(objPath(c, q.acct.Email, o.Name), req))
	}
	writeMultistatus(q.w, resps, "")
	return nil
}

func parseUTC(s string) (time.Time, bool) {
	if s == "" {
		return time.Time{}, false
	}
	t, err := time.ParseInLocation("20060102T150405Z", s, time.UTC)
	return t, err == nil
}

// timeRange returns the bounds of a <time-range> element (open bounds are
// far in the past/future).
func timeRange(n *node) (time.Time, time.Time, bool) {
	tr := n.child(nsCalDAV, "time-range")
	if tr == nil {
		return time.Time{}, time.Time{}, false
	}
	start, ok1 := parseUTC(tr.attr("start"))
	end, ok2 := parseUTC(tr.attr("end"))
	if !ok1 {
		start = time.Date(1, 1, 1, 0, 0, 0, 0, time.UTC)
	}
	if !ok2 {
		end = time.Date(9999, 1, 1, 0, 0, 0, 0, time.UTC)
	}
	return start, end, true
}

// matchComp evaluates a comp-filter against a component.
func matchComp(f *node, comp *ical.Component, cal *ical.Calendar) bool {
	if !strings.EqualFold(f.attr("name"), comp.Name) {
		return false
	}
	if f.child(nsCalDAV, "is-not-defined") != nil {
		return false
	}
	if start, end, ok := timeRange(f); ok {
		if !compInRange(comp, cal, start, end) {
			return false
		}
	}
	for _, cf := range f.children(nsCalDAV, "comp-filter") {
		found := false
		for _, child := range comp.Children {
			if strings.EqualFold(child.Name, cf.attr("name")) && matchComp(cf, child, cal) {
				found = true
				break
			}
		}
		if cf.child(nsCalDAV, "is-not-defined") != nil {
			has := false
			for _, child := range comp.Children {
				if strings.EqualFold(child.Name, cf.attr("name")) {
					has = true
				}
			}
			found = !has
		}
		if !found {
			return false
		}
	}
	for _, pf := range f.children(nsCalDAV, "prop-filter") {
		if !matchICalProp(pf, comp) {
			return false
		}
	}
	return true
}

// compInRange implements the time-range test of RFC 4791 section 9.9,
// including recurrences.
func compInRange(comp *ical.Component, cal *ical.Calendar, start, end time.Time) bool {
	switch comp.Name {
	case ical.CompEvent, ical.CompToDo, ical.CompJournal:
	default:
		return true
	}
	// Only the master is expanded; overrides are part of the expansion.
	if comp.Props.Get(ical.PropRecurrenceID) != nil {
		return false
	}
	if comp.Props.Get(ical.PropDateTimeStart) == nil && comp.Props.Get(ical.PropDue) == nil {
		// Tasks without dates match every range (RFC 4791 section 9.9).
		return comp.Name == ical.CompToDo
	}
	// Floating times are evaluated as UTC; widen by the largest offset.
	return len(pim.Expand(cal, start.Add(-14*time.Hour), end.Add(14*time.Hour))) > 0
}

func matchICalProp(pf *node, comp *ical.Component) bool {
	props := comp.Props.Values(strings.ToUpper(pf.attr("name")))
	if pf.child(nsCalDAV, "is-not-defined") != nil {
		return len(props) == 0
	}
	if len(props) == 0 {
		return false
	}
	for i := range props {
		p := &props[i]
		if matchOneICalProp(pf, p) {
			return true
		}
	}
	return false
}

func matchOneICalProp(pf *node, p *ical.Prop) bool {
	if start, end, ok := timeRange(pf); ok {
		w, err := pim.PropWhen(p)
		if err != nil || w.T.Before(start.Add(-14*time.Hour)) || !w.T.Before(end.Add(14*time.Hour)) {
			return false
		}
	}
	if tm := pf.child(nsCalDAV, "text-match"); tm != nil && !textMatch(tm, p.Value) {
		return false
	}
	for _, paf := range pf.children(nsCalDAV, "param-filter") {
		v := p.Params.Get(strings.ToUpper(paf.attr("name")))
		if paf.child(nsCalDAV, "is-not-defined") != nil {
			if v != "" {
				return false
			}
			continue
		}
		if v == "" {
			return false
		}
		if tm := paf.child(nsCalDAV, "text-match"); tm != nil && !textMatch(tm, v) {
			return false
		}
	}
	return true
}

// textMatch implements text-match with the i;ascii-casemap/i;unicode-casemap
// (case-insensitive, default) and i;octet collations.
func textMatch(tm *node, value string) bool {
	needle := tm.Text
	if tm.attr("collation") != "i;octet" {
		needle, value = strings.ToLower(needle), strings.ToLower(value)
	}
	var ok bool
	switch tm.attr("match-type") {
	case "equals":
		ok = value == needle
	case "starts-with":
		ok = strings.HasPrefix(value, needle)
	case "ends-with":
		ok = strings.HasSuffix(value, needle)
	default:
		ok = strings.Contains(value, needle)
	}
	if neg := tm.attr("negate-condition"); neg == "yes" {
		ok = !ok
	}
	return ok
}

// ---- addressbook-query (RFC 6352 section 8.6) ----

func (q *request) addressbookQuery(c *store.Collection, body *node) error {
	filter := body.child(nsCard, "filter")
	limit := 0
	if l := body.child(nsCard, "limit"); l != nil {
		limit, _ = strconv.Atoi(strings.TrimSpace(l.child(nsCard, "nresults").textOrEmpty()))
	}
	objs, err := q.objectsInScope(c)
	if err != nil {
		return err
	}
	req := parsePropRequest(body)
	var resps []response
	for _, o := range objs {
		if filter != nil {
			card, err := pim.ParseCard(o.Data)
			if err != nil || !matchCardFilter(filter, card) {
				continue
			}
		}
		if limit > 0 && len(resps) >= limit {
			break
		}
		resps = append(resps, q.objectProps(c, o).respond(objPath(c, q.acct.Email, o.Name), req))
	}
	writeMultistatus(q.w, resps, "")
	return nil
}

func matchCardFilter(f *node, card vcard.Card) bool {
	pfs := f.children(nsCard, "prop-filter")
	if len(pfs) == 0 {
		return true
	}
	allOf := f.attr("test") == "allof"
	for _, pf := range pfs {
		m := matchCardProp(pf, card)
		if allOf && !m {
			return false
		}
		if !allOf && m {
			return true
		}
	}
	return allOf
}

func matchCardProp(pf *node, card vcard.Card) bool {
	fields := card[strings.ToUpper(pf.attr("name"))]
	if pf.child(nsCard, "is-not-defined") != nil {
		return len(fields) == 0
	}
	if len(fields) == 0 {
		return false
	}
	tms := pf.children(nsCard, "text-match")
	pafs := pf.children(nsCard, "param-filter")
	if len(tms) == 0 && len(pafs) == 0 {
		return true
	}
	allOf := pf.attr("test") == "allof"
	for _, fld := range fields {
		var results []bool
		for _, tm := range tms {
			results = append(results, textMatch(tm, fld.Value))
		}
		for _, paf := range pafs {
			vals := fld.Params[strings.ToUpper(paf.attr("name"))]
			if paf.child(nsCard, "is-not-defined") != nil {
				results = append(results, len(vals) == 0)
				continue
			}
			ok := false
			for _, v := range vals {
				if tm := paf.child(nsCard, "text-match"); tm == nil || textMatch(tm, v) {
					ok = true
				}
			}
			results = append(results, ok)
		}
		match := allOf
		for _, r := range results {
			if allOf && !r {
				match = false
				break
			}
			if !allOf && r {
				match = true
				break
			}
		}
		if match {
			return true
		}
	}
	return false
}
