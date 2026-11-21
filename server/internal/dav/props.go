package dav

import (
	"encoding/xml"
	"fmt"
	"net/http"
	"strings"

	"postwerk/internal/store"
)

// propFunc renders a property value (inner XML).
type propFunc func() string

// props is the property set of one resource. Expensive properties are
// excluded from allprop.
type props struct {
	names     []xml.Name
	values    map[xml.Name]propFunc
	expensive map[xml.Name]bool
}

func newProps() *props {
	return &props{values: map[xml.Name]propFunc{}, expensive: map[xml.Name]bool{}}
}

func (p *props) add(n xml.Name, f propFunc) {
	if _, ok := p.values[n]; !ok {
		p.names = append(p.names, n)
	}
	p.values[n] = f
}

func (p *props) val(n xml.Name, v string) { p.add(n, func() string { return v }) }

// propRequest is what a PROPFIND or REPORT asks for.
type propRequest struct {
	all   bool
	names bool
	props []xml.Name
}

// parsePropRequest reads <allprop/>, <propname/> or <prop> from el.
func parsePropRequest(el *node) propRequest {
	if el == nil {
		return propRequest{all: true}
	}
	if el.child(nsDAV, "propname") != nil {
		return propRequest{names: true}
	}
	if p := el.child(nsDAV, "prop"); p != nil {
		req := propRequest{}
		for _, c := range p.Children {
			req.props = append(req.props, c.Name)
		}
		return req
	}
	return propRequest{all: true}
}

// respond builds the response for one resource.
func (p *props) respond(href string, req propRequest) response {
	r := response{Href: href}
	switch {
	case req.names:
		for _, n := range p.names {
			r.Found = append(r.Found, propstat{Name: n})
		}
	case req.all:
		for _, n := range p.names {
			if !p.expensive[n] {
				r.Found = append(r.Found, propstat{Name: n, Value: p.values[n]()})
			}
		}
	default:
		for _, n := range req.props {
			if f, ok := p.values[n]; ok {
				r.Found = append(r.Found, propstat{Name: n, Value: f()})
			} else {
				r.Missing = append(r.Missing, n)
			}
		}
	}
	return r
}

var (
	pResourceType = name(nsDAV, "resourcetype")
	pDisplayName  = name(nsDAV, "displayname")
	pETag         = name(nsDAV, "getetag")
	pSyncToken    = name(nsDAV, "sync-token")
	pCTag         = name(nsCS, "getctag")
	pCalData      = name(nsCalDAV, "calendar-data")
	pAddrData     = name(nsCard, "address-data")
)

func privileges() string {
	var b strings.Builder
	for _, p := range []string{"all", "read", "write", "write-properties", "write-content", "bind", "unbind", "read-current-user-privilege-set"} {
		b.WriteString("<d:privilege>" + tag(name(nsDAV, p), "") + "</d:privilege>")
	}
	return b.String()
}

func reportSet(reports ...xml.Name) string {
	var b strings.Builder
	for _, r := range reports {
		b.WriteString("<d:supported-report><d:report>" + tag(r, "") + "</d:report></d:supported-report>")
	}
	return b.String()
}

// syncToken renders the sync token of a collection state.
func syncToken(c *store.Collection) string {
	return fmt.Sprintf("urn:postwerk:sync:%d:%d", c.ID, c.SyncSeq)
}

// parseSyncToken returns the sequence number of a token issued for c.
func parseSyncToken(c *store.Collection, tok string) (int64, bool) {
	tok = strings.TrimSpace(tok)
	if tok == "" {
		return 0, true
	}
	var id, seq int64
	if _, err := fmt.Sscanf(tok, "urn:postwerk:sync:%d:%d", &id, &seq); err != nil {
		return 0, false
	}
	if id != c.ID || seq < 0 || seq > c.SyncSeq {
		return 0, false
	}
	return seq, true
}

// common adds the properties every resource of the user has.
func (q *request) common(p *props) {
	pp := principalPath(q.acct.Email)
	p.val(name(nsDAV, "current-user-principal"), hrefTag(pp))
	p.val(name(nsDAV, "owner"), hrefTag(pp))
	p.val(name(nsDAV, "current-user-privilege-set"), privileges())
	p.val(name(nsDAV, "principal-collection-set"), hrefTag(Prefix+"/principals/"))
}

func (q *request) displayName() string {
	if n := strings.TrimSpace(q.acct.Name); n != "" {
		return n
	}
	return q.acct.Email
}

