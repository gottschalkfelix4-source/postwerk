// Package dnsprov sets the DNS records Postwerk needs directly at a DNS
// provider (IONOS, Strato, Hetzner, Cloudflare, netcup, INWX, deSEC).
//
// Every provider exposes the same small interface: find the zone, list its
// records and apply create/update/delete operations. The planning logic is
// provider independent and never touches records it does not own.
package dnsprov

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"golang.org/x/net/publicsuffix"

	"postwerk/internal/httpx"
)

// Record is a single resource record in canonical form:
//   - Name is relative to the zone ("@" for the apex)
//   - TXT values are unquoted and joined
//   - host names (MX, CNAME, SRV target) end with a dot
//   - MX/SRV priority lives in Priority, SRV Value is "weight port target."
type Record struct {
	ID       string
	Name     string
	Type     string
	Value    string
	Priority int
	TTL      int
}

type OpKind int

const (
	OpCreate OpKind = iota
	OpUpdate
	OpDelete
)

type Op struct {
	Kind OpKind
	Old  Record // update, delete
	New  Record // create, update
}

type Provider interface {
	// Zone returns the zone (as known to the provider) that contains domain.
	Zone(ctx context.Context, domain string) (string, error)
	List(ctx context.Context, zone string) ([]Record, error)
	// Apply executes ops and returns one error (or nil) per op.
	Apply(ctx context.Context, zone string, ops []Op) []error
}

type Field struct {
	Key         string  `json:"key"`
	Label       string  `json:"label"`
	Type        string  `json:"type"`
	Required    bool    `json:"required"`
	Help        *string `json:"help"`
	Placeholder *string `json:"placeholder"`
}

type Info struct {
	ID          string   `json:"id"`
	Name        string   `json:"name"`
	Official    bool     `json:"official"`
	Description string   `json:"description"`
	DocsURL     *string  `json:"docsUrl"`
	Fields      []Field  `json:"fields"`
	Supports    []string `json:"supports"`
	Notes       *string  `json:"notes"`

	New func(creds map[string]string, hc *http.Client) (Provider, error) `json:"-"`
}

