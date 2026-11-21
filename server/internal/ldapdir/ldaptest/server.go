// Package ldaptest provides a minimal in-process LDAP server for tests. It
// understands simple binds, searches (AND, OR, NOT, equality, presence and
// substring filters) and unbinds, which is all ldapdir needs.
package ldaptest

import (
	"net"
	"strings"
	"sync"

	ber "github.com/go-asn1-ber/asn1-ber"
	"github.com/go-ldap/ldap/v3"
)

// Entry is a directory object.
type Entry struct {
	DN       string
	Password string // for simple binds; empty = cannot bind
	Attrs    map[string][]string
}

// Server is a fake LDAP server listening on 127.0.0.1.
type Server struct {
	mu      sync.Mutex
	entries []Entry
	ln      net.Listener
	filters []string
	binds   []string
}

// Start listens on a random local port.
func Start(entries ...Entry) (*Server, error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	s := &Server{entries: entries, ln: ln}
	go s.serve()
	return s, nil
}

// SetEntries replaces the directory content.
func (s *Server) SetEntries(entries ...Entry) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.entries = entries
}

func (s *Server) snapshot() []Entry {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]Entry(nil), s.entries...)
}

// URL returns the ldap:// URL of the server.
func (s *Server) URL() string { return "ldap://" + s.ln.Addr().String() }

// Close stops the server.
func (s *Server) Close() { s.ln.Close() }

// Filters returns all search filters received so far (as strings).
func (s *Server) Filters() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.filters...)
}

// Binds returns the DNs of all bind requests received so far.
func (s *Server) Binds() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.binds...)
}

func (s *Server) serve() {
	for {
		c, err := s.ln.Accept()
		if err != nil {
			return
		}
		go s.handle(c)
	}
}

const (
	appBindRequest   = 0
	appBindResponse  = 1
	appUnbind        = 2
	appSearchRequest = 3
	appSearchEntry   = 4
	appSearchDone    = 5
)

func (s *Server) handle(c net.Conn) {
	defer c.Close()
	for {
		p, err := ber.ReadPacket(c)
		if err != nil || len(p.Children) < 2 {
			return
		}
		id, _ := p.Children[0].Value.(int64)
		op := p.Children[1]
		switch op.Tag {
		case appBindRequest:
			dn, _ := op.Children[1].Value.(string)
			pw := op.Children[2].Data.String()
			s.mu.Lock()
			s.binds = append(s.binds, dn)
			s.mu.Unlock()
			code := int64(ldap.LDAPResultInvalidCredentials)
			if dn == "" && pw == "" {
				code = ldap.LDAPResultSuccess
			} else if e := s.find(dn); e != nil && e.Password != "" && e.Password == pw {
				code = ldap.LDAPResultSuccess
			}
			s.write(c, id, result(appBindResponse, code))
		case appUnbind:
			return
		case appSearchRequest:
			base, _ := op.Children[0].Value.(string)
			scope, _ := op.Children[1].Value.(int64)
			sizeLimit, _ := op.Children[3].Value.(int64)
			filter := op.Children[6]
			if f, err := ldap.DecompileFilter(filter); err == nil {
				s.mu.Lock()
				s.filters = append(s.filters, f)
				s.mu.Unlock()
			}
			var attrs []string
			for _, a := range op.Children[7].Children {
				v, _ := a.Value.(string)
				attrs = append(attrs, v)
			}
			var found []*Entry
			all := s.snapshot()
			for i := range all {
				e := &all[i]
				if !inScope(e.DN, base, scope) || !match(e, filter) {
					continue
				}
				found = append(found, e)
			}
			if len(found) == 0 && scope == ldap.ScopeBaseObject && s.find(base) == nil {
				s.write(c, id, result(appSearchDone, ldap.LDAPResultNoSuchObject))
				continue
			}
			code := int64(ldap.LDAPResultSuccess)
			if sizeLimit > 0 && int64(len(found)) > sizeLimit {
				found = found[:sizeLimit]
				code = ldap.LDAPResultSizeLimitExceeded
			}
			for _, e := range found {
				s.write(c, id, entryPacket(e, attrs))
			}
			s.write(c, id, result(appSearchDone, code))
		default:
			return
		}
	}
}

func (s *Server) find(dn string) *Entry {
	all := s.snapshot()
	for i := range all {
		if strings.EqualFold(all[i].DN, dn) {
			return &all[i]
		}
	}
	return nil
}

func (s *Server) write(c net.Conn, id int64, op *ber.Packet) {
	p := ber.Encode(ber.ClassUniversal, ber.TypeConstructed, ber.TagSequence, nil, "LDAP Response")
	p.AppendChild(ber.NewInteger(ber.ClassUniversal, ber.TypePrimitive, ber.TagInteger, id, "Message ID"))
	p.AppendChild(op)
	_, _ = c.Write(p.Bytes())
}

