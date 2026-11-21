package dav

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/emersion/go-ical"
	"github.com/emersion/go-vcard"
	"github.com/emersion/go-webdav"
	"github.com/emersion/go-webdav/caldav"
	"github.com/emersion/go-webdav/carddav"

	"postwerk/internal/auth"
	"postwerk/internal/store"
)

const (
	testUser = "anna@example.test"
	testPass = "ein-sehr-gutes-passwort"
)

type env struct {
	st     *store.Store
	acct   *store.Account
	srv    *httptest.Server
	client webdav.HTTPClient
}

func newEnv(t *testing.T, insecure bool) *env {
	t.Helper()
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	h, _ := auth.HashPassword(testPass)
	acct := &store.Account{Email: testUser, Name: "Anna", PasswordHash: h, Role: "user"}
	if err := st.CreateAccount(context.Background(), acct); err != nil {
		t.Fatal(err)
	}
	other := &store.Account{Email: "bert@example.test", PasswordHash: h, Role: "user"}
	if err := st.CreateAccount(context.Background(), other); err != nil {
		t.Fatal(err)
	}
	guard := auth.NewGuard(func() auth.Limits { return auth.Limits{MaxAttempts: 100, Window: time.Minute, BanFor: time.Minute} })
	authn := &auth.Authenticator{Store: st, Guard: guard}
	dh := &Handler{
		Store: st,
		Authenticate: func(ctx context.Context, ip, user, pass string) (*store.Account, error) {
			return authn.Protocol(ctx, ip, user, pass, "dav")
		},
		Client: func(r *http.Request) (string, bool) {
			host, _, _ := net.SplitHostPort(r.RemoteAddr)
			return host, r.TLS != nil
		},
		AllowInsecure: func() bool { return insecure },
	}
	mux := http.NewServeMux()
	mux.Handle("/dav/", dh)
	mux.Handle("/.well-known/caldav", dh)
	mux.Handle("/.well-known/carddav", dh)
	srv := httptest.NewTLSServer(mux)
	t.Cleanup(srv.Close)
	return &env{st: st, acct: acct, srv: srv, client: webdav.HTTPClientWithBasicAuth(srv.Client(), testUser, testPass)}
}

func (e *env) raw(t *testing.T, method, path, body string, hdr map[string]string) *http.Response {
	t.Helper()
	req, _ := http.NewRequest(method, e.srv.URL+path, strings.NewReader(body))
	req.SetBasicAuth(testUser, testPass)
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	resp, err := e.srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { resp.Body.Close() })
	return resp
}

func readBody(t *testing.T, r *http.Response) string {
	t.Helper()
	b, _ := io.ReadAll(r.Body)
	return string(b)
}

func event(uid, summary, start, rrule string) *ical.Calendar {
	cal := ical.NewCalendar()
	cal.Props.SetText(ical.PropVersion, "2.0")
	cal.Props.SetText(ical.PropProductID, "-//test//EN")
	ev := ical.NewComponent(ical.CompEvent)
	ev.Props.SetText(ical.PropUID, uid)
	ev.Props.SetText(ical.PropSummary, summary)
	ev.Props.SetDateTime(ical.PropDateTimeStamp, time.Now().UTC())
	p := ical.NewProp(ical.PropDateTimeStart)
	p.Value = start
	ev.Props.Set(p)
	e := ical.NewProp(ical.PropDuration)
	e.Value = "PT1H"
	ev.Props.Set(e)
	if rrule != "" {
		r := ical.NewProp(ical.PropRecurrenceRule)
		r.Value = rrule
		ev.Props.Set(r)
	}
	cal.Children = append(cal.Children, ev)
	return cal
}

