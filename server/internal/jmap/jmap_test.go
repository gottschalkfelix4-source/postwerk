package jmap

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"postwerk/internal/auth"
	"postwerk/internal/delivery"
	"postwerk/internal/message"
	"postwerk/internal/store"
)

const testPassword = "geheim-passwort-123"

// fakeDelivery stores messages through the real delivery service but
// records submissions instead of sending them.
type fakeDelivery struct {
	*delivery.Service
	mu   sync.Mutex
	sent []sentMail
}

type sentMail struct {
	from  string
	rcpts []string
	data  []byte
}

func (f *fakeDelivery) Submit(ctx context.Context, acct *store.Account, from string, rcpts []string, data []byte, saveSent bool) (*store.Message, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sent = append(f.sent, sentMail{from, rcpts, data})
	return nil, nil
}

type tenv struct {
	t         *testing.T
	st        *store.Store
	svc       *delivery.Service
	fake      *fakeDelivery
	jmap      *Server
	srv       *httptest.Server
	acct      *store.Account
	accountID string
}

func setup(t *testing.T, tls bool) *tenv {
	t.Helper()
	ctx := context.Background()
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	h, _ := auth.HashPassword(testPassword)
	acct := &store.Account{Email: "anna@example.test", Name: "Anna", PasswordHash: h, Role: "user"}
	if err := st.CreateAccount(ctx, acct); err != nil {
		t.Fatal(err)
	}
	if _, err := st.CreateAlias(ctx, "team@example.test", []string{"anna@example.test"}); err != nil {
		t.Fatal(err)
	}
	svc := &delivery.Service{Store: st}
	fake := &fakeDelivery{Service: svc}
	guard := auth.NewGuard(func() auth.Limits { return auth.Limits{MaxAttempts: 50, Window: time.Minute, BanFor: time.Minute} })
	js := &Server{
		Store: st, Auth: &auth.Authenticator{Store: st, Guard: guard}, Delivery: fake,
		MaxSize: func() int64 { return 10 << 20 }, Log: slog.New(slog.NewTextHandler(io.Discard, nil)),
		CookieAuth: func(r *http.Request) (*store.Account, error) {
			if c, err := r.Cookie("pw_session"); err == nil && c.Value == "valid" {
				return st.GetAccount(r.Context(), acct.ID)
			}
			return nil, fmt.Errorf("no session")
		},
	}
	mux := http.NewServeMux()
	js.Register(mux)
	var srv *httptest.Server
	if tls {
		srv = httptest.NewTLSServer(mux)
	} else {
		srv = httptest.NewServer(mux)
	}
	t.Cleanup(srv.Close)
	return &tenv{t: t, st: st, svc: svc, fake: fake, jmap: js, srv: srv, acct: acct, accountID: fmtID(prefixAccount, acct.ID)}
}

func (e *tenv) do(method, path string, body io.Reader, hdr map[string]string) *http.Response {
	e.t.Helper()
	req, err := http.NewRequest(method, e.srv.URL+path, body)
	if err != nil {
		e.t.Fatal(err)
	}
	req.SetBasicAuth(e.acct.Email, testPassword)
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	resp, err := e.srv.Client().Do(req)
	if err != nil {
		e.t.Fatal(err)
	}
	return resp
}

type mresp struct {
	Name   string
	Args   map[string]any
	CallID string
}

