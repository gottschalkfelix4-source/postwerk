package dnsprov

import (
	"context"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"strings"
	"time"

	"golang.org/x/net/html"

	"postwerk/internal/auth"
)

// STRATO has no official DNS API. This provider drives the customer web
// interface the same way the community project "strato-certbot" does:
// log in, find the package of the domain and edit the TXT/CNAME list of
// the domain. MX and A records are configured elsewhere in the STRATO menu
// and must be set manually.

var stratoInfo = Info{
	ID: "strato", Name: "STRATO", Official: false,
	Description: "Über den STRATO-Kundenlogin (STRATO bietet keine offizielle DNS-API).",
	DocsURL:     sp("https://www.strato.de/apps/CustomerService"),
	Fields: []Field{
		{Key: "username", Label: "Kundennummer oder E-Mail", Type: "text", Required: true},
		{Key: "password", Label: "Passwort", Type: "password", Required: true},
		{Key: "totpSecret", Label: "2FA-Geheimnis (optional)", Type: "password", Required: false,
			Help: sp("Nur bei aktiver Zwei-Faktor-Anmeldung: Base32-Geheimnis der Authenticator-App.")},
		{Key: "totpDevice", Label: "2FA-Gerätename (optional)", Type: "text", Required: false,
			Help: sp("Name des Geräts, wie er bei STRATO in der 2FA-Auswahl steht.")},
	},
	Supports: []string{"TXT", "CNAME"},
	Notes: sp("Inoffizielle Anbindung über die STRATO-Weboberfläche: Sie kann bei Änderungen an der STRATO-Seite ausfallen. " +
		"Gesetzt werden TXT- (SPF, DKIM, DMARC …) und CNAME-Einträge. MX- und A-Einträge müssen im STRATO-Kundenmenü " +
		"(Domains → Domainverwaltung → DNS) von Hand eingetragen werden. Die STRATO-eigene SPF-Einstellung wird dabei abgeschaltet, " +
		"weil Postwerk einen eigenen SPF-Eintrag setzt."),
	New: func(c map[string]string, hc *http.Client) (Provider, error) {
		jar, _ := cookiejar.New(nil)
		h := *hc
		h.Jar = jar
		return &strato{user: c["username"], pass: c["password"], totp: c["totpSecret"], device: c["totpDevice"],
			hc: &h, base: "https://www.strato.de/apps/CustomerService"}, nil
	},
}

type strato struct {
	user, pass, totp, device string
	base                     string
	hc                       *http.Client
	session, packageID       string
}

const stratoUA = "Mozilla/5.0 (Windows NT 10.0; Win64; x64; rv:140.0) Gecko/20100101 Firefox/140.0"

func (p *strato) do(ctx context.Context, method string, params url.Values) (*http.Response, *html.Node, error) {
	var req *http.Request
	var err error
	if method == "GET" {
		u := p.base
		if len(params) > 0 {
			u += "?" + params.Encode()
		}
		req, err = http.NewRequestWithContext(ctx, "GET", u, nil)
	} else {
		req, err = http.NewRequestWithContext(ctx, "POST", p.base, strings.NewReader(params.Encode()))
		if err == nil {
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		}
	}
	if err != nil {
		return nil, nil, err
	}
	req.Header.Set("User-Agent", stratoUA)
	resp, err := p.hc.Do(req)
	if err != nil {
		return nil, nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 10<<20))
	if err != nil {
		return nil, nil, err
	}
	doc, err := html.Parse(strings.NewReader(string(body)))
	if err != nil {
		return nil, nil, err
	}
	return resp, doc, nil
}

func (p *strato) login(ctx context.Context) error {
	if p.session != "" {
		return nil
	}
	if _, _, err := p.do(ctx, "GET", nil); err != nil {
		return perr("STRATO nicht erreichbar: %v", err)
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(1500 * time.Millisecond):
	}
	resp, doc, err := p.do(ctx, "POST", url.Values{"identifier": {p.user}, "passwd": {p.pass}, "action_customer_login.x": {"Login"}})
	if err != nil {
		return perr("STRATO-Anmeldung fehlgeschlagen: %v", err)
	}
	if find(doc, func(n *html.Node) bool {
		return n.Data == "h1" && strings.Contains(text(n), "Zwei-Faktor-Authentifizierung")
	}) != nil {
		if p.totp == "" || p.device == "" {
			return perr("STRATO verlangt die Zwei-Faktor-Anmeldung – bitte 2FA-Geheimnis und Gerätename angeben")
		}
		token := ""
		if in := find(doc, func(n *html.Node) bool {
			return n.Data == "input" && attr(n, "type") == "hidden" && attr(n, "name") == "totp_token"
		}); in != nil {
			token = attr(in, "value")
		}
		pwID := ""
		for _, opt := range findAll(doc, func(n *html.Node) bool { return n.Data == "option" && strings.Contains(attr(n, "value"), p.user) }) {
			if strings.TrimSpace(text(opt)) == strings.TrimSpace(p.device) {
				pwID = attr(opt, "value")
			}
		}
		if token == "" || pwID == "" {
			return perr("STRATO-2FA: Gerät %q nicht gefunden", p.device)
		}
		resp, _, err = p.do(ctx, "POST", url.Values{"identifier": {p.user}, "totp_token": {token}, "action_customer_login.x": {"1"},
			"pw_id": {pwID}, "totp": {auth.TOTPCode(p.totp)}})
		if err != nil {
			return perr("STRATO-2FA fehlgeschlagen: %v", err)
		}
	}
	p.session = resp.Request.URL.Query().Get("sessionID")
	if p.session == "" {
		return perr("STRATO-Anmeldung fehlgeschlagen (Zugangsdaten prüfen)")
	}
	return nil
}

