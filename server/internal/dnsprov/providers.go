package dnsprov

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"strconv"
	"strings"
	"time"

	"postwerk/internal/auth"
)

var timeNow = time.Now

// httpJSON performs a JSON request and decodes the response into out.
func httpJSON(ctx context.Context, hc *http.Client, method, u string, hdr map[string]string, body, out any) error {
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, u, rd)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	resp, err := hc.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 10<<20))
	if resp.StatusCode == 401 || resp.StatusCode == 403 {
		return perr("Anmeldung beim DNS-Anbieter fehlgeschlagen (%s): %s", resp.Status, short(data))
	}
	if resp.StatusCode >= 300 {
		return perr("DNS-Anbieter meldet %s: %s", resp.Status, short(data))
	}
	if out != nil && len(bytes.TrimSpace(data)) > 0 {
		if err := json.Unmarshal(data, out); err != nil {
			return perr("unerwartete Antwort des DNS-Anbieters: %v", err)
		}
	}
	return nil
}

func short(b []byte) string {
	s := strings.TrimSpace(string(b))
	if len(s) > 300 {
		s = s[:300] + "…"
	}
	return s
}

// =====================================================================
// IONOS (Hosting DNS API)
// =====================================================================

var ionosInfo = Info{
	ID: "ionos", Name: "IONOS", Official: true,
	Description: "Offizielle DNS-API von IONOS (1&1) mit API-Schlüssel.",
	DocsURL:     sp("https://developer.hosting.ionos.de/keys"),
	Fields: []Field{{Key: "apiKey", Label: "API-Schlüssel", Type: "password", Required: true,
		Help:        sp("Im IONOS-Entwicklerportal erzeugen. Format: Präfix.Geheimnis"),
		Placeholder: sp("präfix.geheimnis")}},
	Supports: allTypes,
	New: func(c map[string]string, hc *http.Client) (Provider, error) {
		return &ionos{key: c["apiKey"], hc: hc, base: "https://api.hosting.ionos.com/dns/v1"}, nil
	},
}

type ionos struct {
	key, base string
	hc        *http.Client
	zoneID    string
}

func (p *ionos) hdr() map[string]string { return map[string]string{"X-API-Key": p.key} }

func (p *ionos) Zone(ctx context.Context, domain string) (string, error) {
	var zones []struct{ ID, Name string }
	if err := httpJSON(ctx, p.hc, "GET", p.base+"/zones", p.hdr(), nil, &zones); err != nil {
		return "", err
	}
	names := []string{}
	for _, z := range zones {
		names = append(names, z.Name)
	}
	zone, ok := longestSuffix(domain, names)
	if !ok {
		return "", perr("Die Domain %s ist in diesem IONOS-Konto nicht vorhanden", domain)
	}
	for _, z := range zones {
		if strings.EqualFold(strings.TrimSuffix(z.Name, "."), zone) {
			p.zoneID = z.ID
		}
	}
	return zone, nil
}

type ionosRecord struct {
	ID       string `json:"id,omitempty"`
	Name     string `json:"name"`
	Type     string `json:"type"`
	Content  string `json:"content"`
	TTL      int    `json:"ttl,omitempty"`
	Prio     int    `json:"prio,omitempty"`
	Disabled bool   `json:"disabled"`
}

func (p *ionos) List(ctx context.Context, zone string) ([]Record, error) {
	var z struct{ Records []ionosRecord }
	if err := httpJSON(ctx, p.hc, "GET", p.base+"/zones/"+url.PathEscape(p.zoneID), p.hdr(), nil, &z); err != nil {
		return nil, err
	}
	var out []Record
	for _, r := range z.Records {
		out = append(out, ionosToRecord(r, zone))
	}
	return out, nil
}

func ionosToRecord(r ionosRecord, zone string) Record {
	rec := Record{ID: r.ID, Name: relName(r.Name, zone), Type: strings.ToUpper(r.Type), TTL: r.TTL, Priority: r.Prio}
	switch rec.Type {
	case "TXT":
		rec.Value = unquoteTXT(r.Content)
	case "MX", "CNAME":
		rec.Value = dot(r.Content)
	case "SRV":
		f := strings.Fields(r.Content) // "weight port target"
		if len(f) == 3 {
			rec.Value = f[0] + " " + f[1] + " " + dot(f[2])
		} else {
			rec.Value = r.Content
		}
	default:
		rec.Value = r.Content
	}
	return rec
}

