package dnsprov

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"testing"
)

var longDKIM = "v=DKIM1; k=rsa; p=" + strings.Repeat("MIIBIjANBgkqhkiG9w0BAQEFAAOCAQ8AMIIBCgKCAQEA", 9)

func desired(ipv4 string) []Desired {
	return DesiredRecords("example.com", "mail.example.com", "pw202611", longDKIM, ipv4, "")
}

// ---- fake provider for the planning logic ----

type memProvider struct {
	zone string
	recs []Record
	next int
}

func (m *memProvider) Zone(ctx context.Context, d string) (string, error) { return m.zone, nil }
func (m *memProvider) List(ctx context.Context, z string) ([]Record, error) {
	return append([]Record{}, m.recs...), nil
}
func (m *memProvider) Apply(ctx context.Context, z string, ops []Op) []error {
	return applyEach(ops, func(op Op) error {
		switch op.Kind {
		case OpCreate:
			m.next++
			r := op.New
			r.ID = strconv.Itoa(m.next)
			m.recs = append(m.recs, r)
		case OpUpdate:
			for i := range m.recs {
				if m.recs[i].ID == op.Old.ID {
					id := m.recs[i].ID
					m.recs[i] = op.New
					m.recs[i].ID = id
				}
			}
		case OpDelete:
			for i := range m.recs {
				if m.recs[i].ID == op.Old.ID {
					m.recs = append(m.recs[:i], m.recs[i+1:]...)
					break
				}
			}
		}
		return nil
	})
}

func find1(t *testing.T, p *Plan, key string) Change {
	for _, c := range p.Changes {
		if c.Key == key {
			return c
		}
	}
	t.Fatalf("change %s missing", key)
	return Change{}
}

func TestPlanRespectsForeignRecords(t *testing.T) {
	m := &memProvider{zone: "example.com", recs: []Record{
		{ID: "1", Name: "@", Type: "TXT", Value: "v=spf1 include:_spf.strato.com ~all"},
		{ID: "2", Name: "@", Type: "TXT", Value: "google-site-verification=abc"},
		{ID: "3", Name: "@", Type: "MX", Value: "smtpin.rzone.de.", Priority: 5},
		{ID: "4", Name: "_dmarc", Type: "TXT", Value: "v=DMARC1; p=reject"},
		{ID: "5", Name: "www", Type: "A", Value: "1.2.3.4"},
	}, next: 10}
	info := Info{ID: "mem", Name: "Mem", Supports: allTypes}
	plan, err := MakePlan(context.Background(), m, info, "example.com", desired("203.0.113.10"))
	if err != nil {
		t.Fatal(err)
	}
	spf := find1(t, plan, "spf")
	if spf.Action != "update" || spf.Desired != "v=spf1 mx include:_spf.strato.com ~all" {
		t.Errorf("spf = %+v", spf)
	}
	if mx := find1(t, plan, "mx"); mx.Action != "update" || mx.Warning == nil {
		t.Errorf("mx = %+v", mx)
	}
	if dm := find1(t, plan, "dmarc"); dm.Action != "unchanged" {
		t.Errorf("existing DMARC must be kept, got %+v", dm)
	}
	if dk := find1(t, plan, "dkim"); dk.Action != "create" || dk.Name != "pw202611._domainkey.example.com." {
		t.Errorf("dkim = %+v", dk)
	}
	if a := find1(t, plan, "a"); a.Action != "create" {
		t.Errorf("a = %+v", a)
	}
	keys := []string{}
	for _, c := range plan.Changes {
		keys = append(keys, c.Key)
	}
	res := ApplyPlan(context.Background(), m, plan, keys)
	if len(res.Failed) != 0 {
		t.Fatalf("failed: %+v", res.Failed)
	}
	// Google verification and www stay, MX replaced, plan is now idempotent.
	var hasGoogle, hasWWW bool
	mxCount := 0
	for _, r := range m.recs {
		hasGoogle = hasGoogle || strings.HasPrefix(r.Value, "google-site")
		hasWWW = hasWWW || r.Name == "www"
		if r.Type == "MX" {
			mxCount++
			if r.Value != "mail.example.com." || r.Priority != 10 {
				t.Errorf("mx = %+v", r)
			}
		}
	}
	if !hasGoogle || !hasWWW || mxCount != 1 {
		t.Fatalf("foreign records touched or MX wrong: %+v", m.recs)
	}
	again, _ := MakePlan(context.Background(), m, info, "example.com", desired("203.0.113.10"))
	for _, c := range again.Changes {
		if c.Action == "create" || c.Action == "update" {
			t.Errorf("second plan not idempotent: %s %s", c.Key, c.Action)
		}
	}
}