func (p *strato) Zone(ctx context.Context, domain string) (string, error) {
	zone := registrable(domain)
	if err := p.login(ctx); err != nil {
		return "", err
	}
	_, doc, err := p.do(ctx, "GET", url.Values{"sessionID": {p.session}, "cID": {"0"}, "node": {"kds_CustomerEntryPage"}})
	if err != nil {
		return "", err
	}
	table := find(doc, func(n *html.Node) bool { return attr(n, "id") == "package_list" })
	if table != nil {
		for _, tr := range findAll(table, func(n *html.Node) bool { return n.Data == "tr" }) {
			info := find(tr, func(n *html.Node) bool { return hasClass(n, "package-information") })
			if info == nil || !strings.Contains(strings.ToLower(text(info)), zone) {
				continue
			}
			if own := find(tr, func(n *html.Node) bool { return hasClass(n, "jss_with_own_packagename") }); own != nil {
				if a := find(own, func(n *html.Node) bool { return n.Data == "a" }); a != nil {
					if u, err := url.Parse(attr(a, "href")); err == nil {
						p.packageID = u.Query().Get("cID")
					}
				}
			}
		}
	}
	if p.packageID == "" {
		return "", perr("Die Domain %s wurde in keinem STRATO-Paket dieses Kontos gefunden", zone)
	}
	return zone, nil
}

type stratoEntry struct{ prefix, typ, value string }

func (p *strato) entries(ctx context.Context, zone string) ([]stratoEntry, error) {
	_, doc, err := p.do(ctx, "GET", url.Values{"sessionID": {p.session}, "cID": {p.packageID}, "node": {"ManageDomains"},
		"action_show_txt_records": {""}, "vhost": {zone}})
	if err != nil {
		return nil, err
	}
	// Safety: only continue if the TXT/CNAME form is really there, otherwise a
	// later write could wipe all records.
	form := find(doc, func(n *html.Node) bool {
		return (n.Data == "input" || n.Data == "button") && strings.HasPrefix(attr(n, "name"), "action_change_txt_records")
	})
	tmpls := findAll(doc, func(n *html.Node) bool { return n.Data == "div" && hasClass(n, "txt-record-tmpl") })
	if form == nil && len(tmpls) == 0 {
		return nil, perr("Die TXT/CNAME-Seite von STRATO hat sich verändert – bitte die Einträge von Hand setzen")
	}
	var out []stratoEntry
	for _, t := range tmpls {
		prefixIn := find(t, func(n *html.Node) bool { return n.Data == "input" && attr(n, "name") == "prefix" })
		sel := find(t, func(n *html.Node) bool { return n.Data == "select" && attr(n, "name") == "type" })
		ta := find(t, func(n *html.Node) bool { return n.Data == "textarea" && attr(n, "name") == "value" })
		if prefixIn == nil || sel == nil || ta == nil {
			continue // the empty template row
		}
		typ := ""
		if opt := find(sel, func(n *html.Node) bool { return n.Data == "option" && hasAttr(n, "selected") }); opt != nil {
			typ = strings.TrimSpace(text(opt))
		}
		if typ == "" {
			if opt := find(sel, func(n *html.Node) bool { return n.Data == "option" }); opt != nil {
				typ = attr(opt, "value")
			}
		}
		typ = strings.ToUpper(strings.TrimSpace(typ))
		val := text(ta)
		prefix := attr(prefixIn, "value")
		if typ != "TXT" && typ != "CNAME" {
			return nil, perr("Unbekannter Eintragstyp %q auf der STRATO-Seite – Abbruch zum Schutz der Zone", typ)
		}
		if strings.TrimSpace(val) == "" && strings.TrimSpace(prefix) == "" {
			continue
		}
		out = append(out, stratoEntry{prefix: prefix, typ: typ, value: val})
	}
	return out, nil
}