func ionosContent(r Record) string {
	switch r.Type {
	case "TXT":
		return `"` + strings.ReplaceAll(r.Value, `"`, `\"`) + `"`
	case "MX", "CNAME":
		return undot(r.Value)
	case "SRV":
		f := strings.Fields(r.Value)
		if len(f) == 3 {
			return f[0] + " " + f[1] + " " + undot(f[2])
		}
	}
	return r.Value
}

func (p *ionos) Apply(ctx context.Context, zone string, ops []Op) []error {
	base := p.base + "/zones/" + url.PathEscape(p.zoneID) + "/records"
	return applyEach(ops, func(op Op) error {
		switch op.Kind {
		case OpCreate:
			r := op.New
			body := []ionosRecord{{Name: fqdn(r.Name, zone), Type: r.Type, Content: ionosContent(r), TTL: r.TTL, Prio: r.Priority}}
			return httpJSON(ctx, p.hc, "POST", base, p.hdr(), body, nil)
		case OpUpdate:
			r := op.New
			body := ionosRecord{Name: fqdn(r.Name, zone), Type: r.Type, Content: ionosContent(r), TTL: r.TTL, Prio: r.Priority}
			return httpJSON(ctx, p.hc, "PUT", base+"/"+url.PathEscape(op.Old.ID), p.hdr(), body, nil)
		default:
			return httpJSON(ctx, p.hc, "DELETE", base+"/"+url.PathEscape(op.Old.ID), p.hdr(), nil, nil)
		}
	})
}

// =====================================================================
// Hetzner (DNS in the Hetzner Cloud API, since 2025)
// =====================================================================

var hetznerInfo = Info{
	ID: "hetzner", Name: "Hetzner", Official: true,
	Description: "DNS in der Hetzner Console über die Cloud-API.",
	DocsURL:     sp("https://docs.hetzner.com/cloud/api/getting-started/generating-api-token/"),
	Fields: []Field{{Key: "token", Label: "API-Token (Hetzner Console)", Type: "password", Required: true,
		Help: sp("Im Projekt der Hetzner Console unter Sicherheit → API-Tokens mit Lese- und Schreibrecht erzeugen. Tokens der alten DNS Console funktionieren nicht mehr.")}},
	Supports: allTypes,
	New: func(c map[string]string, hc *http.Client) (Provider, error) {
		return &hetzner{token: c["token"], hc: hc, base: "https://api.hetzner.cloud/v1"}, nil
	},
}

type hetzner struct {
	token, base string
	hc          *http.Client
	zoneID      string
}

func (p *hetzner) hdr() map[string]string {
	return map[string]string{"Authorization": "Bearer " + p.token}
}

func (p *hetzner) Zone(ctx context.Context, domain string) (string, error) {
	for _, c := range candidates(domain) {
		var resp struct {
			Zones []struct {
				ID   json.Number `json:"id"`
				Name string      `json:"name"`
			} `json:"zones"`
		}
		if err := httpJSON(ctx, p.hc, "GET", p.base+"/zones?name="+url.QueryEscape(c), p.hdr(), nil, &resp); err != nil {
			return "", err
		}
		for _, z := range resp.Zones {
			if strings.EqualFold(strings.TrimSuffix(z.Name, "."), c) {
				p.zoneID = z.ID.String()
				return c, nil
			}
		}
	}
	return "", perr("Keine Zone für %s in diesem Hetzner-Projekt gefunden", domain)
}

type rrset struct {
	Name    string `json:"name"`
	Type    string `json:"type"`
	TTL     *int   `json:"ttl,omitempty"`
	Records []struct {
		Value string `json:"value"`
	} `json:"records"`
}

func (p *hetzner) rrsets(ctx context.Context) ([]rrset, error) {
	var all []rrset
	for page := 1; page < 100; page++ {
		var resp struct {
			RRSets []rrset `json:"rrsets"`
			Meta   struct {
				Pagination struct {
					NextPage *int `json:"next_page"`
				} `json:"pagination"`
			} `json:"meta"`
		}
		u := fmt.Sprintf("%s/zones/%s/rrsets?per_page=50&page=%d", p.base, url.PathEscape(p.zoneID), page)
		if err := httpJSON(ctx, p.hc, "GET", u, p.hdr(), nil, &resp); err != nil {
			return nil, err
		}
		all = append(all, resp.RRSets...)
		if resp.Meta.Pagination.NextPage == nil {
			break
		}
	}
	return all, nil
}