func TestPlanManualForUnsupported(t *testing.T) {
	m := &memProvider{zone: "example.com"}
	info := Info{ID: "strato", Name: "STRATO", Supports: []string{"TXT", "CNAME"}}
	plan, _ := MakePlan(context.Background(), m, info, "example.com", desired(""))
	for _, key := range []string{"a", "mx", "srv-pop3s"} {
		if c := find1(t, plan, key); c.Action != "manual" || c.Reason == nil {
			t.Errorf("%s should be manual: %+v", key, c)
		}
	}
	if c := find1(t, plan, "spf"); c.Action != "create" {
		t.Errorf("spf = %+v", c)
	}
}

func TestMergeSPF(t *testing.T) {
	cases := map[string]string{
		"":                                       "v=spf1 mx -all",
		"v=spf1 -all":                            "v=spf1 mx -all",
		"v=spf1 a mx include:x.example ?all":     "v=spf1 mx a include:x.example ?all",
		"v=spf1 redirect=_spf.provider.example":  "v=spf1 mx -all",
		"v=spf1 ip4:1.2.3.4 include:_spf.a ~all": "v=spf1 mx ip4:1.2.3.4 include:_spf.a ~all",
	}
	for in, want := range cases {
		if got := MergeSPF(in); got != want {
			t.Errorf("MergeSPF(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestPresentationRoundtrip(t *testing.T) {
	r := Record{Name: "pw._domainkey", Type: "TXT", Value: longDKIM}
	p := presentation(r)
	if !strings.HasPrefix(p, `"v=DKIM1`) || strings.Count(p, `"`) < 4 {
		t.Fatalf("long TXT not split: %s", p)
	}
	for _, part := range strings.Split(p, `" "`) {
		if len(strings.Trim(part, `"`)) > 255 {
			t.Fatalf("chunk longer than 255")
		}
	}
	if back := parsePresentation("pw._domainkey", "TXT", p, 0); back.Value != longDKIM {
		t.Fatalf("roundtrip mismatch")
	}
	mx := parsePresentation("@", "MX", "10 mail.example.com.", 0)
	if mx.Priority != 10 || mx.Value != "mail.example.com." {
		t.Fatalf("mx = %+v", mx)
	}
}

// ---- provider fakes speaking the documented wire formats ----

// runProvider plans and applies all changes and returns the plan.
func runProvider(t *testing.T, p Provider, info Info) *Plan {
	t.Helper()
	plan, err := MakePlan(context.Background(), p, info, "example.com", desired("203.0.113.10"))
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	var keys []string
	for _, c := range plan.Changes {
		keys = append(keys, c.Key)
	}
	res := ApplyPlan(context.Background(), p, plan, keys)
	if len(res.Failed) > 0 {
		t.Fatalf("apply failed: %+v", res.Failed)
	}
	return plan
}

func TestHetzner(t *testing.T) {
	var mu sync.Mutex
	sets := map[string][]string{"@/MX": {"10 old.mx.example."}, "@/TXT": {`"google-site-verification=abc"`}}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		if r.Header.Get("Authorization") != "Bearer tok" {
			w.WriteHeader(401)
			return
		}
		path := strings.TrimPrefix(r.URL.Path, "/v1")
		switch {
		case path == "/zones" && r.Method == "GET":
			if r.URL.Query().Get("name") == "example.com" {
				fmt.Fprint(w, `{"zones":[{"id":42,"name":"example.com"}]}`)
			} else {
				fmt.Fprint(w, `{"zones":[]}`)
			}
		case path == "/zones/42/rrsets" && r.Method == "GET":
			var out []map[string]any
			for k, vals := range sets {
				nt := strings.SplitN(k, "/", 2)
				recs := []map[string]string{}
				for _, v := range vals {
					recs = append(recs, map[string]string{"value": v})
				}
				out = append(out, map[string]any{"name": nt[0], "type": nt[1], "ttl": 3600, "records": recs})
			}
			json.NewEncoder(w).Encode(map[string]any{"rrsets": out, "meta": map[string]any{"pagination": map[string]any{"next_page": nil}}})
		case path == "/zones/42/rrsets" && r.Method == "POST":
			var b struct {
				Name, Type string
				Records    []struct{ Value string }
			}
			json.NewDecoder(r.Body).Decode(&b)
			for _, x := range b.Records {
				sets[b.Name+"/"+b.Type] = append(sets[b.Name+"/"+b.Type], x.Value)
			}
			w.WriteHeader(201)
			fmt.Fprint(w, `{"action":{"id":1,"status":"running"}}`)
		case strings.HasSuffix(path, "/actions/set_records"):
			parts := strings.Split(path, "/") // /zones/42/rrsets/{name}/{type}/actions/set_records
			var b struct{ Records []struct{ Value string } }
			json.NewDecoder(r.Body).Decode(&b)
			key := parts[4] + "/" + parts[5]
			sets[key] = nil
			for _, x := range b.Records {
				sets[key] = append(sets[key], x.Value)
			}
			w.WriteHeader(201)
			fmt.Fprint(w, `{"action":{"id":2,"status":"running"}}`)
		case strings.HasPrefix(path, "/zones/actions/"):
			fmt.Fprint(w, `{"action":{"status":"success"}}`)
		default:
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
			w.WriteHeader(404)
		}
	}))
	defer srv.Close()
	p := &hetzner{token: "tok", hc: srv.Client(), base: srv.URL + "/v1"}
	runProvider(t, p, hetznerInfo)
	if got := sets["@/MX"]; len(got) != 1 || got[0] != "10 mail.example.com." {
		t.Errorf("MX = %v", got)
	}
	if got := sets["@/TXT"]; len(got) != 2 {
		t.Errorf("TXT at apex should keep google + add spf: %v", got)
	}
	dk := sets["pw202611._domainkey/TXT"]
	if len(dk) != 1 || !strings.Contains(dk[0], `" "`) {
		t.Errorf("DKIM must be split into quoted strings: %v", dk)
	}
	if got := sets["mail/A"]; len(got) != 1 || got[0] != "203.0.113.10" {
		t.Errorf("A = %v", got)
	}
}