func result(tag ber.Tag, code int64) *ber.Packet {
	op := ber.Encode(ber.ClassApplication, ber.TypeConstructed, tag, nil, "Result")
	op.AppendChild(ber.NewInteger(ber.ClassUniversal, ber.TypePrimitive, ber.TagEnumerated, code, "resultCode"))
	op.AppendChild(ber.NewString(ber.ClassUniversal, ber.TypePrimitive, ber.TagOctetString, "", "matchedDN"))
	op.AppendChild(ber.NewString(ber.ClassUniversal, ber.TypePrimitive, ber.TagOctetString, "", "diagnosticMessage"))
	return op
}

func entryPacket(e *Entry, attrs []string) *ber.Packet {
	op := ber.Encode(ber.ClassApplication, ber.TypeConstructed, appSearchEntry, nil, "Search Result Entry")
	op.AppendChild(ber.NewString(ber.ClassUniversal, ber.TypePrimitive, ber.TagOctetString, e.DN, "objectName"))
	list := ber.Encode(ber.ClassUniversal, ber.TypeConstructed, ber.TagSequence, nil, "attributes")
	all := len(attrs) == 0
	want := map[string]bool{}
	for _, a := range attrs {
		if a == "*" {
			all = true
		}
		want[strings.ToLower(a)] = true
	}
	for name, vals := range e.Attrs {
		if !all && !want[strings.ToLower(name)] {
			continue
		}
		a := ber.Encode(ber.ClassUniversal, ber.TypeConstructed, ber.TagSequence, nil, "attribute")
		a.AppendChild(ber.NewString(ber.ClassUniversal, ber.TypePrimitive, ber.TagOctetString, name, "type"))
		set := ber.Encode(ber.ClassUniversal, ber.TypeConstructed, ber.TagSet, nil, "vals")
		for _, v := range vals {
			set.AppendChild(ber.NewString(ber.ClassUniversal, ber.TypePrimitive, ber.TagOctetString, v, "value"))
		}
		a.AppendChild(set)
		list.AppendChild(a)
	}
	op.AppendChild(list)
	return op
}

func inScope(dn, base string, scope int64) bool {
	dn, base = strings.ToLower(dn), strings.ToLower(base)
	switch scope {
	case ldap.ScopeBaseObject:
		return dn == base
	case ldap.ScopeSingleLevel:
		rest, ok := strings.CutSuffix(dn, ","+base)
		return ok && !strings.Contains(rest, ",")
	default:
		return base == "" || dn == base || strings.HasSuffix(dn, ","+base)
	}
}

func values(e *Entry, attr string) []string {
	if strings.EqualFold(attr, "dn") {
		return []string{e.DN}
	}
	for k, v := range e.Attrs {
		if strings.EqualFold(k, attr) {
			return v
		}
	}
	return nil
}

// match evaluates an RFC 4511 filter packet against an entry.
func match(e *Entry, f *ber.Packet) bool {
	switch f.Tag {
	case ldap.FilterAnd:
		for _, c := range f.Children {
			if !match(e, c) {
				return false
			}
		}
		return true
	case ldap.FilterOr:
		for _, c := range f.Children {
			if match(e, c) {
				return true
			}
		}
		return false
	case ldap.FilterNot:
		return len(f.Children) == 1 && !match(e, f.Children[0])
	case ldap.FilterEqualityMatch:
		attr, _ := f.Children[0].Value.(string)
		want, _ := f.Children[1].Value.(string)
		for _, v := range values(e, attr) {
			if strings.EqualFold(v, want) {
				return true
			}
		}
		return false
	case ldap.FilterPresent:
		return len(values(e, f.Data.String())) > 0 || strings.EqualFold(f.Data.String(), "objectClass")
	case ldap.FilterSubstrings:
		attr, _ := f.Children[0].Value.(string)
		for _, v := range values(e, attr) {
			if substringMatch(strings.ToLower(v), f.Children[1].Children) {
				return true
			}
		}
		return false
	}
	return false
}

func substringMatch(v string, parts []*ber.Packet) bool {
	for _, p := range parts {
		s := strings.ToLower(p.Data.String())
		switch p.Tag {
		case ldap.FilterSubstringsInitial:
			if !strings.HasPrefix(v, s) {
				return false
			}
			v = v[len(s):]
		case ldap.FilterSubstringsAny:
			i := strings.Index(v, s)
			if i < 0 {
				return false
			}
			v = v[i+len(s):]
		case ldap.FilterSubstringsFinal:
			if !strings.HasSuffix(v, s) {
				return false
			}
		}
	}
	return true
}