func (p *hetzner) List(ctx context.Context, zone string) ([]Record, error) {
	sets, err := p.rrsets(ctx)
	if err != nil {
		return nil, err
	}
	return expandRRSets(sets, zone), nil
}

func expandRRSets(sets []rrset, zone string) []Record {
	var out []Record
	for _, s := range sets {
		ttl := 0
		if s.TTL != nil {
			ttl = *s.TTL
		}
		for _, r := range s.Records {
			out = append(out, parsePresentation(relName(s.Name, zone), s.Type, r.Value, ttl))
		}
	}
	return out
}

// groupOps computes the final value list per (name,type) for rrset APIs.
type rrKey struct{ name, typ string }

func groupOps(existing []Record, ops []Op) (map[rrKey][]Record, map[rrKey][]int) {
	final := map[rrKey][]Record{}
	owners := map[rrKey][]int{}
	for i, op := range ops {
		r := op.New
		if op.Kind == OpDelete {
			r = op.Old
		}
		k := rrKey{r.Name, r.Type}
		if _, ok := final[k]; !ok {
			cur := []Record{}
			for _, e := range existing {
				if e.Name == k.name && e.Type == k.typ {
					cur = append(cur, e)
				}
			}
			final[k] = cur
		}
		owners[k] = append(owners[k], i)
		switch op.Kind {
		case OpCreate:
			final[k] = append(final[k], op.New)
		case OpUpdate, OpDelete:
			var keep []Record
			for _, e := range final[k] {
				if !(e.Value == op.Old.Value && e.Priority == op.Old.Priority) {
					keep = append(keep, e)
				}
			}
			if op.Kind == OpUpdate {
				keep = append(keep, op.New)
			}
			final[k] = keep
		}
	}
	return final, owners
}

func (p *hetzner) Apply(ctx context.Context, zone string, ops []Op) []error {
	errs := make([]error, len(ops))
	sets, err := p.rrsets(ctx)
	if err != nil {
		return allErr(len(ops), err)
	}
	existing := expandRRSets(sets, zone)
	exists := map[rrKey]bool{}
	for _, s := range sets {
		exists[rrKey{relName(s.Name, zone), strings.ToUpper(s.Type)}] = true
	}
	final, owners := groupOps(existing, ops)
	for k, recs := range final {
		vals := []map[string]string{}
		for _, r := range recs {
			vals = append(vals, map[string]string{"value": presentation(r)})
		}
		path := fmt.Sprintf("%s/zones/%s/rrsets", p.base, url.PathEscape(p.zoneID))
		var e error
		var resp struct {
			Action struct {
				ID json.Number `json:"id"`
			} `json:"action"`
		}
		switch {
		case !exists[k] && len(vals) > 0:
			e = httpJSON(ctx, p.hc, "POST", path, p.hdr(), map[string]any{"name": k.name, "type": k.typ, "ttl": 3600, "records": vals}, &resp)
		case exists[k] && len(vals) > 0:
			e = httpJSON(ctx, p.hc, "POST", path+"/"+url.PathEscape(k.name)+"/"+k.typ+"/actions/set_records", p.hdr(), map[string]any{"records": vals}, &resp)
		case exists[k]:
			e = httpJSON(ctx, p.hc, "DELETE", path+"/"+url.PathEscape(k.name)+"/"+k.typ, p.hdr(), nil, &resp)
		}
		if e == nil && resp.Action.ID != "" {
			e = p.wait(ctx, resp.Action.ID.String())
		}
		for _, i := range owners[k] {
			errs[i] = e
		}
	}
	return errs
}