func TestIONOS(t *testing.T) {
	recs := []ionosRecord{
		{ID: "r1", Name: "example.com", Type: "MX", Content: "mx00.ionos.de", Prio: 10},
		{ID: "r2", Name: "example.com", Type: "TXT", Content: `"v=spf1 include:_spf-eu.ionos.com ~all"`},
	}
	n := 2
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-API-Key") != "pre.secret" {
			w.WriteHeader(401)
			return
		}
		switch {
		case r.URL.Path == "/dns/v1/zones":
			fmt.Fprint(w, `[{"id":"z1","name":"example.com","type":"NATIVE"}]`)
		case r.URL.Path == "/dns/v1/zones/z1" && r.Method == "GET":
			json.NewEncoder(w).Encode(map[string]any{"id": "z1", "name": "example.com", "records": recs})
		case r.URL.Path == "/dns/v1/zones/z1/records" && r.Method == "POST":
			var in []ionosRecord
			json.NewDecoder(r.Body).Decode(&in)
			for _, x := range in {
				n++
				x.ID = "r" + strconv.Itoa(n)
				recs = append(recs, x)
			}
			json.NewEncoder(w).Encode(in)
		case strings.HasPrefix(r.URL.Path, "/dns/v1/zones/z1/records/") && r.Method == "PUT":
			id := strings.TrimPrefix(r.URL.Path, "/dns/v1/zones/z1/records/")
			var in ionosRecord
			json.NewDecoder(r.Body).Decode(&in)
			for i := range recs {
				if recs[i].ID == id {
					in.ID = id
					recs[i] = in
				}
			}
		default:
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
		}
	}))
	defer srv.Close()
	p := &ionos{key: "pre.secret", hc: srv.Client(), base: srv.URL + "/dns/v1"}
	runProvider(t, p, ionosInfo)
	var spf, mx, dkim *ionosRecord
	for i := range recs {
		switch {
		case recs[i].Type == "TXT" && strings.Contains(recs[i].Content, "v=spf1"):
			spf = &recs[i]
		case recs[i].Type == "MX":
			mx = &recs[i]
		case strings.HasPrefix(recs[i].Name, "pw202611._domainkey."):
			dkim = &recs[i]
		}
	}
	if spf == nil || spf.Content != `"v=spf1 mx include:_spf-eu.ionos.com ~all"` {
		t.Errorf("spf = %+v", spf)
	}
	if mx == nil || mx.Content != "mail.example.com" || mx.Prio != 10 {
		t.Errorf("mx = %+v", mx)
	}
	if dkim == nil || dkim.Name != "pw202611._domainkey.example.com" {
		t.Errorf("dkim = %+v", dkim)
	}
}