// homeProps adds calendar/address book home set information.
func (q *request) homeSets(p *props) {
	p.val(name(nsCalDAV, "calendar-home-set"), hrefTag(homePath(store.KindCalendar, q.acct.Email)))
	p.val(name(nsCard, "addressbook-home-set"), hrefTag(homePath(store.KindAddressBook, q.acct.Email)))
	p.val(name(nsCalDAV, "calendar-user-address-set"), hrefTag("mailto:"+q.acct.Email)+hrefTag(principalPath(q.acct.Email)))
	p.val(name(nsCS, "email-address-set"), tag(name(nsCS, "email-address"), esc(q.acct.Email)))
}

func (q *request) rootProps() *props {
	p := newProps()
	q.common(p)
	p.val(pResourceType, tag(name(nsDAV, "collection"), ""))
	p.val(pDisplayName, "Postwerk")
	return p
}

func (q *request) principalProps() *props {
	p := newProps()
	q.common(p)
	q.homeSets(p)
	p.val(pResourceType, tag(name(nsDAV, "collection"), "")+tag(name(nsDAV, "principal"), ""))
	p.val(pDisplayName, esc(q.displayName()))
	p.val(name(nsDAV, "principal-URL"), hrefTag(principalPath(q.acct.Email)))
	p.val(name(nsDAV, "supported-report-set"), "")
	return p
}

func (q *request) homeProps(kind string) *props {
	p := newProps()
	q.common(p)
	q.homeSets(p)
	p.val(pResourceType, tag(name(nsDAV, "collection"), ""))
	if kind == store.KindCalendar {
		p.val(pDisplayName, "Kalender")
	} else {
		p.val(pDisplayName, "Adressbücher")
	}
	p.val(name(nsDAV, "supported-report-set"), "")
	return p
}

func (q *request) collectionProps(c *store.Collection) *props {
	p := newProps()
	q.common(p)
	p.val(pDisplayName, esc(c.Name))
	p.val(pCTag, esc(fmt.Sprint(c.SyncSeq)))
	p.val(pSyncToken, esc(syncToken(c)))
	p.val(pETag, esc(quoteETag(fmt.Sprintf("c%d-%d", c.ID, c.SyncSeq))))
	sync := name(nsDAV, "sync-collection")
	if c.Kind == store.KindCalendar {
		p.val(pResourceType, tag(name(nsDAV, "collection"), "")+tag(name(nsCalDAV, "calendar"), ""))
		var comps strings.Builder
		for _, comp := range c.Components {
			comps.WriteString(`<c:comp name="` + esc(comp) + `"/>`)
		}
		p.val(name(nsCalDAV, "supported-calendar-component-set"), comps.String())
		p.val(name(nsCalDAV, "supported-calendar-data"), `<c:calendar-data content-type="text/calendar" version="2.0"/>`)
		p.val(name(nsCalDAV, "max-resource-size"), fmt.Sprint(MaxObjectSize))
		p.val(name(nsCalDAV, "calendar-description"), esc(c.Description))
		p.val(name(nsCalDAV, "schedule-calendar-transp"), tag(name(nsCalDAV, "opaque"), ""))
		if c.Color != "" {
			p.val(name(nsApple, "calendar-color"), esc(c.Color))
		}
		p.val(name(nsDAV, "supported-report-set"), reportSet(name(nsCalDAV, "calendar-query"), name(nsCalDAV, "calendar-multiget"), sync))
	} else {
		p.val(pResourceType, tag(name(nsDAV, "collection"), "")+tag(name(nsCard, "addressbook"), ""))
		p.val(name(nsCard, "supported-address-data"), `<card:address-data-type content-type="text/vcard" version="3.0"/>`)
		p.val(name(nsCard, "max-resource-size"), fmt.Sprint(MaxObjectSize))
		p.val(name(nsCard, "addressbook-description"), esc(c.Description))
		p.val(name(nsDAV, "supported-report-set"), reportSet(name(nsCard, "addressbook-query"), name(nsCard, "addressbook-multiget"), sync))
	}
	return p
}

func (q *request) objectProps(c *store.Collection, o *store.DavObject) *props {
	p := newProps()
	q.common(p)
	p.val(pResourceType, "")
	p.val(pETag, esc(quoteETag(o.ETag)))
	p.val(name(nsDAV, "getcontenttype"), esc(contentType(o)))
	p.val(name(nsDAV, "getcontentlength"), fmt.Sprint(len(o.Data)))
	p.val(name(nsDAV, "getlastmodified"), lastModified(o.UpdatedAt))
	data := func() string { return esc(string(o.Data)) }
	if c.Kind == store.KindCalendar {
		p.add(pCalData, data)
		p.expensive[pCalData] = true
	} else {
		p.add(pAddrData, data)
		p.expensive[pAddrData] = true
	}
	return p
}