// wait polls an asynchronous action for up to 20 seconds.
func (p *hetzner) wait(ctx context.Context, id string) error {
	for i := 0; i < 20; i++ {
		var resp struct {
			Action struct {
				Status string `json:"status"`
				Error  *struct {
					Message string `json:"message"`
				} `json:"error"`
			} `json:"action"`
		}
		if err := httpJSON(ctx, p.hc, "GET", p.base+"/zones/actions/"+id, p.hdr(), nil, &resp); err != nil {
			return nil // status unknown; the change was accepted
		}
		switch resp.Action.Status {
		case "success":
			return nil
		case "error":
			if resp.Action.Error != nil {
				return perr("Hetzner: %s", resp.Action.Error.Message)
			}
			return perr("Hetzner meldet einen Fehler bei der Änderung")
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(time.Second):
		}
	}
	return nil
}

// =====================================================================
// Cloudflare
// =====================================================================

var cloudflareInfo = Info{
	ID: "cloudflare", Name: "Cloudflare", Official: true,
	Description: "Cloudflare DNS mit API-Token.",
	DocsURL:     sp("https://dash.cloudflare.com/profile/api-tokens"),
	Fields: []Field{{Key: "token", Label: "API-Token", Type: "password", Required: true,
		Help: sp("Token mit der Berechtigung „Zone → DNS → Bearbeiten“ für die Domain.")}},
	Supports: allTypes,
	Notes:    sp("Einträge werden ohne Proxy (graue Wolke) angelegt – Mailserver dürfen nicht hinter dem Cloudflare-Proxy liegen."),
	New: func(c map[string]string, hc *http.Client) (Provider, error) {
		return &cloudflare{token: c["token"], hc: hc, base: "https://api.cloudflare.com/client/v4"}, nil
	},
}

type cloudflare struct {
	token, base string
	hc          *http.Client
	zoneID      string
}

func (p *cloudflare) hdr() map[string]string {
	return map[string]string{"Authorization": "Bearer " + p.token}
}

type cfResp[T any] struct {
	Success bool `json:"success"`
	Errors  []struct {
		Message string `json:"message"`
	} `json:"errors"`
	Result     T `json:"result"`
	ResultInfo struct {
		TotalPages int `json:"total_pages"`
	} `json:"result_info"`
}

func cfErr[T any](r cfResp[T]) error {
	if r.Success {
		return nil
	}
	msgs := []string{}
	for _, e := range r.Errors {
		msgs = append(msgs, e.Message)
	}
	return perr("Cloudflare: %s", strings.Join(msgs, "; "))
}

func (p *cloudflare) Zone(ctx context.Context, domain string) (string, error) {
	for _, c := range candidates(domain) {
		var r cfResp[[]struct{ ID, Name string }]
		if err := httpJSON(ctx, p.hc, "GET", p.base+"/zones?name="+url.QueryEscape(c), p.hdr(), nil, &r); err != nil {
			return "", err
		}
		if err := cfErr(r); err != nil {
			return "", err
		}
		if len(r.Result) > 0 {
			p.zoneID = r.Result[0].ID
			return c, nil
		}
	}
	return "", perr("Keine Cloudflare-Zone für %s gefunden (Token-Berechtigung prüfen)", domain)
}

type cfRecord struct {
	ID       string         `json:"id,omitempty"`
	Type     string         `json:"type"`
	Name     string         `json:"name"`
	Content  string         `json:"content,omitempty"`
	TTL      int            `json:"ttl"`
	Priority *int           `json:"priority,omitempty"`
	Proxied  *bool          `json:"proxied,omitempty"`
	Data     map[string]any `json:"data,omitempty"`
}

func (p *cloudflare) List(ctx context.Context, zone string) ([]Record, error) {
	var out []Record
	for page := 1; page < 50; page++ {
		var r cfResp[[]cfRecord]
		u := fmt.Sprintf("%s/zones/%s/dns_records?per_page=500&page=%d", p.base, p.zoneID, page)
		if err := httpJSON(ctx, p.hc, "GET", u, p.hdr(), nil, &r); err != nil {
			return nil, err
		}
		if err := cfErr(r); err != nil {
			return nil, err
		}
		for _, c := range r.Result {
			rec := Record{ID: c.ID, Name: relName(c.Name, zone), Type: c.Type, TTL: c.TTL}
			if c.Priority != nil {
				rec.Priority = *c.Priority
			}
			switch c.Type {
			case "TXT":
				rec.Value = unquoteTXT(c.Content)
			case "MX", "CNAME":
				rec.Value = dot(c.Content)
			case "SRV":
				if c.Data != nil {
					rec.Priority = toInt(c.Data["priority"])
					rec.Value = fmt.Sprintf("%d %d %s", toInt(c.Data["weight"]), toInt(c.Data["port"]), dot(fmt.Sprint(c.Data["target"])))
				} else {
					rec = parsePresentation(rec.Name, "SRV", c.Content, c.TTL)
					rec.ID = c.ID
				}
			default:
				rec.Value = c.Content
			}
			out = append(out, rec)
		}
		if page >= r.ResultInfo.TotalPages {
			break
		}
	}
	return out, nil
}