func TestNetcup(t *testing.T) {
	records := []netcupRecord{{ID: "7", Hostname: "@", Type: "MX", Priority: "10", Destination: "mx2f80.netcup.net"}}
	var updates []netcupRecord
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Action string
			Param  map[string]any
		}
		json.NewDecoder(r.Body).Decode(&req)
		ok := func(data any) {
			json.NewEncoder(w).Encode(map[string]any{"status": "success", "statuscode": 2000, "responsedata": data})
		}
		switch req.Action {
		case "login":
			if req.Param["apipassword"] != "pw" {
				json.NewEncoder(w).Encode(map[string]any{"status": "error", "statuscode": 4013, "shortmessage": "Validation Error."})
				return
			}
			ok(map[string]string{"apisessionid": "sess"})
		case "infoDnsRecords":
			ok(map[string]any{"dnsrecords": records})
		case "updateDnsRecords":
			b, _ := json.Marshal(req.Param["dnsrecordset"])
			var set struct{ DNSRecords []netcupRecord }
			json.Unmarshal(b, &set)
			updates = append(updates, set.DNSRecords...)
			ok(map[string]any{"dnsrecords": records})
		case "logout":
			ok("")
		}
	}))
	defer srv.Close()
	p := &netcup{cust: "123", key: "k", pass: "pw", hc: srv.Client(), endpoint: srv.URL}
	runProvider(t, p, netcupInfo)
	var mx *netcupRecord
	for i := range updates {
		if updates[i].Type == "MX" {
			mx = &updates[i]
		}
	}
	if mx == nil || mx.ID != "7" || mx.Destination != "mail.example.com" || mx.Priority != "10" {
		t.Errorf("mx update = %+v", mx)
	}
	bad := &netcup{cust: "123", key: "k", pass: "falsch", hc: srv.Client(), endpoint: srv.URL}
	if _, err := bad.List(context.Background(), "example.com"); !IsProviderError(err) {
		t.Errorf("wrong password must be a provider error, got %v", err)
	}
}

func TestINWX(t *testing.T) {
	type rec struct {
		ID      int    `json:"id"`
		Name    string `json:"name"`
		Type    string `json:"type"`
		Content string `json:"content"`
		Prio    int    `json:"prio"`
	}
	records := []rec{{ID: 1, Name: "example.com", Type: "MX", Content: "mail.inwx.example", Prio: 10}}
	var created []map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Method string
			Params map[string]any
		}
		json.NewDecoder(r.Body).Decode(&req)
		if req.Method != "account.login" {
			if c, err := r.Cookie("domrobot"); err != nil || c.Value != "s1" {
				json.NewEncoder(w).Encode(map[string]any{"code": 2200, "msg": "Authentication error"})
				return
			}
		}
		res := map[string]any{}
		switch req.Method {
		case "account.login":
			http.SetCookie(w, &http.Cookie{Name: "domrobot", Value: "s1", Path: "/"})
			res = map[string]any{"tfa": "0"}
		case "nameserver.info":
			res = map[string]any{"record": records}
		case "nameserver.createRecord":
			created = append(created, req.Params)
			res = map[string]any{"id": 99}
		case "nameserver.updateRecord":
			records[0].Content = fmt.Sprint(req.Params["content"])
		}
		json.NewEncoder(w).Encode(map[string]any{"code": 1000, "msg": "Command completed successfully", "resData": res})
	}))
	defer srv.Close()
	pv, _ := inwxInfo.New(map[string]string{"username": "u", "password": "p"}, srv.Client())
	p := pv.(*inwx)
	p.endpoint = srv.URL
	runProvider(t, p, inwxInfo)
	if records[0].Content != "mail.example.com" {
		t.Errorf("mx not updated: %+v", records[0])
	}
	var spfName any = "missing"
	for _, c := range created {
		if strings.HasPrefix(fmt.Sprint(c["content"]), "v=spf1") {
			spfName = c["name"]
		}
	}
	if spfName != "" {
		t.Errorf("apex record must use empty name, got %v", spfName)
	}
}

