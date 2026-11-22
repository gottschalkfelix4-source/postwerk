package dav

import (
	"encoding/xml"
	"errors"
	"io"
	"net/http"
	"sort"
	"strconv"
	"strings"
)

// XML namespaces used by WebDAV, CalDAV, CardDAV and common extensions.
const (
	nsDAV    = "DAV:"
	nsCalDAV = "urn:ietf:params:xml:ns:caldav"
	nsCard   = "urn:ietf:params:xml:ns:carddav"
	nsCS     = "http://calendarserver.org/ns/"
	nsApple  = "http://apple.com/ns/ical/"
)

var prefixes = map[string]string{nsDAV: "d", nsCalDAV: "c", nsCard: "card", nsCS: "cs", nsApple: "ical"}

func name(ns, local string) xml.Name { return xml.Name{Space: ns, Local: local} }

// node is a minimal DOM of a request body.
type node struct {
	Name     xml.Name
	Attr     []xml.Attr
	Children []*node
	Text     string
}

func (n *node) child(ns, local string) *node {
	if n == nil {
		return nil
	}
	for _, c := range n.Children {
		if c.Name.Space == ns && c.Name.Local == local {
			return c
		}
	}
	return nil
}

func (n *node) children(ns, local string) []*node {
	if n == nil {
		return nil
	}
	var out []*node
	for _, c := range n.Children {
		if c.Name.Space == ns && c.Name.Local == local {
			out = append(out, c)
		}
	}
	return out
}

func (n *node) attr(local string) string {
	if n == nil {
		return ""
	}
	for _, a := range n.Attr {
		if a.Name.Local == local {
			return a.Value
		}
	}
	return ""
}

const maxXMLBody = 1 << 20

var errBadXML = errors.New("malformed XML request body")

// parseBody reads an XML request body into a DOM; an empty body yields nil.
func parseBody(r *http.Request) (*node, error) {
	if r.Body == nil {
		return nil, nil
	}
	dec := xml.NewDecoder(io.LimitReader(r.Body, maxXMLBody))
	var stack []*node
	// Text is collected per open element in a builder: appending to a string
	// per character-data token would copy quadratically on bodies split into
	// many small tokens (e.g. text interleaved with comments).
	var texts []*strings.Builder
	var root *node
	for {
		tok, err := dec.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, errBadXML
		}
		switch t := tok.(type) {
		case xml.StartElement:
			if len(stack) > 32 {
				return nil, errBadXML
			}
			n := &node{Name: t.Name, Attr: t.Attr}
			if len(stack) == 0 {
				if root != nil {
					return nil, errBadXML
				}
				root = n
			} else {
				p := stack[len(stack)-1]
				p.Children = append(p.Children, n)
			}
			stack = append(stack, n)
			texts = append(texts, &strings.Builder{})
		case xml.EndElement:
			if len(stack) == 0 {
				return nil, errBadXML
			}
			stack[len(stack)-1].Text = texts[len(texts)-1].String()
			stack = stack[:len(stack)-1]
			texts = texts[:len(texts)-1]
		case xml.CharData:
			if len(texts) > 0 {
				texts[len(texts)-1].Write(t)
			}
		}
	}
	if len(stack) != 0 {
		return nil, errBadXML
	}
	return root, nil
}

// ---- response writing ----