func toInt(v any) int {
	switch n := v.(type) {
	case float64:
		return int(n)
	case int:
		return n
	case string:
		i, _ := strconv.Atoi(n)
		return i
	}
	return 0
}

func cfBody(r Record, zone string) cfRecord {
	no := false
	b := cfRecord{Type: r.Type, Name: fqdn(r.Name, zone), TTL: 1}
	switch r.Type {
	case "MX":
		pr := r.Priority
		b.Priority = &pr
		b.Content = undot(r.Value)
	case "CNAME":
		b.Content = undot(r.Value)
		b.Proxied = &no
	case "A", "AAAA":
		b.Content = r.Value
		b.Proxied = &no
	case "SRV":
		f := strings.Fields(r.Value)
		if len(f) == 3 {
			w, _ := strconv.Atoi(f[0])
			port, _ := strconv.Atoi(f[1])
			b.Data = map[string]any{"priority": r.Priority, "weight": w, "port": port, "target": undot(f[2])}
		}
	default:
		b.Content = r.Value
	}
	return b
}

func (p *cloudflare) Apply(ctx context.Context, zone string, ops []Op) []error {
	base := fmt.Sprintf("%s/zones/%s/dns_records", p.base, p.zoneID)
	return applyEach(ops, func(op Op) error {
		var r cfResp[json.RawMessage]
		var err error
		switch op.Kind {
		case OpCreate:
			err = httpJSON(ctx, p.hc, "POST", base, p.hdr(), cfBody(op.New, zone), &r)
		case OpUpdate:
			err = httpJSON(ctx, p.hc, "PUT", base+"/"+op.Old.ID, p.hdr(), cfBody(op.New, zone), &r)
		default:
			err = httpJSON(ctx, p.hc, "DELETE", base+"/"+op.Old.ID, p.hdr(), nil, &r)
			r.Success = true
		}
		if err != nil {
			return err
		}
		return cfErr(r)
	})
}

// =====================================================================
// deSEC
// =====================================================================

var desecInfo = Info{
	ID: "desec", Name: "deSEC", Official: true,
	Description: "Kostenloser DNS-Hoster mit DNSSEC (desec.io).",
	DocsURL:     sp("https://desec.io/tokens"),
	Fields:      []Field{{Key: "token", Label: "API-Token", Type: "password", Required: true}},
	Supports:    allTypes,
	New: func(c map[string]string, hc *http.Client) (Provider, error) {
		return &desec{token: c["token"], hc: hc, base: "https://desec.io/api/v1"}, nil
	},
}

type desec struct {
	token, base string
	hc          *http.Client
}

func (p *desec) hdr() map[string]string {
	return map[string]string{"Authorization": "Token " + p.token}
}

func (p *desec) Zone(ctx context.Context, domain string) (string, error) {
	var ds []struct{ Name string }
	if err := httpJSON(ctx, p.hc, "GET", p.base+"/domains/", p.hdr(), nil, &ds); err != nil {
		return "", err
	}
	names := []string{}
	for _, d := range ds {
		names = append(names, d.Name)
	}
	z, ok := longestSuffix(domain, names)
	if !ok {
		return "", perr("Die Domain %s ist in diesem deSEC-Konto nicht vorhanden", domain)
	}
	return z, nil
}

type desecRRSet struct {
	Subname string   `json:"subname"`
	Type    string   `json:"type"`
	TTL     int      `json:"ttl,omitempty"`
	Records []string `json:"records"`
}