func TestStrato(t *testing.T) {
	var mu sync.Mutex
	entries := [][3]string{{"", "TXT", "google-site-verification=abc"}, {"www", "CNAME", "example.com"}}
	var posted url.Values
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		r.ParseForm()
		q := r.Form
		switch {
		case r.Method == "POST" && q.Get("action_customer_login.x") == "Login":
			if q.Get("passwd") != "geheim" {
				fmt.Fprint(w, "<html><body>Login fehlgeschlagen</body></html>")
				return
			}
			http.Redirect(w, r, srv.URL+"/apps/CustomerService?sessionID=SID1&cID=0&node=kds_CustomerEntryPage", 302)
		case r.Method == "GET" && q.Get("node") == "kds_CustomerEntryPage":
			fmt.Fprint(w, `<table id="package_list"><tbody><tr><td class="package-information">Paket – example.com</td>
				<td class="jss_with_own_packagename"><a href="/apps/CustomerService?sessionID=SID1&cID=7&node=Overview">Paket</a></td></tr></tbody></table>`)
		case r.Method == "GET" && q.Get("node") == "ManageDomains":
			var b strings.Builder
			b.WriteString(`<form>`)
			for _, e := range entries {
				fmt.Fprintf(&b, `<div class="txt-record-tmpl"><input name="prefix" value="%s"><select name="type"><option value="TXT"%s>TXT</option><option value="CNAME"%s>CNAME</option></select><textarea name="value">%s</textarea></div>`,
					e[0], sel(e[1] == "TXT"), sel(e[1] == "CNAME"), e[2])
			}
			b.WriteString(`<input type="submit" name="action_change_txt_records" value="Einstellung übernehmen"></form>`)
			fmt.Fprint(w, b.String())
		case r.Method == "POST" && q.Get("node") == "ManageDomains":
			posted = q
			entries = nil
			for i := range q["prefix"] {
				entries = append(entries, [3]string{q["prefix"][i], q["type"][i], q["value"][i]})
			}
			fmt.Fprint(w, "<html>ok</html>")
		default:
			fmt.Fprint(w, "<html></html>")
		}
	}))
	defer srv.Close()
	pv, _ := stratoInfo.New(map[string]string{"username": "kunde", "password": "geheim"}, srv.Client())
	p := pv.(*strato)
	p.base = srv.URL + "/apps/CustomerService"
	plan := runProvider(t, p, stratoInfo)
	if c := find1(t, plan, "mx"); c.Action != "manual" {
		t.Errorf("MX must be manual at STRATO: %+v", c)
	}
	if posted.Get("spf_type") != "NONE" || posted.Get("sessionID") != "SID1" || posted.Get("cID") != "7" {
		t.Errorf("posted form wrong: %v", posted)
	}
	var keepGoogle, keepWWW, spf, dkim bool
	for _, e := range entries {
		keepGoogle = keepGoogle || strings.HasPrefix(e[2], "google-site")
		keepWWW = keepWWW || (e[0] == "www" && e[1] == "CNAME")
		spf = spf || (e[0] == "" && strings.HasPrefix(e[2], "v=spf1 mx"))
		dkim = dkim || (e[0] == "pw202611._domainkey" && e[2] == longDKIM)
	}
	if !keepGoogle || !keepWWW || !spf || !dkim {
		t.Fatalf("strato entries after push: %v", entries)
	}
	// wrong password
	pv2, _ := stratoInfo.New(map[string]string{"username": "kunde", "password": "falsch"}, srv.Client())
	p2 := pv2.(*strato)
	p2.base = srv.URL + "/apps/CustomerService"
	if _, err := p2.Zone(context.Background(), "example.com"); !IsProviderError(err) {
		t.Errorf("expected provider error, got %v", err)
	}
	_ = io.Discard
}

func sel(b bool) string {
	if b {
		return " selected"
	}
	return ""
}