// esc escapes text for XML content. Carriage returns are written as
// character references so CRLF line endings of iCalendar/vCard data survive
// XML end-of-line normalization; characters invalid in XML are dropped.
func esc(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		switch {
		case r == '&':
			b.WriteString("&amp;")
		case r == '<':
			b.WriteString("&lt;")
		case r == '>':
			b.WriteString("&gt;")
		case r == '"':
			b.WriteString("&quot;")
		case r == '\r':
			b.WriteString("&#13;")
		case r == '\n' || r == '\t':
			b.WriteRune(r)
		case r < 0x20 || r == 0xFFFE || r == 0xFFFF:
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}

// tag renders an element with optional inner XML.
func tag(n xml.Name, inner string) string {
	p, ok := prefixes[n.Space]
	var open, close string
	if ok {
		open, close = p+":"+n.Local, p+":"+n.Local
		if inner == "" {
			return "<" + open + "/>"
		}
		return "<" + open + ">" + inner + "</" + close + ">"
	}
	if n.Space == "" {
		if inner == "" {
			return "<" + n.Local + "/>"
		}
		return "<" + n.Local + ">" + inner + "</" + n.Local + ">"
	}
	decl := `x:` + n.Local + ` xmlns:x="` + esc(n.Space) + `"`
	if inner == "" {
		return "<" + decl + "/>"
	}
	return "<" + decl + ">" + inner + "</x:" + n.Local + ">"
}

func hrefTag(p string) string { return tag(name(nsDAV, "href"), esc(p)) }

// propstat is one property with its rendered value.
type propstat struct {
	Name  xml.Name
	Value string
}

// response is one <d:response> of a multistatus.
type response struct {
	Href    string
	Status  int // response-level status (no propstat), e.g. 404 in sync reports
	Found   []propstat
	Missing []xml.Name
	Denied  []xml.Name
}

func statusLine(code int) string {
	return "HTTP/1.1 " + strconv.Itoa(code) + " " + http.StatusText(code)
}

func (r *response) render(b *strings.Builder) {
	b.WriteString("<d:response>")
	b.WriteString(hrefTag(r.Href))
	if r.Status != 0 {
		b.WriteString(tag(name(nsDAV, "status"), statusLine(r.Status)))
	}
	group := func(code int, props []propstat) {
		if len(props) == 0 {
			return
		}
		b.WriteString("<d:propstat><d:prop>")
		for _, p := range props {
			b.WriteString(tag(p.Name, p.Value))
		}
		b.WriteString("</d:prop>")
		b.WriteString(tag(name(nsDAV, "status"), statusLine(code)))
		b.WriteString("</d:propstat>")
	}
	if r.Status == 0 {
		if len(r.Found) == 0 && len(r.Missing) == 0 && len(r.Denied) == 0 {
			b.WriteString(tag(name(nsDAV, "status"), statusLine(http.StatusOK)))
		}
		group(http.StatusOK, r.Found)
		names := func(ns []xml.Name) []propstat {
			out := make([]propstat, len(ns))
			for i, n := range ns {
				out[i] = propstat{Name: n}
			}
			return out
		}
		group(http.StatusNotFound, names(r.Missing))
		group(http.StatusForbidden, names(r.Denied))
	}
	b.WriteString("</d:response>")
}

func multistatusHeader() string {
	keys := make([]string, 0, len(prefixes))
	for ns := range prefixes {
		keys = append(keys, ns)
	}
	sort.Strings(keys)
	var b strings.Builder
	b.WriteString(`<?xml version="1.0" encoding="utf-8"?>` + "\n<d:multistatus")
	for _, ns := range keys {
		b.WriteString(` xmlns:` + prefixes[ns] + `="` + ns + `"`)
	}
	b.WriteString(">")
	return b.String()
}

// writeMultistatus sends a 207 response.
func writeMultistatus(w http.ResponseWriter, resps []response, syncToken string) {
	var b strings.Builder
	b.WriteString(multistatusHeader())
	for i := range resps {
		resps[i].render(&b)
	}
	if syncToken != "" {
		b.WriteString(tag(name(nsDAV, "sync-token"), esc(syncToken)))
	}
	b.WriteString("</d:multistatus>")
	w.Header().Set("Content-Type", "application/xml; charset=utf-8")
	w.WriteHeader(http.StatusMultiStatus)
	_, _ = io.WriteString(w, b.String())
}

// writeError sends a DAV error body with a precondition element
// (RFC 4918 section 16).
func writeError(w http.ResponseWriter, code int, cond xml.Name, inner string) {
	var b strings.Builder
	b.WriteString(`<?xml version="1.0" encoding="utf-8"?>` + "\n<d:error")
	for ns, p := range prefixes {
		b.WriteString(` xmlns:` + p + `="` + ns + `"`)
	}
	b.WriteString(">" + tag(cond, inner) + "</d:error>")
	w.Header().Set("Content-Type", "application/xml; charset=utf-8")
	w.WriteHeader(code)
	_, _ = io.WriteString(w, b.String())
}