// call runs one JMAP request; each call is [name, args, callId].
func (e *tenv) call(calls ...[]any) []mresp {
	e.t.Helper()
	body, _ := json.Marshal(map[string]any{"using": []string{capCore, capMail, capSubmission}, "methodCalls": calls})
	resp := e.do("POST", "/jmap/api", bytes.NewReader(body), map[string]string{"Content-Type": "application/json"})
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 {
		e.t.Fatalf("api status %d: %s", resp.StatusCode, raw)
	}
	var out struct {
		MethodResponses [][]json.RawMessage `json:"methodResponses"`
		SessionState    string              `json:"sessionState"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		e.t.Fatalf("bad response %s: %v", raw, err)
	}
	if out.SessionState == "" {
		e.t.Fatal("sessionState missing")
	}
	var res []mresp
	for _, r := range out.MethodResponses {
		var m mresp
		_ = json.Unmarshal(r[0], &m.Name)
		_ = json.Unmarshal(r[1], &m.Args)
		_ = json.Unmarshal(r[2], &m.CallID)
		res = append(res, m)
	}
	return res
}

func (e *tenv) must(r mresp, name string) map[string]any {
	e.t.Helper()
	if r.Name != name {
		e.t.Fatalf("expected %s, got %s %v", name, r.Name, r.Args)
	}
	return r.Args
}

func (e *tenv) mailboxID(role string) string {
	id, err := e.st.MailboxByRole(context.Background(), e.acct.ID, role)
	if err != nil {
		e.t.Fatal(err)
	}
	return fmtID(prefixMailbox, id)
}

func (e *tenv) deliver(subject, body string, extra map[string]string) *store.Message {
	e.t.Helper()
	inbox, _ := e.st.MailboxByRole(context.Background(), e.acct.ID, "inbox")
	raw := message.Build(message.Draft{From: store.Address{Name: "Bernd", Email: "bernd@example.test"}, To: []store.Address{{Email: e.acct.Email}},
		Subject: subject, Text: body, MessageID: strings.ReplaceAll(subject, " ", "") + "@example.test", Extra: extra})
	m, err := e.svc.ImportMessage(context.Background(), e.acct, inbox, raw, delivery.ImportOptions{SkipAI: true})
	if err != nil {
		e.t.Fatal(err)
	}
	return m
}

func list(v any) []any {
	l, _ := v.([]any)
	return l
}

func obj(v any) map[string]any {
	m, _ := v.(map[string]any)
	return m
}

func TestSession(t *testing.T) {
	e := setup(t, true)
	resp := e.do("GET", "/.well-known/jmap", nil, nil)
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("status %d", resp.StatusCode)
	}
	var s map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&s); err != nil {
		t.Fatal(err)
	}
	caps := obj(s["capabilities"])
	for _, c := range []string{capCore, capMail, capSubmission} {
		if caps[c] == nil {
			t.Errorf("capability %s missing", c)
		}
	}
	if obj(caps[capCore])["maxCallsInRequest"] == nil || obj(caps[capCore])["maxSizeUpload"] == nil {
		t.Error("core limits missing")
	}
	if obj(s["accounts"])[e.accountID] == nil || obj(s["primaryAccounts"])[capMail] != e.accountID {
		t.Errorf("accounts wrong: %v", s["accounts"])
	}
	if !strings.HasSuffix(s["apiUrl"].(string), "/jmap/api") || !strings.Contains(s["downloadUrl"].(string), "{blobId}") ||
		!strings.Contains(s["uploadUrl"].(string), "{accountId}") || !strings.Contains(s["eventSourceUrl"].(string), "{types}") {
		t.Errorf("urls wrong: %v", s)
	}
	if s["username"] != e.acct.Email || s["state"] == "" {
		t.Errorf("username/state wrong: %v", s)
	}
}

func TestAuthRejectedOverPlainHTTP(t *testing.T) {
	e := setup(t, false)
	resp := e.do("GET", "/.well-known/jmap", nil, nil)
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("basic auth over http: status %d, want 403", resp.StatusCode)
	}
	req, _ := http.NewRequest("GET", e.srv.URL+"/.well-known/jmap", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 401 || resp.Header.Get("WWW-Authenticate") != "" {
		t.Fatalf("no auth over http: status %d, challenge %q", resp.StatusCode, resp.Header.Get("WWW-Authenticate"))
	}
}

func TestAuthWrongPasswordAndCookieCSRF(t *testing.T) {
	e := setup(t, true)
	req, _ := http.NewRequest("GET", e.srv.URL+"/.well-known/jmap", nil)
	req.SetBasicAuth(e.acct.Email, "falsch")
	resp, err := e.srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 401 || !strings.HasPrefix(resp.Header.Get("WWW-Authenticate"), "Basic") {
		t.Fatalf("wrong password: status %d", resp.StatusCode)
	}

	body := `{"using":["urn:ietf:params:jmap:core"],"methodCalls":[["Core/echo",{"hello":true},"c1"]]}`
	post := func(csrf bool) int {
		req, _ := http.NewRequest("POST", e.srv.URL+"/jmap/api", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.AddCookie(&http.Cookie{Name: "pw_session", Value: "valid"})
		if csrf {
			req.Header.Set("X-Postwerk", "1")
		}
		resp, err := e.srv.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		return resp.StatusCode
	}
	if code := post(false); code != http.StatusForbidden {
		t.Fatalf("cookie without CSRF header: status %d, want 403", code)
	}
	if code := post(true); code != 200 {
		t.Fatalf("cookie with CSRF header: status %d", code)
	}
}

func TestRequestErrors(t *testing.T) {
	e := setup(t, true)
	r := e.call(
		[]any{"Foo/bar", map[string]any{}, "a"},
		[]any{"Mailbox/get", map[string]any{"accountId": "A999"}, "b"},
		[]any{"Email/get", map[string]any{"accountId": e.accountID, "#ids": map[string]any{"resultOf": "x", "name": "Email/query", "path": "/ids"}}, "c"},
		[]any{"Core/echo", map[string]any{"ping": "pong"}, "d"},
	)
	want := []string{"unknownMethod", "accountNotFound", "invalidResultReference"}
	for i, w := range want {
		if r[i].Name != "error" || r[i].Args["type"] != w {
			t.Errorf("call %d: got %s %v, want %s", i, r[i].Name, r[i].Args, w)
		}
	}
	if r[3].Name != "Core/echo" || r[3].Args["ping"] != "pong" {
		t.Errorf("echo: %v", r[3])
	}
	// Unknown capability is a request-level error.
	resp := e.do("POST", "/jmap/api", strings.NewReader(`{"using":["urn:example:nope"],"methodCalls":[]}`), nil)
	resp.Body.Close()
	if resp.StatusCode != 400 {
		t.Errorf("unknown capability: status %d", resp.StatusCode)
	}
}

func TestMailboxGetAndSet(t *testing.T) {
	e := setup(t, true)
	r := e.call([]any{"Mailbox/get", map[string]any{"accountId": e.accountID, "ids": nil}, "0"})
	got := e.must(r[0], "Mailbox/get")
	oldState := got["state"].(string)
	roles := map[string]bool{}
	for _, m := range list(got["list"]) {
		if role, ok := obj(m)["role"].(string); ok {
			roles[role] = true
		}
	}
	for _, role := range []string{"inbox", "drafts", "sent", "archive", "junk", "trash"} {
		if !roles[role] {
			t.Errorf("role %s missing", role)
		}
	}

	r = e.call([]any{"Mailbox/set", map[string]any{"accountId": e.accountID, "create": map[string]any{
		"p": map[string]any{"name": "Projekte"},
		"c": map[string]any{"name": "2024", "parentId": "#p"},
	}}, "0"})
	set := e.must(r[0], "Mailbox/set")
	created := obj(set["created"])
	if created["p"] == nil || created["c"] == nil {
		t.Fatalf("not created: %v", set["notCreated"])
	}
	parentID := obj(created["p"])["id"].(string)
	childID := obj(created["c"])["id"].(string)

	r = e.call(
		[]any{"Mailbox/get", map[string]any{"accountId": e.accountID, "ids": []string{childID}}, "0"},
		[]any{"Mailbox/changes", map[string]any{"accountId": e.accountID, "sinceState": oldState}, "1"},
	)
	child := obj(list(e.must(r[0], "Mailbox/get")["list"])[0])
	if child["name"] != "2024" || child["parentId"] != parentID {
		t.Errorf("child mailbox wrong: %v", child)
	}
	ch := e.must(r[1], "Mailbox/changes")
	if len(list(ch["created"])) != 2 {
		t.Errorf("changes: %v", ch)
	}

	// Rename the parent: the child moves along.
	r = e.call([]any{"Mailbox/set", map[string]any{"accountId": e.accountID, "update": map[string]any{
		parentID: map[string]any{"name": "Kunden"},
	}, "destroy": []string{e.mailboxID("inbox"), parentID}}, "0"})
	set = e.must(r[0], "Mailbox/set")
	if _, ok := obj(set["updated"])[parentID]; !ok {
		t.Errorf("rename failed: %v", set["notUpdated"])
	}
	nd := obj(set["notDestroyed"])
	if obj(nd[e.mailboxID("inbox")])["type"] != "forbidden" {
		t.Errorf("inbox destroy: %v", nd)
	}
	if obj(nd[parentID])["type"] != "mailboxHasChild" {
		t.Errorf("parent destroy: %v", nd)
	}
	var name string
	cid, _ := parseID(prefixMailbox, childID)
	_ = e.st.DB.QueryRow(`SELECT name FROM mailboxes WHERE id = ?`, cid).Scan(&name)
	if name != "Kunden/2024" {
		t.Errorf("child full name = %q", name)
	}

	r = e.call([]any{"Mailbox/set", map[string]any{"accountId": e.accountID, "ifInState": "nope"}, "0"})
	if r[0].Name != "error" || r[0].Args["type"] != "stateMismatch" {
		t.Errorf("ifInState: %v", r[0])
	}
	r = e.call([]any{"Mailbox/query", map[string]any{"accountId": e.accountID, "filter": map[string]any{"hasAnyRole": true},
		"sort": []any{map[string]any{"property": "sortOrder"}}}, "0"})
	ids := list(e.must(r[0], "Mailbox/query")["ids"])
	if len(ids) != 6 || ids[0] != e.mailboxID("inbox") {
		t.Errorf("query: %v", ids)
	}
}

func TestEmailQueryAndGetWithBackReferences(t *testing.T) {
	e := setup(t, true)
	e.deliver("Erste Nachricht", "Hallo Anna, das ist die erste.", nil)
	time.Sleep(1100 * time.Millisecond) // distinct receivedAt
	e.deliver("Zweite Nachricht", "Noch eine Nachricht mit Rechnung.", map[string]string{"List-Id": "<news.example.test>"})
	inbox := e.mailboxID("inbox")

	r := e.call(
		[]any{"Email/query", map[string]any{"accountId": e.accountID, "filter": map[string]any{"inMailbox": inbox},
			"sort": []any{map[string]any{"property": "receivedAt", "isAscending": false}}, "calculateTotal": true}, "q"},
		[]any{"Email/get", map[string]any{"accountId": e.accountID,
			"#ids":                map[string]any{"resultOf": "q", "name": "Email/query", "path": "/ids"},
			"properties":          []string{"subject", "from", "threadId", "keywords", "mailboxIds", "textBody", "bodyValues", "messageId", "header:List-Id:asText"},
			"fetchTextBodyValues": true}, "g"},
		[]any{"Thread/get", map[string]any{"accountId": e.accountID,
			"#ids": map[string]any{"resultOf": "g", "name": "Email/get", "path": "/list/*/threadId"}}, "t"},
	)
	q := e.must(r[0], "Email/query")
	if q["total"].(float64) != 2 || len(list(q["ids"])) != 2 {
		t.Fatalf("query: %v", q)
	}
	emails := list(e.must(r[1], "Email/get")["list"])
	if len(emails) != 2 {
		t.Fatalf("get: %v", r[1].Args)
	}
	first := obj(emails[0])
	if first["subject"] != "Zweite Nachricht" {
		t.Errorf("sort order: %v", first["subject"])
	}
	if first["header:List-Id:asText"] != "<news.example.test>" {
		t.Errorf("header property: %v", first["header:List-Id:asText"])
	}
	if obj(list(first["from"])[0])["email"] != "bernd@example.test" || list(first["messageId"])[0] != "ZweiteNachricht@example.test" {
		t.Errorf("from/messageId: %v %v", first["from"], first["messageId"])
	}
	if obj(first["mailboxIds"])[inbox] != true {
		t.Errorf("mailboxIds: %v", first["mailboxIds"])
	}
	tb := list(first["textBody"])
	partID := obj(tb[0])["partId"].(string)
	val := obj(obj(first["bodyValues"])[partID])["value"].(string)
	if !strings.Contains(val, "Rechnung") {
		t.Errorf("body value: %q", val)
	}
	if threads := list(e.must(r[2], "Thread/get")["list"]); len(threads) != 2 {
		t.Errorf("threads: %v", r[2].Args)
	}

	// Filters: text search, header, keyword, NOT operator.
	r = e.call(
		[]any{"Email/query", map[string]any{"accountId": e.accountID, "filter": map[string]any{"text": "rechnung"}}, "a"},
		[]any{"Email/query", map[string]any{"accountId": e.accountID, "filter": map[string]any{"header": []string{"List-Id", "news"}}}, "b"},
		[]any{"Email/query", map[string]any{"accountId": e.accountID, "filter": map[string]any{"operator": "NOT",
			"conditions": []any{map[string]any{"subject": "zweite"}}}}, "c"},
		[]any{"Email/query", map[string]any{"accountId": e.accountID, "filter": map[string]any{"hasKeyword": "$seen"}}, "d"},
		[]any{"SearchSnippet/get", map[string]any{"accountId": e.accountID, "filter": map[string]any{"text": "rechnung"},
			"#emailIds": map[string]any{"resultOf": "a", "name": "Email/query", "path": "/ids"}}, "s"},
		[]any{"Email/query", map[string]any{"accountId": e.accountID, "position": 1, "limit": 1}, "p"},
		[]any{"Email/query", map[string]any{"accountId": e.accountID, "collapseThreads": true, "calculateTotal": true}, "ct"},
	)
	if n := len(list(e.must(r[0], "Email/query")["ids"])); n != 1 {
		t.Errorf("text filter: %d", n)
	}
	if n := len(list(e.must(r[1], "Email/query")["ids"])); n != 1 {
		t.Errorf("header filter: %d", n)
	}
	if n := len(list(e.must(r[2], "Email/query")["ids"])); n != 1 {
		t.Errorf("NOT filter: %d", n)
	}
	if n := len(list(e.must(r[3], "Email/query")["ids"])); n != 0 {
		t.Errorf("keyword filter: %d", n)
	}
	snip := obj(list(e.must(r[4], "SearchSnippet/get")["list"])[0])
	if !strings.Contains(fmt.Sprint(snip["preview"]), "<mark>Rechnung</mark>") {
		t.Errorf("snippet: %v", snip)
	}
	p := e.must(r[5], "Email/query")
	if p["position"].(float64) != 1 || len(list(p["ids"])) != 1 {
		t.Errorf("paging: %v", p)
	}
	if ct := e.must(r[6], "Email/query"); ct["total"].(float64) != 2 {
		t.Errorf("collapseThreads: %v", ct)
	}
}

func TestEmailSetKeywordsAndChanges(t *testing.T) {
	e := setup(t, true)
	m := e.deliver("Hallo", "Text", nil)
	id := fmtID(prefixEmail, m.ID)
	r := e.call([]any{"Email/get", map[string]any{"accountId": e.accountID, "ids": []string{id}, "properties": []string{"keywords"}}, "0"})
	state := e.must(r[0], "Email/get")["state"].(string)
	mbState := e.call([]any{"Mailbox/get", map[string]any{"accountId": e.accountID, "ids": []string{}}, "0"})[0].Args["state"].(string)

	r = e.call(
		[]any{"Email/set", map[string]any{"accountId": e.accountID, "ifInState": state, "update": map[string]any{
			id: map[string]any{"keywords/$seen": true, "keywords/$flagged": true, "keywords/$label1": true},
		}}, "0"},
		[]any{"Email/changes", map[string]any{"accountId": e.accountID, "sinceState": state}, "1"},
		[]any{"Email/get", map[string]any{"accountId": e.accountID, "ids": []string{id}, "properties": []string{"keywords"}}, "2"},
		[]any{"Mailbox/changes", map[string]any{"accountId": e.accountID, "sinceState": mbState}, "3"},
	)
	set := e.must(r[0], "Email/set")
	if _, ok := obj(set["updated"])[id]; !ok {
		t.Fatalf("update failed: %v", set["notUpdated"])
	}
	ch := e.must(r[1], "Email/changes")
	if fmt.Sprint(list(ch["updated"])) != fmt.Sprint([]any{id}) || ch["newState"] == state {
		t.Errorf("changes: %v", ch)
	}
	kw := obj(obj(list(e.must(r[2], "Email/get")["list"])[0])["keywords"])
	if kw["$seen"] != true || kw["$flagged"] != true || kw["$label1"] != true {
		t.Errorf("keywords: %v", kw)
	}
	if len(list(e.must(r[3], "Mailbox/changes")["updated"])) != 1 {
		t.Errorf("mailbox counts change not reported: %v", r[3].Args)
	}
	f, _ := e.st.IMAPMessages(context.Background(), m.MailboxID)
	if !f[0].Flags.Seen || !f[0].Flags.Flagged || len(f[0].Flags.Keywords) != 1 {
		t.Errorf("imap flags: %+v", f[0].Flags)
	}

	// Remove a keyword, move to the archive, then destroy.
	archive := e.mailboxID("archive")
	inbox := e.mailboxID("inbox")
	state2 := ch["newState"].(string)
	r = e.call(
		[]any{"Email/set", map[string]any{"accountId": e.accountID, "update": map[string]any{
			id: map[string]any{"keywords/$flagged": nil, "mailboxIds/" + inbox: nil, "mailboxIds/" + archive: true},
		}}, "0"},
		[]any{"Email/get", map[string]any{"accountId": e.accountID, "ids": []string{id}, "properties": []string{"keywords", "mailboxIds"}}, "1"},
		[]any{"Email/set", map[string]any{"accountId": e.accountID, "update": map[string]any{
			id: map[string]any{"mailboxIds": map[string]any{inbox: true, archive: true}},
		}}, "2"},
	)
	got := obj(list(e.must(r[1], "Email/get")["list"])[0])
	if obj(got["keywords"])["$flagged"] != nil || obj(got["mailboxIds"])[archive] != true {
		t.Errorf("after move: %v", got)
	}
	if obj(obj(e.must(r[2], "Email/set")["notUpdated"])[id])["type"] != "invalidProperties" {
		t.Errorf("two mailboxes accepted: %v", r[2].Args)
	}
	r = e.call(
		[]any{"Email/set", map[string]any{"accountId": e.accountID, "destroy": []string{id}}, "0"},
		[]any{"Email/changes", map[string]any{"accountId": e.accountID, "sinceState": state2}, "1"},
		[]any{"Thread/changes", map[string]any{"accountId": e.accountID, "sinceState": "0"}, "2"},
	)
	if len(list(e.must(r[0], "Email/set")["destroyed"])) != 1 {
		t.Fatalf("destroy: %v", r[0].Args)
	}
	ch = e.must(r[1], "Email/changes")
	if fmt.Sprint(list(ch["destroyed"])) != fmt.Sprint([]any{id}) {
		t.Errorf("destroy changes: %v", ch)
	}
	// The thread was created and destroyed since state 0: nothing to report.
	tc := e.must(r[2], "Thread/changes")
	if len(list(tc["created"]))+len(list(tc["destroyed"])) != 0 {
		t.Errorf("thread changes: %v", tc)
	}
	r = e.call([]any{"Email/changes", map[string]any{"accountId": e.accountID, "sinceState": "999999"}, "0"})
	if r[0].Args["type"] != "cannotCalculateChanges" {
		t.Errorf("future state: %v", r[0])
	}
}

func (e *tenv) upload(data []byte, typ string) string {
	e.t.Helper()
	resp := e.do("POST", "/jmap/upload/"+e.accountID+"/", bytes.NewReader(data), map[string]string{"Content-Type": typ})
	defer resp.Body.Close()
	var out map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&out)
	if resp.StatusCode != http.StatusCreated {
		e.t.Fatalf("upload status %d: %v", resp.StatusCode, out)
	}
	if out["size"].(float64) != float64(len(data)) || out["type"] != typ {
		e.t.Fatalf("upload response: %v", out)
	}
	return out["blobId"].(string)
}

func TestUploadDownload(t *testing.T) {
	e := setup(t, true)
	data := []byte("%PDF-1.4 fake pdf content \x00\x01\x02")
	blobID := e.upload(data, "application/pdf")
	resp := e.do("GET", "/jmap/download/"+e.accountID+"/"+blobID+"/rechnung.pdf?type=application/pdf", nil, nil)
	got, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 200 || !bytes.Equal(got, data) {
		t.Fatalf("download: %d %q", resp.StatusCode, got)
	}
	if cd := resp.Header.Get("Content-Disposition"); !strings.HasPrefix(cd, "attachment") || !strings.Contains(cd, "rechnung.pdf") {
		t.Errorf("Content-Disposition: %q", cd)
	}
	if resp.Header.Get("X-Content-Type-Options") != "nosniff" || resp.Header.Get("Content-Type") != "application/pdf" {
		t.Errorf("headers: %v", resp.Header)
	}
	for _, bad := range []string{"G" + strings.Repeat("0", 64), "../../etc/passwd", "Gxyz"} {
		resp := e.do("GET", "/jmap/download/"+e.accountID+"/"+bad+"/x", nil, nil)
		resp.Body.Close()
		if resp.StatusCode != 404 {
			t.Errorf("download %q: status %d", bad, resp.StatusCode)
		}
	}
	resp = e.do("GET", "/jmap/download/A999/"+blobID+"/x", nil, nil)
	resp.Body.Close()
	if resp.StatusCode != 404 {
		t.Errorf("foreign account: status %d", resp.StatusCode)
	}
	// Another account cannot read the blob.
	ctx := context.Background()
	h, _ := auth.HashPassword(testPassword)
	other := &store.Account{Email: "otto@example.test", PasswordHash: h, Role: "user"}
	if err := e.st.CreateAccount(ctx, other); err != nil {
		t.Fatal(err)
	}
	req, _ := http.NewRequest("GET", e.srv.URL+"/jmap/download/"+fmtID(prefixAccount, other.ID)+"/"+blobID+"/x", nil)
	req.SetBasicAuth(other.Email, testPassword)
	r2, err := e.srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	r2.Body.Close()
	if r2.StatusCode != 404 {
		t.Errorf("other account read the upload: %d", r2.StatusCode)
	}
}

func TestEmailCreateAndSubmission(t *testing.T) {
	e := setup(t, true)
	attachment := []byte("Anhang-Inhalt \xff\xfe binär")
	blobID := e.upload(attachment, "application/octet-stream")
	drafts, sent := e.mailboxID("drafts"), e.mailboxID("sent")

	r := e.call([]any{"Identity/get", map[string]any{"accountId": e.accountID}, "i"})
	idents := list(e.must(r[0], "Identity/get")["list"])
	if len(idents) != 2 {
		t.Fatalf("identities: %v", idents)
	}
	identityID := obj(idents[0])["id"].(string)
	if obj(idents[0])["email"] != e.acct.Email || obj(idents[1])["email"] != "team@example.test" {
		t.Errorf("identities: %v", idents)
	}

	r = e.call(
		[]any{"Email/set", map[string]any{"accountId": e.accountID, "create": map[string]any{"draft": map[string]any{
			"mailboxIds": map[string]bool{drafts: true},
			"keywords":   map[string]bool{"$draft": true, "$seen": true},
			"from":       []any{map[string]any{"name": "Anna Ä", "email": e.acct.Email}},
			"to":         []any{map[string]any{"email": "carl@remote.test"}},
			"bcc":        []any{map[string]any{"email": "geheim@remote.test"}},
			"subject":    "Angebot für Sie",
			"textBody":   []any{map[string]any{"partId": "t1"}},
			"htmlBody":   []any{map[string]any{"partId": "h1"}},
			"bodyValues": map[string]any{"t1": map[string]any{"value": "Hallo Carl,\nanbei das Angebot."},
				"h1": map[string]any{"value": "<p>Hallo Carl,<br>anbei das Angebot.</p>"}},
			"attachments":            []any{map[string]any{"blobId": blobID, "name": "angebot.bin", "type": "application/octet-stream"}},
			"header:X-Mailer:asText": "Testclient",
		}}}, "c"},
		[]any{"Email/get", map[string]any{"accountId": e.accountID, "ids": []string{"#draft"},
			"properties": []string{"subject", "keywords", "attachments", "htmlBody", "bcc", "header:X-Mailer:asText", "bodyStructure"}}, "g"},
		[]any{"EmailSubmission/set", map[string]any{"accountId": e.accountID,
			"create": map[string]any{"sub": map[string]any{"identityId": identityID, "emailId": "#draft"}},
			"onSuccessUpdateEmail": map[string]any{"#sub": map[string]any{
				"mailboxIds/" + drafts: nil, "mailboxIds/" + sent: true, "keywords/$draft": nil}},
		}, "s"},
		[]any{"Email/get", map[string]any{"accountId": e.accountID, "ids": []string{"#draft"},
			"properties": []string{"mailboxIds", "keywords"}}, "g2"},
		[]any{"EmailSubmission/get", map[string]any{"accountId": e.accountID, "ids": nil}, "sg"},
	)
	created := obj(obj(e.must(r[0], "Email/set")["created"])["draft"])
	if created["id"] == nil || created["blobId"] == nil || created["threadId"] == nil {
		t.Fatalf("create: %v", r[0].Args)
	}
	draft := obj(list(e.must(r[1], "Email/get")["list"])[0])
	if draft["subject"] != "Angebot für Sie" || obj(draft["keywords"])["$draft"] != true || draft["header:X-Mailer:asText"] != "Testclient" {
		t.Errorf("draft: %v", draft)
	}
	atts := list(draft["attachments"])
	if len(atts) != 1 || obj(atts[0])["name"] != "angebot.bin" || obj(atts[0])["size"].(float64) != float64(len(attachment)) {
		t.Fatalf("attachments: %v", atts)
	}
	if len(list(draft["htmlBody"])) != 1 || obj(list(draft["htmlBody"])[0])["type"] != "text/html" {
		t.Errorf("htmlBody: %v", draft["htmlBody"])
	}
	bs := obj(draft["bodyStructure"])
	if bs["type"] != "multipart/mixed" || obj(list(bs["subParts"])[0])["type"] != "multipart/alternative" || bs["partId"] != nil {
		t.Errorf("bodyStructure: %v", bs)
	}
	if obj(list(draft["bcc"])[0])["email"] != "geheim@remote.test" {
		t.Errorf("bcc not stored in draft: %v", draft["bcc"])
	}

	sub := e.must(r[2], "EmailSubmission/set")
	if obj(sub["created"])["sub"] == nil {
		t.Fatalf("submission: %v", sub["notCreated"])
	}
	impl := e.must(r[3], "Email/set")
	if r[3].CallID != "s" || len(obj(impl["updated"])) != 1 {
		t.Errorf("implicit Email/set: %v", r[3])
	}
	after := obj(list(e.must(r[4], "Email/get")["list"])[0])
	if obj(after["mailboxIds"])[sent] != true || obj(after["keywords"])["$draft"] != nil {
		t.Errorf("after send: %v", after)
	}
	if len(list(e.must(r[5], "EmailSubmission/get")["list"])) != 1 {
		t.Errorf("submission/get: %v", r[5].Args)
	}

	if len(e.fake.sent) != 1 {
		t.Fatalf("sent %d messages", len(e.fake.sent))
	}
	s := e.fake.sent[0]
	if s.from != e.acct.Email || strings.Join(s.rcpts, ",") != "carl@remote.test,geheim@remote.test" {
		t.Errorf("envelope: %s %v", s.from, s.rcpts)
	}
	if bytes.Contains(s.data, []byte("geheim@remote.test")) || bytes.Contains(bytes.ToLower(s.data), []byte("\nbcc:")) {
		t.Error("Bcc header was not removed")
	}
	if !bytes.Contains(s.data, []byte(base64.StdEncoding.EncodeToString(attachment)[:20])) {
		t.Error("attachment missing in sent data")
	}

	// The download of the attachment part returns the decoded content.
	partBlob := obj(atts[0])["blobId"].(string)
	resp := e.do("GET", "/jmap/download/"+e.accountID+"/"+partBlob+"/angebot.bin", nil, nil)
	got, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if !bytes.Equal(got, attachment) {
		t.Errorf("part download: %q", got)
	}

	// Sending from a foreign address is refused.
	r = e.call(
		[]any{"Email/set", map[string]any{"accountId": e.accountID, "create": map[string]any{"x": map[string]any{
			"mailboxIds": map[string]bool{drafts: true}, "from": []any{map[string]any{"email": "boss@other.test"}},
			"to": []any{map[string]any{"email": "carl@remote.test"}}, "subject": "Fake", "textBody": []any{map[string]any{"partId": "1"}},
			"bodyValues": map[string]any{"1": map[string]any{"value": "x"}},
		}}}, "c"},
		[]any{"EmailSubmission/set", map[string]any{"accountId": e.accountID,
			"create": map[string]any{"s": map[string]any{"identityId": identityID, "emailId": "#x"}}}, "s"},
	)
	if obj(obj(e.must(r[1], "EmailSubmission/set")["notCreated"])["s"])["type"] != "forbiddenFrom" {
		t.Errorf("foreign from: %v", r[1].Args)
	}
	if len(e.fake.sent) != 1 {
		t.Error("forbidden mail was sent")
	}
}

func TestEmailImportAndParse(t *testing.T) {
	e := setup(t, true)
	raw := message.Build(message.Draft{From: store.Address{Email: "x@example.test"}, To: []store.Address{{Email: e.acct.Email}},
		Subject: "Importiert", Text: "Inhalt", MessageID: "imp1@example.test"})
	blobID := e.upload(raw, "message/rfc822")
	archive := e.mailboxID("archive")
	r := e.call(
		[]any{"Email/import", map[string]any{"accountId": e.accountID, "emails": map[string]any{"m": map[string]any{
			"blobId": blobID, "mailboxIds": map[string]bool{archive: true}, "keywords": map[string]bool{"$seen": true},
			"receivedAt": "2024-05-01T10:00:00Z"}}}, "i"},
		[]any{"Email/get", map[string]any{"accountId": e.accountID, "ids": []string{"#m"},
			"properties": []string{"subject", "receivedAt", "keywords", "blobId"}}, "g"},
		[]any{"Email/parse", map[string]any{"accountId": e.accountID, "blobIds": []string{blobID, "G" + strings.Repeat("a", 64)}}, "p"},
	)
	imp := e.must(r[0], "Email/import")
	if obj(imp["created"])["m"] == nil {
		t.Fatalf("import: %v", imp)
	}
	got := obj(list(e.must(r[1], "Email/get")["list"])[0])
	if got["subject"] != "Importiert" || got["receivedAt"] != "2024-05-01T10:00:00Z" || obj(got["keywords"])["$seen"] != true || got["blobId"] != blobID {
		t.Errorf("imported: %v", got)
	}
	p := e.must(r[2], "Email/parse")
	if obj(obj(p["parsed"])[blobID])["subject"] != "Importiert" || len(list(p["notFound"])) != 1 {
		t.Errorf("parse: %v", p)
	}
}

func TestEventSourceStateChange(t *testing.T) {
	e := setup(t, true)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, "GET", e.srv.URL+"/jmap/eventsource?types=*&closeafter=no&ping=0", nil)
	req.SetBasicAuth(e.acct.Email, testPassword)
	resp, err := e.srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 || !strings.HasPrefix(resp.Header.Get("Content-Type"), "text/event-stream") {
		t.Fatalf("eventsource: %d %s", resp.StatusCode, resp.Header.Get("Content-Type"))
	}
	events := make(chan string, 10)
	go func() {
		sc := bufio.NewScanner(resp.Body)
		var ev string
		for sc.Scan() {
			line := sc.Text()
			switch {
			case strings.HasPrefix(line, "event: "):
				ev = strings.TrimPrefix(line, "event: ")
			case strings.HasPrefix(line, "data: ") && ev == "state":
				events <- strings.TrimPrefix(line, "data: ")
			}
		}
		close(events)
	}()
	e.deliver("Push", "neu", nil)
	select {
	case data := <-events:
		var sc struct {
			Type    string                       `json:"@type"`
			Changed map[string]map[string]string `json:"changed"`
		}
		if err := json.Unmarshal([]byte(data), &sc); err != nil {
			t.Fatal(err)
		}
		ch := sc.Changed[e.accountID]
		if sc.Type != "StateChange" || ch["Email"] == "" || ch["Mailbox"] == "" || ch["EmailDelivery"] == "" {
			t.Errorf("state change: %s", data)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no StateChange event received")
	}
}

func TestChangesPruned(t *testing.T) {
	e := setup(t, true)
	state := e.call([]any{"Mailbox/get", map[string]any{"accountId": e.accountID, "ids": []string{}}, "0"})[0].Args["state"].(string)
	r := e.call([]any{"Mailbox/set", map[string]any{"accountId": e.accountID, "create": map[string]any{
		"a": map[string]any{"name": "Alt"}, "b": map[string]any{"name": "Neu"}}}, "0"})
	created := obj(e.must(r[0], "Mailbox/set")["created"])
	oldID, newID := obj(created["a"])["id"].(string), obj(created["b"])["id"].(string)
	e.call([]any{"Mailbox/set", map[string]any{"accountId": e.accountID, "destroy": []string{oldID}}, "0"})
	// Another change so the tombstone is not the newest Mailbox row.
	e.call([]any{"Mailbox/set", map[string]any{"accountId": e.accountID, "update": map[string]any{newID: map[string]any{"name": "Neuer"}}}, "0"})
	if err := e.st.PruneChanges(context.Background(), -time.Hour, 0, time.Hour); err != nil {
		t.Fatal(err)
	}
	r = e.call([]any{"Mailbox/changes", map[string]any{"accountId": e.accountID, "sinceState": state}, "0"})
	if r[0].Name != "error" || r[0].Args["type"] != "cannotCalculateChanges" {
		t.Errorf("after pruning: %v", r[0])
	}
}