func TestDeSEC(t *testing.T) {
	sets := []desecRRSet{{Subname: "", Type: "TXT", TTL: 3600, Records: []string{`"google-site-verification=abc"`}}}
	var put []desecRRSet
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Token tok" {
			w.WriteHeader(401)
			return
		}
		switch {
		case r.URL.Path == "/api/v1/domains/":
			fmt.Fprint(w, `[{"name":"example.com"}]`)
		case r.URL.Path == "/api/v1/domains/example.com/rrsets/" && r.Method == "GET":
			json.NewEncoder(w).Encode(sets)
		case r.URL.Path == "/api/v1/domains/example.com/rrsets/" && r.Method == "PUT":
			json.NewDecoder(r.Body).Decode(&put)
			fmt.Fprint(w, "[]")
		}
	}))
	defer srv.Close()
	p := &desec{token: "tok", hc: srv.Client(), base: srv.URL + "/api/v1"}
	runProvider(t, p, desecInfo)
	var apexTXT *desecRRSet
	for i := range put {
		if put[i].Subname == "" && put[i].Type == "TXT" {
			apexTXT = &put[i]
		}
	}
	if apexTXT == nil || len(apexTXT.Records) != 2 {
		t.Fatalf("apex TXT must keep google and add SPF: %+v", apexTXT)
	}
}

func TestCloudflare(t *testing.T) {
	var created []cfRecord
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/client/v4/zones":
			if r.URL.Query().Get("name") == "example.com" {
				fmt.Fprint(w, `{"success":true,"result":[{"id":"z9","name":"example.com"}]}`)
			} else {
				fmt.Fprint(w, `{"success":true,"result":[]}`)
			}
		case r.URL.Path == "/client/v4/zones/z9/dns_records" && r.Method == "GET":
			fmt.Fprint(w, `{"success":true,"result":[{"id":"a1","type":"TXT","name":"example.com","content":"\"v=spf1 -all\"","ttl":1}],"result_info":{"total_pages":1}}`)
		case r.URL.Path == "/client/v4/zones/z9/dns_records" && r.Method == "POST":
			var c cfRecord
			json.NewDecoder(r.Body).Decode(&c)
			created = append(created, c)
			fmt.Fprint(w, `{"success":true,"result":{}}`)
		case strings.HasPrefix(r.URL.Path, "/client/v4/zones/z9/dns_records/") && r.Method == "PUT":
			fmt.Fprint(w, `{"success":true,"result":{}}`)
		default:
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
		}
	}))
	defer srv.Close()
	p := &cloudflare{token: "t", hc: srv.Client(), base: srv.URL + "/client/v4"}
	runProvider(t, p, cloudflareInfo)
	for _, c := range created {
		if (c.Type == "A" || c.Type == "CNAME") && (c.Proxied == nil || *c.Proxied) {
			t.Errorf("%s must not be proxied", c.Type)
		}
		if c.Type == "SRV" && (c.Data == nil || c.Data["port"] == nil) {
			t.Errorf("SRV must use data object: %+v", c)
		}
		if c.Type == "MX" && (c.Priority == nil || *c.Priority != 10 || c.Content != "mail.example.com") {
			t.Errorf("mx = %+v", c)
		}
	}
}

func TestSmarthostIncludeAndDynDNS(t *testing.T) {
	if got := MergeSPF("", "include:_spf.strato.com"); got != "v=spf1 mx include:_spf.strato.com -all" {
		t.Errorf("fresh SPF = %q", got)
	}
	if got := MergeSPF("v=spf1 include:_spf.strato.com ~all", "include:_spf.strato.com"); got != "v=spf1 mx include:_spf.strato.com ~all" {
		t.Errorf("include must not be duplicated: %q", got)
	}
	m := &memProvider{zone: "example.com", recs: []Record{{ID: "1", Name: "@", Type: "TXT", Value: "v=spf1 a ~all"}}}
	info := Info{ID: "mem", Name: "Mem", Supports: allTypes}
	d := DesiredRecords("example.com", "mail.example.com", "pw1", longDKIM, "", "", Options{SPFInclude: "include:_spf.strato.com", DynDNS: true})
	plan, _ := MakePlan(context.Background(), m, info, "example.com", d)
	if c := find1(t, plan, "spf"); c.Desired != "v=spf1 mx a include:_spf.strato.com ~all" {
		t.Errorf("spf desired = %q", c.Desired)
	}
	if c := find1(t, plan, "a"); c.Action != "manual" || c.Reason == nil || !strings.Contains(*c.Reason, "DynDNS") {
		t.Errorf("A with DynDNS = %+v", c)
	}
}