func (p *desec) List(ctx context.Context, zone string) ([]Record, error) {
	var sets []desecRRSet
	if err := httpJSON(ctx, p.hc, "GET", p.base+"/domains/"+zone+"/rrsets/", p.hdr(), nil, &sets); err != nil {
		return nil, err
	}
	var out []Record
	for _, s := range sets {
		for _, v := range s.Records {
			out = append(out, parsePresentation(relName(s.Subname, zone), s.Type, v, s.TTL))
		}
	}
	return out, nil
}

func (p *desec) Apply(ctx context.Context, zone string, ops []Op) []error {
	existing, err := p.List(ctx, zone)
	if err != nil {
		return allErr(len(ops), err)
	}
	final, _ := groupOps(existing, ops)
	body := []desecRRSet{}
	for k, recs := range final {
		sub := k.name
		if sub == "@" {
			sub = ""
		}
		vals := []string{}
		for _, r := range recs {
			vals = append(vals, presentation(r))
		}
		body = append(body, desecRRSet{Subname: sub, Type: k.typ, TTL: 3600, Records: vals})
	}
	err = httpJSON(ctx, p.hc, "PUT", p.base+"/domains/"+zone+"/rrsets/", p.hdr(), body, nil)
	return allErr(len(ops), err)
}

// =====================================================================
// netcup (CCP DNS API)
// =====================================================================

var netcupInfo = Info{
	ID: "netcup", Name: "netcup", Official: true,
	Description: "DNS-API im netcup Customer Control Panel.",
	DocsURL:     sp("https://www.customercontrolpanel.de/"),
	Fields: []Field{
		{Key: "customerNumber", Label: "Kundennummer", Type: "text", Required: true},
		{Key: "apiKey", Label: "API-Key", Type: "password", Required: true, Help: sp("CCP → Stammdaten → API")},
		{Key: "apiPassword", Label: "API-Passwort", Type: "password", Required: true},
	},
	Supports: allTypes,
	Notes:    sp("netcup übernimmt Änderungen meist innerhalb weniger Minuten in die Zone."),
	New: func(c map[string]string, hc *http.Client) (Provider, error) {
		return &netcup{cust: c["customerNumber"], key: c["apiKey"], pass: c["apiPassword"], hc: hc,
			endpoint: "https://ccp.netcup.net/run/webservice/servers/endpoint.php?JSON"}, nil
	},
}

type netcup struct {
	cust, key, pass, endpoint string
	hc                        *http.Client
	session                   string
}

type netcupRecord struct {
	ID           string `json:"id,omitempty"`
	Hostname     string `json:"hostname"`
	Type         string `json:"type"`
	Priority     string `json:"priority,omitempty"`
	Destination  string `json:"destination"`
	DeleteRecord bool   `json:"deleterecord"`
}

func (p *netcup) call(ctx context.Context, action string, param map[string]any, out any) error {
	param["customernumber"] = p.cust
	param["apikey"] = p.key
	if action != "login" {
		param["apisessionid"] = p.session
	}
	var resp struct {
		Status       string          `json:"status"`
		StatusCode   int             `json:"statuscode"`
		ShortMessage string          `json:"shortmessage"`
		LongMessage  string          `json:"longmessage"`
		ResponseData json.RawMessage `json:"responsedata"`
	}
	if err := httpJSON(ctx, p.hc, "POST", p.endpoint, nil, map[string]any{"action": action, "param": param}, &resp); err != nil {
		return err
	}
	if resp.Status != "success" {
		return perr("netcup: %s %s", resp.ShortMessage, resp.LongMessage)
	}
	if out != nil && len(resp.ResponseData) > 0 && string(resp.ResponseData) != `""` {
		return json.Unmarshal(resp.ResponseData, out)
	}
	return nil
}

func (p *netcup) login(ctx context.Context) error {
	if p.session != "" {
		return nil
	}
	var r struct {
		APISessionID string `json:"apisessionid"`
	}
	if err := p.call(ctx, "login", map[string]any{"apipassword": p.pass}, &r); err != nil {
		return err
	}
	p.session = r.APISessionID
	return nil
}

func (p *netcup) logout() {
	if p.session != "" {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = p.call(ctx, "logout", map[string]any{}, nil)
		p.session = ""
	}
}

func (p *netcup) Zone(ctx context.Context, domain string) (string, error) {
	return registrable(domain), nil
}