func sp(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

var allTypes = []string{"A", "AAAA", "MX", "TXT", "CNAME", "SRV"}

// Providers lists all supported providers.
func Providers() []Info {
	return []Info{ionosInfo, stratoInfo, hetznerInfo, cloudflareInfo, netcupInfo, inwxInfo, desecInfo}
}

func Lookup(id string) (Info, bool) {
	for _, p := range Providers() {
		if p.ID == id {
			return p, true
		}
	}
	return Info{}, false
}

// New validates credentials against the field list and builds the provider.
func New(id string, creds map[string]string) (Provider, Info, error) {
	info, ok := Lookup(id)
	if !ok {
		return nil, info, fmt.Errorf("unbekannter DNS-Anbieter %q", id)
	}
	clean := map[string]string{}
	for _, f := range info.Fields {
		v := strings.TrimSpace(creds[f.Key])
		if f.Required && v == "" {
			return nil, info, fmt.Errorf("%s fehlt", f.Label)
		}
		clean[f.Key] = v
	}
	p, err := info.New(clean, &http.Client{Timeout: 30 * time.Second, CheckRedirect: httpx.SameHostRedirect})
	return p, info, err
}

// ProviderError marks errors reported by the provider (auth, API errors).
type ProviderError struct{ Msg string }

func (e *ProviderError) Error() string { return e.Msg }

func perr(format string, args ...any) error { return &ProviderError{Msg: fmt.Sprintf(format, args...)} }

func IsProviderError(err error) bool {
	var pe *ProviderError
	return errors.As(err, &pe)
}

// ---- name and value helpers ----

func fqdn(name, zone string) string {
	if name == "@" || name == "" {
		return zone
	}
	return name + "." + zone
}

// relName converts a (possibly fully qualified) record name to zone-relative form.
func relName(name, zone string) string {
	n := strings.ToLower(strings.TrimSuffix(strings.TrimSpace(name), "."))
	z := strings.ToLower(strings.TrimSuffix(zone, "."))
	switch {
	case n == "" || n == "@" || n == z:
		return "@"
	case strings.HasSuffix(n, "."+z):
		return strings.TrimSuffix(n, "."+z)
	}
	return n
}

func dot(h string) string {
	h = strings.ToLower(strings.TrimSpace(h))
	if h == "" || strings.HasSuffix(h, ".") {
		return h
	}
	return h + "."
}

func undot(h string) string { return strings.TrimSuffix(h, ".") }

// unquoteTXT turns `"abc" "def"` into `abcdef`; unquoted input is returned as is.
func unquoteTXT(v string) string {
	v = strings.TrimSpace(v)
	if !strings.HasPrefix(v, `"`) {
		return v
	}
	var b strings.Builder
	in := false
	esc := false
	for _, r := range v {
		switch {
		case esc:
			b.WriteRune(r)
			esc = false
		case r == '\\' && in:
			esc = true
		case r == '"':
			in = !in
		case in:
			b.WriteRune(r)
		}
	}
	return b.String()
}

// quoteTXT renders a TXT value in zone file presentation format, split
// into 255-byte strings.
func quoteTXT(v string) string {
	esc := strings.NewReplacer(`\`, `\\`, `"`, `\"`)
	var parts []string
	for len(v) > 255 {
		parts = append(parts, `"`+esc.Replace(v[:255])+`"`)
		v = v[255:]
	}
	parts = append(parts, `"`+esc.Replace(v)+`"`)
	return strings.Join(parts, " ")
}

// presentation renders a record value in zone-file syntax (used by
// rrset-based APIs such as Hetzner and deSEC).
func presentation(r Record) string {
	switch r.Type {
	case "TXT":
		return quoteTXT(r.Value)
	case "MX":
		return strconv.Itoa(r.Priority) + " " + dot(r.Value)
	case "SRV":
		return strconv.Itoa(r.Priority) + " " + r.Value
	case "CNAME":
		return dot(r.Value)
	}
	return r.Value
}

// parsePresentation is the inverse of presentation.
func parsePresentation(name, typ, value string, ttl int) Record {
	r := Record{Name: name, Type: strings.ToUpper(typ), TTL: ttl}
	switch r.Type {
	case "TXT":
		r.Value = unquoteTXT(value)
	case "MX":
		f := strings.Fields(value)
		if len(f) == 2 {
			r.Priority, _ = strconv.Atoi(f[0])
			r.Value = dot(f[1])
		} else {
			r.Value = dot(value)
		}
	case "SRV":
		f := strings.Fields(value)
		if len(f) == 4 {
			r.Priority, _ = strconv.Atoi(f[0])
			r.Value = f[1] + " " + f[2] + " " + dot(f[3])
		} else {
			r.Value = value
		}
	case "CNAME":
		r.Value = dot(value)
	default:
		r.Value = strings.TrimSpace(value)
	}
	return r
}

// registrable returns the registered domain (zone) for providers without a zone listing.
func registrable(domain string) string {
	d := strings.ToLower(strings.TrimSuffix(domain, "."))
	if r, err := publicsuffix.EffectiveTLDPlusOne(d); err == nil {
		return r
	}
	return d
}

// candidates yields domain, parent, grandparent … down to the registrable domain.
func candidates(domain string) []string {
	d := strings.ToLower(strings.TrimSuffix(domain, "."))
	reg := registrable(d)
	var out []string
	for {
		out = append(out, d)
		if d == reg {
			break
		}
		i := strings.IndexByte(d, '.')
		if i < 0 {
			break
		}
		d = d[i+1:]
	}
	return out
}

// longestSuffix picks the zone from a list that best matches domain.
func longestSuffix(domain string, zones []string) (string, bool) {
	d := strings.ToLower(strings.TrimSuffix(domain, "."))
	best := ""
	for _, z := range zones {
		z = strings.ToLower(strings.TrimSuffix(z, "."))
		if (d == z || strings.HasSuffix(d, "."+z)) && len(z) > len(best) {
			best = z
		}
	}
	return best, best != ""
}

// applyEach runs fn for each op (helper for per-record APIs).
func applyEach(ops []Op, fn func(Op) error) []error {
	errs := make([]error, len(ops))
	for i, op := range ops {
		errs[i] = fn(op)
	}
	return errs
}

func allErr(n int, err error) []error {
	errs := make([]error, n)
	for i := range errs {
		errs[i] = err
	}
	return errs
}

func sortRecords(rs []Record) {
	sort.SliceStable(rs, func(i, j int) bool {
		if rs[i].Name != rs[j].Name {
			return rs[i].Name < rs[j].Name
		}
		return rs[i].Type < rs[j].Type
	})
}