func TestCalDAVRoundTrip(t *testing.T) {
	e := newEnv(t, false)
	ctx := context.Background()
	c, err := caldav.NewClient(e.client, e.srv.URL+"/dav/")
	if err != nil {
		t.Fatal(err)
	}
	principal, err := c.FindCurrentUserPrincipal(ctx)
	if err != nil || principal != "/dav/principals/anna@example.test/" {
		t.Fatalf("principal = %q, %v", principal, err)
	}
	home, err := c.FindCalendarHomeSet(ctx, principal)
	if err != nil || home != "/dav/calendars/anna@example.test/" {
		t.Fatalf("home = %q, %v", home, err)
	}
	cals, err := c.FindCalendars(ctx, home)
	if err != nil || len(cals) != 1 || cals[0].Name != "Kalender" {
		t.Fatalf("calendars = %+v, %v", cals, err)
	}
	calPath := cals[0].Path
	if !strings.Contains(strings.Join(cals[0].SupportedComponentSet, ","), "VTODO") {
		t.Fatalf("default calendar must support VTODO: %v", cals[0].SupportedComponentSet)
	}

	obj, err := c.PutCalendarObject(ctx, calPath+"a.ics", event("uid-a", "Zahnarzt", "20261201T090000Z", ""), nil)
	if err != nil {
		t.Fatal(err)
	}
	if obj.ETag == "" {
		t.Fatal("PUT must return an ETag")
	}
	if _, err := c.PutCalendarObject(ctx, calPath+"b.ics", event("uid-b", "Sport", "20261101T180000Z", "FREQ=WEEKLY;COUNT=10"), nil); err != nil {
		t.Fatal(err)
	}

	// calendar-multiget
	got, err := c.MultiGetCalendar(ctx, calPath, &caldav.CalendarMultiGet{
		Paths:       []string{calPath + "a.ics", calPath + "b.ics"},
		CompRequest: caldav.CalendarCompRequest{AllProps: true, AllComps: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("multiget returned %d objects", len(got))
	}
	for _, o := range got {
		if o.Data == nil || o.ETag == "" {
			t.Fatalf("multiget object incomplete: %+v", o)
		}
	}

	// calendar-query with a time range: the weekly series has an instance
	// in December even though it starts in November.
	q := &caldav.CalendarQuery{
		CompRequest: caldav.CalendarCompRequest{Name: "VCALENDAR", AllProps: true, AllComps: true},
		CompFilter: caldav.CompFilter{Name: "VCALENDAR", Comps: []caldav.CompFilter{{
			Name:  "VEVENT",
			Start: time.Date(2026, 12, 7, 0, 0, 0, 0, time.UTC),
			End:   time.Date(2026, 12, 9, 0, 0, 0, 0, time.UTC),
		}}},
	}
	res, err := c.QueryCalendar(ctx, calPath, q)
	if err != nil {
		t.Fatal(err)
	}
	if len(res) != 1 || !strings.HasSuffix(res[0].Path, "b.ics") {
		t.Fatalf("query returned %+v", res)
	}

	// sync-collection: initial, then incremental after a delete.
	sync, err := c.SyncCollection(ctx, calPath, &caldav.SyncQuery{CompRequest: caldav.CalendarCompRequest{AllProps: true}})
	if err != nil {
		t.Fatal(err)
	}
	if len(sync.Updated) != 2 || sync.SyncToken == "" {
		t.Fatalf("initial sync = %+v", sync)
	}
	if err := c.RemoveAll(ctx, calPath+"a.ics", nil); err != nil {
		t.Fatal(err)
	}
	sync2, err := c.SyncCollection(ctx, calPath, &caldav.SyncQuery{SyncToken: sync.SyncToken})
	if err != nil {
		t.Fatal(err)
	}
	if len(sync2.Updated) != 0 || len(sync2.Deleted) != 1 || !strings.HasSuffix(sync2.Deleted[0], "a.ics") {
		t.Fatalf("incremental sync = %+v", sync2)
	}
	if sync2.SyncToken == sync.SyncToken {
		t.Fatal("sync token must change")
	}
}

func TestPropfindCTagAndPreconditions(t *testing.T) {
	e := newEnv(t, false)
	propfind := `<?xml version="1.0"?><d:propfind xmlns:d="DAV:" xmlns:cs="http://calendarserver.org/ns/"><d:prop><cs:getctag/><d:sync-token/><d:displayname/><d:unknown-prop/></d:prop></d:propfind>`
	ctag := func() string {
		r := e.raw(t, "PROPFIND", "/dav/calendars/anna@example.test/default/", propfind, map[string]string{"Depth": "0"})
		if r.StatusCode != http.StatusMultiStatus {
			t.Fatalf("PROPFIND status %d", r.StatusCode)
		}
		body := readBody(t, r)
		if !strings.Contains(body, "404 Not Found") {
			t.Fatalf("unknown property must be reported as 404: %s", body)
		}
		i := strings.Index(body, "<cs:getctag>")
		j := strings.Index(body, "</cs:getctag>")
		if i < 0 || j < i {
			t.Fatalf("no getctag: %s", body)
		}
		return body[i:j]
	}
	before := ctag()
	ics := "BEGIN:VCALENDAR\r\nVERSION:2.0\r\nPRODID:x\r\nBEGIN:VTODO\r\nUID:t1\r\nDTSTAMP:20260101T000000Z\r\nSUMMARY:Steuer\r\nEND:VTODO\r\nEND:VCALENDAR\r\n"
	r := e.raw(t, "PUT", "/dav/calendars/anna@example.test/default/t1.ics", ics, map[string]string{"Content-Type": "text/calendar", "If-None-Match": "*"})
	if r.StatusCode != http.StatusCreated {
		t.Fatalf("PUT status %d: %s", r.StatusCode, readBody(t, r))
	}
	etag := r.Header.Get("ETag")
	if after := ctag(); after == before {
		t.Fatal("getctag must change after PUT")
	}
	// If-None-Match: * on an existing resource fails.
	if r := e.raw(t, "PUT", "/dav/calendars/anna@example.test/default/t1.ics", ics, map[string]string{"If-None-Match": "*"}); r.StatusCode != http.StatusPreconditionFailed {
		t.Fatalf("If-None-Match on existing: %d", r.StatusCode)
	}
	// Wrong If-Match fails, correct one succeeds with 204.
	if r := e.raw(t, "PUT", "/dav/calendars/anna@example.test/default/t1.ics", ics, map[string]string{"If-Match": `"nope"`}); r.StatusCode != http.StatusPreconditionFailed {
		t.Fatalf("wrong If-Match: %d", r.StatusCode)
	}
	changed := strings.Replace(ics, "Steuer", "Steuererklärung", 1)
	r = e.raw(t, "PUT", "/dav/calendars/anna@example.test/default/t1.ics", changed, map[string]string{"If-Match": etag})
	if r.StatusCode != http.StatusNoContent || r.Header.Get("ETag") == etag {
		t.Fatalf("update: %d, etag %q -> %q", r.StatusCode, etag, r.Header.Get("ETag"))
	}
	// Same UID under another name is a conflict.
	r = e.raw(t, "PUT", "/dav/calendars/anna@example.test/default/other.ics", ics, nil)
	if r.StatusCode != http.StatusConflict || !strings.Contains(readBody(t, r), "no-uid-conflict") {
		t.Fatalf("uid conflict: %d", r.StatusCode)
	}
	// Invalid data is rejected.
	if r := e.raw(t, "PUT", "/dav/calendars/anna@example.test/default/bad.ics", "kein kalender", nil); r.StatusCode != http.StatusForbidden {
		t.Fatalf("invalid data: %d", r.StatusCode)
	}
}

func TestMkcalendarAndProppatch(t *testing.T) {
	e := newEnv(t, false)
	body := `<?xml version="1.0"?><c:mkcalendar xmlns:d="DAV:" xmlns:c="urn:ietf:params:xml:ns:caldav" xmlns:a="http://apple.com/ns/ical/"><d:set><d:prop><d:displayname>Arbeit</d:displayname><a:calendar-color>#FF0000FF</a:calendar-color><c:supported-calendar-component-set><c:comp name="VEVENT"/></c:supported-calendar-component-set></d:prop></d:set></c:mkcalendar>`
	if r := e.raw(t, "MKCALENDAR", "/dav/calendars/anna@example.test/work/", body, nil); r.StatusCode != http.StatusCreated {
		t.Fatalf("MKCALENDAR: %d %s", r.StatusCode, readBody(t, r))
	}
	c, err := e.st.GetCollectionBySlug(context.Background(), e.acct.ID, store.KindCalendar, "work")
	if err != nil || c.Name != "Arbeit" || c.Color != "#FF0000" || c.Supports("VTODO") {
		t.Fatalf("created calendar = %+v, %v", c, err)
	}
	patch := `<?xml version="1.0"?><d:propertyupdate xmlns:d="DAV:"><d:set><d:prop><d:displayname>Büro</d:displayname></d:prop></d:set></d:propertyupdate>`
	if r := e.raw(t, "PROPPATCH", "/dav/calendars/anna@example.test/work/", patch, nil); r.StatusCode != http.StatusMultiStatus {
		t.Fatalf("PROPPATCH: %d", r.StatusCode)
	}
	c, _ = e.st.GetCollectionBySlug(context.Background(), e.acct.ID, store.KindCalendar, "work")
	if c.Name != "Büro" {
		t.Fatalf("name after PROPPATCH = %q", c.Name)
	}
	// Extended MKCOL creates an address book.
	mk := `<?xml version="1.0"?><d:mkcol xmlns:d="DAV:" xmlns:card="urn:ietf:params:xml:ns:carddav"><d:set><d:prop><d:resourcetype><d:collection/><card:addressbook/></d:resourcetype><d:displayname>Familie</d:displayname></d:prop></d:set></d:mkcol>`
	if r := e.raw(t, "MKCOL", "/dav/addressbooks/anna@example.test/family/", mk, nil); r.StatusCode != http.StatusCreated {
		t.Fatalf("MKCOL: %d %s", r.StatusCode, readBody(t, r))
	}
}

func TestCardDAVQuery(t *testing.T) {
	e := newEnv(t, false)
	ctx := context.Background()
	c, err := carddav.NewClient(e.client, e.srv.URL+"/dav/")
	if err != nil {
		t.Fatal(err)
	}
	home, err := c.FindAddressBookHomeSet(ctx, "/dav/principals/anna@example.test/")
	if err != nil || home != "/dav/addressbooks/anna@example.test/" {
		t.Fatalf("home = %q, %v", home, err)
	}
	books, err := c.FindAddressBooks(ctx, home)
	if err != nil || len(books) != 1 || books[0].Name != "Kontakte" {
		t.Fatalf("books = %+v, %v", books, err)
	}
	path := books[0].Path
	for _, p := range []struct{ uid, fn, email string }{
		{"c1", "Erika Mustermann", "erika@example.org"},
		{"c2", "Max Muster", "max@example.net"},
	} {
		card := vcard.Card{}
		card.SetValue(vcard.FieldVersion, "3.0")
		card.SetValue(vcard.FieldUID, p.uid)
		card.SetValue(vcard.FieldFormattedName, p.fn)
		card.SetValue(vcard.FieldEmail, p.email)
		if _, err := c.PutAddressObject(ctx, path+p.uid+".vcf", card, nil); err != nil {
			t.Fatal(err)
		}
	}
	res, err := c.QueryAddressBook(ctx, path, &carddav.AddressBookQuery{
		DataRequest: carddav.AddressDataRequest{AllProp: true},
		PropFilters: []carddav.PropFilter{{Name: vcard.FieldEmail, TextMatches: []carddav.TextMatch{{Text: "ERIKA@", MatchType: carddav.MatchStartsWith}}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(res) != 1 || res[0].Card.PreferredValue(vcard.FieldFormattedName) != "Erika Mustermann" {
		t.Fatalf("query = %+v", res)
	}
	all, err := c.MultiGetAddressBook(ctx, path, &carddav.AddressBookMultiGet{Paths: []string{path + "c1.vcf", path + "c2.vcf"}, DataRequest: carddav.AddressDataRequest{AllProp: true}})
	if err != nil || len(all) != 2 {
		t.Fatalf("multiget = %d, %v", len(all), err)
	}
}

func TestAuthentication(t *testing.T) {
	e := newEnv(t, false)
	// Plain HTTP is refused even with valid credentials.
	plain := httptest.NewServer(e.srv.Config.Handler)
	defer plain.Close()
	req, _ := http.NewRequest("PROPFIND", plain.URL+"/dav/", nil)
	req.SetBasicAuth(testUser, testPass)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden || resp.Header.Get("WWW-Authenticate") != "" {
		t.Fatalf("plain HTTP: %d", resp.StatusCode)
	}

	// Missing and wrong credentials get a Basic challenge.
	req, _ = http.NewRequest("PROPFIND", e.srv.URL+"/dav/", nil)
	resp, _ = e.srv.Client().Do(req)
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized || !strings.HasPrefix(resp.Header.Get("WWW-Authenticate"), "Basic") {
		t.Fatalf("no auth: %d", resp.StatusCode)
	}
	req, _ = http.NewRequest("PROPFIND", e.srv.URL+"/dav/", nil)
	req.SetBasicAuth(testUser, "falsch")
	resp, _ = e.srv.Client().Do(req)
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("wrong password: %d", resp.StatusCode)
	}

	// Another user's collections are off limits.
	if r := e.raw(t, "PROPFIND", "/dav/calendars/bert@example.test/", "", map[string]string{"Depth": "1"}); r.StatusCode != http.StatusForbidden {
		t.Fatalf("foreign home: %d", r.StatusCode)
	}

	// Well-known redirect.
	noFollow := e.srv.Client()
	noFollow.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	resp, err = noFollow.Get(e.srv.URL + "/.well-known/caldav")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusMovedPermanently || resp.Header.Get("Location") != "/dav/" {
		t.Fatalf("well-known: %d %s", resp.StatusCode, resp.Header.Get("Location"))
	}
}

func TestPlainHTTPAllowedWhenConfigured(t *testing.T) {
	e := newEnv(t, true)
	plain := httptest.NewServer(e.srv.Config.Handler)
	defer plain.Close()
	req, _ := http.NewRequest("PROPFIND", plain.URL+"/dav/", nil)
	req.SetBasicAuth(testUser, testPass)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusMultiStatus {
		t.Fatalf("status %d", resp.StatusCode)
	}
}