func (p *netcup) records(ctx context.Context, zone string) ([]netcupRecord, error) {
	if err := p.login(ctx); err != nil {
		return nil, err
	}
	var r struct {
		DNSRecords []netcupRecord `json:"dnsrecords"`
	}
	if err := p.call(ctx, "infoDnsRecords", map[string]any{"domainname": zone}, &r); err != nil {
		return nil, err
	}
	return r.DNSRecords, nil
}

func (p *netcup) List(ctx context.Context, zone string) ([]Record, error) {
	defer p.logout()
	recs, err := p.records(ctx, zone)
	if err != nil {
		return nil, err
	}
	var out []Record
	for _, r := range recs {
		rec := Record{ID: r.ID, Name: relName(r.Hostname, zone), Type: strings.ToUpper(r.Type)}
		rec.Priority, _ = strconv.Atoi(r.Priority)
		switch rec.Type {
		case "TXT":
			rec.Value = unquoteTXT(r.Destination)
		case "MX", "CNAME":
			rec.Value = dot(r.Destination)
		case "SRV":
			f := strings.Fields(r.Destination)
			if len(f) == 3 {
				rec.Value = f[0] + " " + f[1] + " " + dot(f[2])
			} else {
				rec.Value = r.Destination
			}
		default:
			rec.Value = r.Destination
		}
		out = append(out, rec)
	}
	return out, nil
}

func netcupFrom(r Record) netcupRecord {
	n := netcupRecord{Hostname: r.Name, Type: r.Type, Destination: r.Value}
	switch r.Type {
	case "MX", "CNAME":
		n.Destination = undot(r.Value)
	case "SRV":
		f := strings.Fields(r.Value)
		if len(f) == 3 {
			n.Destination = f[0] + " " + f[1] + " " + undot(f[2])
		}
	}
	if r.Type == "MX" || r.Type == "SRV" {
		n.Priority = strconv.Itoa(r.Priority)
	}
	return n
}

func (p *netcup) Apply(ctx context.Context, zone string, ops []Op) []error {
	defer p.logout()
	if err := p.login(ctx); err != nil {
		return allErr(len(ops), err)
	}
	var set []netcupRecord
	for _, op := range ops {
		switch op.Kind {
		case OpCreate:
			set = append(set, netcupFrom(op.New))
		case OpUpdate:
			n := netcupFrom(op.New)
			n.ID = op.Old.ID
			set = append(set, n)
		case OpDelete:
			n := netcupFrom(op.Old)
			n.ID = op.Old.ID
			n.DeleteRecord = true
			set = append(set, n)
		}
	}
	err := p.call(ctx, "updateDnsRecords", map[string]any{"domainname": zone, "dnsrecordset": map[string]any{"dnsrecords": set}}, nil)
	return allErr(len(ops), err)
}

// =====================================================================
// INWX (DomRobot JSON-RPC)
// =====================================================================

var inwxInfo = Info{
	ID: "inwx", Name: "INWX", Official: true,
	Description: "DomRobot-API von INWX mit Benutzername und Passwort.",
	DocsURL:     sp("https://www.inwx.de/de/help/apidoc"),
	Fields: []Field{
		{Key: "username", Label: "Benutzername", Type: "text", Required: true},
		{Key: "password", Label: "Passwort", Type: "password", Required: true},
		{Key: "totpSecret", Label: "2FA-Geheimnis (optional)", Type: "password", Required: false,
			Help: sp("Nur nötig, wenn die Zwei-Faktor-Anmeldung aktiv ist: das Base32-Geheimnis aus der Einrichtung.")},
	},
	Supports: allTypes,
	New: func(c map[string]string, hc *http.Client) (Provider, error) {
		jar, _ := cookiejar.New(nil)
		h := *hc
		h.Jar = jar
		return &inwx{user: c["username"], pass: c["password"], totp: c["totpSecret"], hc: &h, endpoint: "https://api.domrobot.com/jsonrpc/"}, nil
	},
}

type inwx struct {
	user, pass, totp, endpoint string
	hc                         *http.Client
	loggedIn                   bool
}