func stratoName(prefix string) string {
	if strings.TrimSpace(prefix) == "" || prefix == "@" {
		return "@"
	}
	return strings.ToLower(strings.TrimSpace(prefix))
}

func (p *strato) List(ctx context.Context, zone string) ([]Record, error) {
	if err := p.login(ctx); err != nil {
		return nil, err
	}
	es, err := p.entries(ctx, zone)
	if err != nil {
		return nil, err
	}
	var out []Record
	for _, e := range es {
		r := Record{Name: stratoName(e.prefix), Type: e.typ, Value: e.value}
		if e.typ == "TXT" {
			r.Value = unquoteTXT(e.value)
		} else {
			r.Value = dot(e.value)
		}
		out = append(out, r)
	}
	return out, nil
}

// Apply rewrites the complete TXT/CNAME list of the domain in one request.
func (p *strato) Apply(ctx context.Context, zone string, ops []Op) []error {
	if err := p.login(ctx); err != nil {
		return allErr(len(ops), err)
	}
	es, err := p.entries(ctx, zone)
	if err != nil {
		return allErr(len(ops), err)
	}
	cur := make([]Record, 0, len(es))
	for _, e := range es {
		r := Record{Name: stratoName(e.prefix), Type: e.typ, Value: e.value}
		if e.typ == "TXT" {
			r.Value = unquoteTXT(e.value)
		}
		cur = append(cur, r)
	}
	for _, op := range ops {
		switch op.Kind {
		case OpCreate:
			cur = append(cur, op.New)
		case OpUpdate, OpDelete:
			for i := range cur {
				if cur[i].Name == op.Old.Name && cur[i].Type == op.Old.Type && strings.TrimSuffix(cur[i].Value, ".") == strings.TrimSuffix(op.Old.Value, ".") {
					if op.Kind == OpUpdate {
						cur[i] = op.New
					} else {
						cur = append(cur[:i], cur[i+1:]...)
					}
					break
				}
			}
		}
	}
	form := url.Values{"sessionID": {p.session}, "cID": {p.packageID}, "node": {"ManageDomains"}, "vhost": {zone}, "spf_type": {"NONE"}}
	for _, r := range cur {
		prefix := r.Name
		if prefix == "@" {
			prefix = ""
		}
		v := r.Value
		if r.Type == "CNAME" {
			v = undot(v)
		}
		form.Add("prefix", prefix)
		form.Add("type", r.Type)
		form.Add("value", v)
	}
	form.Set("action_change_txt_records", "Einstellung+übernehmen")
	if _, _, err := p.do(ctx, "POST", form); err != nil {
		return allErr(len(ops), perr("STRATO: Speichern fehlgeschlagen: %v", err))
	}
	// Verify: read back and check that every new record is present.
	after, err := p.List(ctx, zone)
	if err != nil {
		return allErr(len(ops), err)
	}
	errs := make([]error, len(ops))
	for i, op := range ops {
		if op.Kind == OpDelete {
			continue
		}
		found := false
		for _, r := range after {
			if r.Name == op.New.Name && r.Type == op.New.Type && strings.TrimSuffix(r.Value, ".") == strings.TrimSuffix(op.New.Value, ".") {
				found = true
			}
		}
		if !found {
			errs[i] = perr("STRATO hat den Eintrag nicht übernommen")
		}
	}
	return errs
}

// ---- tiny HTML helpers ----

func attr(n *html.Node, key string) string {
	for _, a := range n.Attr {
		if a.Key == key {
			return a.Val
		}
	}
	return ""
}

func hasAttr(n *html.Node, key string) bool {
	for _, a := range n.Attr {
		if a.Key == key {
			return true
		}
	}
	return false
}

func hasClass(n *html.Node, class string) bool {
	if n.Type != html.ElementNode {
		return false
	}
	for _, c := range strings.Fields(attr(n, "class")) {
		if c == class {
			return true
		}
	}
	return false
}

func findAll(n *html.Node, match func(*html.Node) bool) []*html.Node {
	var out []*html.Node
	var walk func(*html.Node)
	walk = func(x *html.Node) {
		if x.Type == html.ElementNode && match(x) {
			out = append(out, x)
		}
		for c := x.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(n)
	return out
}

func find(n *html.Node, match func(*html.Node) bool) *html.Node {
	if all := findAll(n, match); len(all) > 0 {
		return all[0]
	}
	return nil
}

func text(n *html.Node) string {
	var b strings.Builder
	var walk func(*html.Node)
	walk = func(x *html.Node) {
		if x.Type == html.TextNode {
			b.WriteString(x.Data)
		}
		for c := x.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(n)
	return b.String()
}