// ---- PROPFIND ----

func (q *request) depth() int {
	switch strings.TrimSpace(q.r.Header.Get("Depth")) {
	case "0":
		return 0
	default: // "1"; "infinity" is answered like depth 1
		return 1
	}
}

func (q *request) propfind() error {
	body, err := parseBody(q.r)
	if err != nil {
		return err
	}
	if body != nil && body.Name != name(nsDAV, "propfind") {
		return errStatus(http.StatusBadRequest, "propfind erwartet")
	}
	req := parsePropRequest(body)
	depth := q.depth()
	email := q.acct.Email
	var resps []response
	switch q.t.kind {
	case resRoot, resPrincipals, resHomeParent:
		href := Prefix + "/"
		switch q.t.kind {
		case resPrincipals:
			href = Prefix + "/principals/"
		case resHomeParent:
			href = Prefix + "/" + homeSegment(q.t.collKind) + "/"
		}
		resps = append(resps, q.rootProps().respond(href, req))
		if depth > 0 && q.t.kind == resPrincipals {
			resps = append(resps, q.principalProps().respond(principalPath(email), req))
		}
		if depth > 0 && q.t.kind == resHomeParent {
			resps = append(resps, q.homeProps(q.t.collKind).respond(homePath(q.t.collKind, email), req))
		}
	case resPrincipal:
		resps = append(resps, q.principalProps().respond(principalPath(email), req))
	case resHome:
		resps = append(resps, q.homeProps(q.t.collKind).respond(homePath(q.t.collKind, email), req))
		if depth > 0 {
			cols, err := q.h.Store.ListCollections(q.ctx(), q.acct.ID, q.t.collKind)
			if err != nil {
				return err
			}
			for _, c := range cols {
				resps = append(resps, q.collectionProps(c).respond(collPath(c, email), req))
			}
		}
	case resCollection:
		c, err := q.collection()
		if err != nil {
			return err
		}
		resps = append(resps, q.collectionProps(c).respond(collPath(c, email), req))
		if depth > 0 {
			objs, err := q.h.Store.ListObjects(q.ctx(), c)
			if err != nil {
				return err
			}
			for _, o := range objs {
				resps = append(resps, q.objectProps(c, o).respond(objPath(c, email, o.Name), req))
			}
		}
	case resObject:
		c, err := q.collection()
		if err != nil {
			return err
		}
		o, err := q.h.Store.GetObject(q.ctx(), c, q.t.name)
		if err != nil {
			return err
		}
		resps = append(resps, q.objectProps(c, o).respond(objPath(c, email, o.Name), req))
	}
	writeMultistatus(q.w, resps, "")
	return nil
}

// ---- PROPPATCH ----

func (q *request) proppatch() error {
	body, err := parseBody(q.r)
	if err != nil {
		return err
	}
	if body == nil || body.Name != name(nsDAV, "propertyupdate") {
		return errStatus(http.StatusBadRequest, "propertyupdate erwartet")
	}
	var set, all []*node
	for _, op := range body.Children {
		p := op.child(nsDAV, "prop")
		if p == nil {
			continue
		}
		all = append(all, p.Children...)
		if op.Name == name(nsDAV, "set") {
			set = append(set, p.Children...)
		}
	}
	href := q.r.URL.Path
	if q.t.kind == resCollection {
		c, err := q.collection()
		if err != nil {
			return err
		}
		before := *c
		applyCollectionProps(c, set)
		for _, n := range all {
			if n.Name == name(nsApple, "calendar-color") && !hasNode(set, n) {
				c.Color = ""
			}
		}
		if c.Name != before.Name || c.Color != before.Color || c.Description != before.Description {
			if err := q.h.Store.UpdateCollection(q.ctx(), c); err != nil {
				return err
			}
		}
		href = collPath(c, q.acct.Email)
	}
	// Properties without storage (calendar order, default alarms, ...) are
	// accepted so clients do not report errors; they are not persisted.
	r := response{Href: href}
	for _, n := range all {
		r.Found = append(r.Found, propstat{Name: n.Name})
	}
	writeMultistatus(q.w, []response{r}, "")
	return nil
}

func hasNode(list []*node, n *node) bool {
	for _, x := range list {
		if x == n {
			return true
		}
	}
	return false
}