func (p *inwx) call(ctx context.Context, method string, params map[string]any, out any) error {
	var resp struct {
		Code    int             `json:"code"`
		Msg     string          `json:"msg"`
		Reason  string          `json:"reason"`
		ResData json.RawMessage `json:"resData"`
	}
	if err := httpJSON(ctx, p.hc, "POST", p.endpoint, nil, map[string]any{"method": method, "params": params}, &resp); err != nil {
		return err
	}
	if resp.Code < 1000 || resp.Code >= 2000 {
		return perr("INWX: %s %s", resp.Msg, resp.Reason)
	}
	if out != nil && len(resp.ResData) > 0 {
		return json.Unmarshal(resp.ResData, out)
	}
	return nil
}

func (p *inwx) login(ctx context.Context) error {
	if p.loggedIn {
		return nil
	}
	var r struct {
		TFA any `json:"tfa"`
	}
	if err := p.call(ctx, "account.login", map[string]any{"user": p.user, "pass": p.pass, "lang": "de"}, &r); err != nil {
		return err
	}
	if tfa := fmt.Sprint(r.TFA); tfa != "" && tfa != "0" && tfa != "<nil>" && tfa != "false" {
		if p.totp == "" {
			return perr("INWX verlangt einen zweiten Faktor – bitte das 2FA-Geheimnis angeben")
		}
		if err := p.call(ctx, "account.unlock", map[string]any{"tan": auth.TOTPCode(p.totp)}, nil); err != nil {
			return err
		}
	}
	p.loggedIn = true
	return nil
}

func (p *inwx) logout() {
	if p.loggedIn {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = p.call(ctx, "account.logout", map[string]any{}, nil)
		p.loggedIn = false
	}
}

func (p *inwx) Zone(ctx context.Context, domain string) (string, error) {
	return registrable(domain), nil
}

func (p *inwx) List(ctx context.Context, zone string) ([]Record, error) {
	defer p.logout()
	if err := p.login(ctx); err != nil {
		return nil, err
	}
	var r struct {
		Record []struct {
			ID      json.Number `json:"id"`
			Name    string      `json:"name"`
			Type    string      `json:"type"`
			Content string      `json:"content"`
			TTL     int         `json:"ttl"`
			Prio    int         `json:"prio"`
		} `json:"record"`
	}
	if err := p.call(ctx, "nameserver.info", map[string]any{"domain": zone}, &r); err != nil {
		return nil, err
	}
	var out []Record
	for _, x := range r.Record {
		rec := Record{ID: x.ID.String(), Name: relName(x.Name, zone), Type: strings.ToUpper(x.Type), TTL: x.TTL, Priority: x.Prio}
		switch rec.Type {
		case "TXT":
			rec.Value = unquoteTXT(x.Content)
		case "MX", "CNAME":
			rec.Value = dot(x.Content)
		case "SRV":
			f := strings.Fields(x.Content)
			if len(f) == 3 {
				rec.Value = f[0] + " " + f[1] + " " + dot(f[2])
			} else {
				rec.Value = x.Content
			}
		default:
			rec.Value = x.Content
		}
		out = append(out, rec)
	}
	return out, nil
}

func inwxContent(r Record) string {
	switch r.Type {
	case "MX", "CNAME":
		return undot(r.Value)
	case "SRV":
		f := strings.Fields(r.Value)
		if len(f) == 3 {
			return f[0] + " " + f[1] + " " + undot(f[2])
		}
	}
	return r.Value
}

func (p *inwx) Apply(ctx context.Context, zone string, ops []Op) []error {
	defer p.logout()
	if err := p.login(ctx); err != nil {
		return allErr(len(ops), err)
	}
	return applyEach(ops, func(op Op) error {
		switch op.Kind {
		case OpCreate:
			name := op.New.Name
			if name == "@" {
				name = ""
			}
			return p.call(ctx, "nameserver.createRecord", map[string]any{"domain": zone, "type": op.New.Type, "name": name,
				"content": inwxContent(op.New), "ttl": 3600, "prio": op.New.Priority}, nil)
		case OpUpdate:
			id, _ := strconv.Atoi(op.Old.ID)
			return p.call(ctx, "nameserver.updateRecord", map[string]any{"id": id, "content": inwxContent(op.New), "prio": op.New.Priority, "ttl": 3600}, nil)
		default:
			id, _ := strconv.Atoi(op.Old.ID)
			return p.call(ctx, "nameserver.deleteRecord", map[string]any{"id": id}, nil)
		}
	})
}
