package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"postwerk/internal/pim"
	"postwerk/internal/store"
)

func decodeJSON[T any](t *testing.T, rec *httptest.ResponseRecorder) T {
	t.Helper()
	var v T
	if err := json.Unmarshal(rec.Body.Bytes(), &v); err != nil {
		t.Fatalf("decode %q: %v", rec.Body.String(), err)
	}
	return v
}

func TestCalendarRESTWithRecurrence(t *testing.T) {
	e := newAuthEnv(t)
	c := e.login(t, "")
	rec := e.do(t, "POST", "/api/calendar/events", map[string]any{
		"title": "Sport", "start": "2026-11-02T17:00:00Z", "end": "2026-11-02T18:00:00Z", "rrule": "FREQ=WEEKLY;COUNT=3", "location": "Halle",
	}, c)
	if rec.Code != 200 {
		t.Fatalf("create: %d %s", rec.Code, rec.Body.String())
	}
	ev := decodeJSON[pim.Event](t, rec)
	if ev.ID == 0 || ev.CalendarID == 0 || ev.RRule == nil || *ev.RRule != "FREQ=WEEKLY;COUNT=3" {
		t.Fatalf("event = %+v", ev)
	}
	rec = e.do(t, "GET", "/api/calendar/events?from=2026-11-01T00:00:00Z&to=2026-12-01T00:00:00Z", nil, c)
	list := decodeJSON[[]pim.Event](t, rec)
	if len(list) != 3 || list[1].Start != "2026-11-09T17:00:00Z" || list[1].Instance == nil || list[1].ID != ev.ID {
		t.Fatalf("expanded = %+v", list)
	}
	// Move the series by editing its second occurrence.
	rec = e.do(t, "PUT", "/api/calendar/events/"+id64(ev.ID), map[string]any{
		"title": "Sport", "start": "2026-11-09T18:00:00Z", "end": "2026-11-09T19:00:00Z", "instance": "2026-11-09T17:00:00Z", "location": "Halle",
	}, c)
	if rec.Code != 200 {
		t.Fatalf("update: %d %s", rec.Code, rec.Body.String())
	}
	if up := decodeJSON[pim.Event](t, rec); up.Start != "2026-11-02T18:00:00Z" || up.RRule == nil {
		t.Fatalf("updated = %+v", up)
	}
	// All-day event in a second calendar.
	rec = e.do(t, "POST", "/api/calendars", map[string]any{"name": "Privat", "color": "#16a34a"}, c)
	cal := decodeJSON[struct {
		ID  int64  `json:"id"`
		URL string `json:"url"`
	}](t, rec)
	if cal.ID == 0 || !strings.HasPrefix(cal.URL, "/dav/calendars/anna@example.test/") || !strings.HasSuffix(cal.URL, "/") {
		t.Fatalf("calendar = %+v", cal)
	}
	rec = e.do(t, "POST", "/api/calendar/events", map[string]any{"title": "Urlaub", "start": "2026-12-24", "end": "2026-12-27", "allDay": true, "calendarId": cal.ID}, c)
	if allDay := decodeJSON[pim.Event](t, rec); !allDay.AllDay || allDay.End == nil || *allDay.End != "2026-12-27" || allDay.CalendarID != cal.ID {
		t.Fatalf("all-day = %s", rec.Body.String())
	}
	// ICS export of the default calendar and re-import into the second one.
	rec = e.do(t, "GET", "/api/calendars/"+id64(ev.CalendarID)+"/export", nil, c)
	if rec.Code != 200 || !strings.Contains(rec.Header().Get("Content-Disposition"), "attachment") || !strings.Contains(rec.Body.String(), "RRULE:FREQ=WEEKLY;COUNT=3") {
		t.Fatalf("export: %d %s", rec.Code, rec.Body.String())
	}
	rec = e.do(t, "POST", "/api/calendars/"+id64(cal.ID)+"/import", map[string]string{"data": rec.Body.String()}, c)
	if res := decodeJSON[importResult](t, rec); res.Imported != 1 {
		t.Fatalf("import: %s", rec.Body.String())
	}
	// The last calendar cannot be deleted, others can.
	if rec := e.do(t, "DELETE", "/api/calendars/"+id64(cal.ID), nil, c); rec.Code != 200 {
		t.Fatalf("delete calendar: %d", rec.Code)
	}
	if rec := e.do(t, "DELETE", "/api/calendars/"+id64(ev.CalendarID), nil, c); rec.Code != 409 {
		t.Fatalf("delete last calendar: %d", rec.Code)
	}
	if rec := e.do(t, "DELETE", "/api/calendar/events/"+id64(ev.ID), nil, c); rec.Code != 200 {
		t.Fatalf("delete event: %d", rec.Code)
	}
}

func TestTasksREST(t *testing.T) {
	e := newAuthEnv(t)
	c := e.login(t, "")
	rec := e.do(t, "POST", "/api/tasks", map[string]any{"title": "Rechnung zahlen", "due": "2026-11-30"}, c)
	task := decodeJSON[pim.Task](t, rec)
	if task.ID == 0 || task.Due == nil || *task.Due != "2026-11-30" || task.Done {
		t.Fatalf("task = %s", rec.Body.String())
	}
	rec = e.do(t, "PATCH", "/api/tasks/"+id64(task.ID), map[string]any{"done": true}, c)
	if got := decodeJSON[pim.Task](t, rec); !got.Done || got.Title != "Rechnung zahlen" {
		t.Fatalf("patched = %s", rec.Body.String())
	}
	rec = e.do(t, "GET", "/api/tasks", nil, c)
	if list := decodeJSON[[]pim.Task](t, rec); len(list) != 1 || !list[0].Done {
		t.Fatalf("list = %s", rec.Body.String())
	}
	// Tasks are not events.
	if rec := e.do(t, "DELETE", "/api/calendar/events/"+id64(task.ID), nil, c); rec.Code != 404 {
		t.Fatalf("delete task as event: %d", rec.Code)
	}
}

func TestContactsREST(t *testing.T) {
	e := newAuthEnv(t)
	c := e.login(t, "")
	rec := e.do(t, "POST", "/api/contacts", map[string]any{
		"firstName": "Erika", "lastName": "Mustermann", "emails": []map[string]string{{"type": "work", "value": "erika@muster.de"}},
		"phones": []map[string]string{{"type": "cell", "value": "+49 170 1"}}, "organization": "Muster GmbH", "birthday": "1985-03-14",
	}, c)
	ct := decodeJSON[pim.Contact](t, rec)
	if ct.ID == 0 || ct.Name != "Erika Mustermann" || ct.AddressBookID == 0 {
		t.Fatalf("contact = %s", rec.Body.String())
	}
	rec = e.do(t, "PUT", "/api/contacts/"+id64(ct.ID), map[string]any{"name": "Erika M.", "firstName": "Erika", "lastName": "Mustermann",
		"emails": []map[string]string{{"type": "work", "value": "erika@muster.de"}, {"type": "home", "value": "erika@example.org"}}}, c)
	if up := decodeJSON[pim.Contact](t, rec); up.Name != "Erika M." || len(up.Emails) != 2 || up.Birthday != nil || up.Organization != "" {
		t.Fatalf("updated = %s", rec.Body.String())
	}
	rec = e.do(t, "GET", "/api/contacts?q=example.org", nil, c)
	if list := decodeJSON[[]pim.Contact](t, rec); len(list) != 1 {
		t.Fatalf("search = %s", rec.Body.String())
	}
	rec = e.do(t, "GET", "/api/contacts/suggest?q=erika", nil, c)
	if s := decodeJSON[[]suggestion](t, rec); len(s) != 2 || s[0].Source != "contact" {
		t.Fatalf("suggest = %s", rec.Body.String())
	}
	// vCard export and import (import updates by UID).
	rec = e.do(t, "GET", "/api/contacts/export", nil, c)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), "BEGIN:VCARD") {
		t.Fatalf("export: %d", rec.Code)
	}
	vcf := rec.Body.String() + "BEGIN:VCARD\r\nVERSION:3.0\r\nFN:Max Muster\r\nEMAIL:max@example.net\r\nEND:VCARD\r\n"
	rec = e.do(t, "POST", "/api/contacts/import", map[string]string{"data": vcf}, c)
	if res := decodeJSON[importResult](t, rec); res.Imported != 1 || res.Updated != 1 {
		t.Fatalf("import = %s", rec.Body.String())
	}
	if rec := e.do(t, "DELETE", "/api/contacts/"+id64(ct.ID), nil, c); rec.Code != 200 {
		t.Fatalf("delete: %d", rec.Code)
	}
	rec = e.do(t, "GET", "/api/addressbooks", nil, c)
	if books := decodeJSON[[]struct {
		Count int `json:"count"`
	}](t, rec); len(books) != 1 || books[0].Count != 1 {
		t.Fatalf("books = %s", rec.Body.String())
	}
	rec = e.do(t, "GET", "/api/account/apple-profile", nil, c)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), "com.apple.caldav.account") || strings.Contains(rec.Body.String(), "Password</key><string>") {
		t.Fatalf("profile: %d", rec.Code)
	}
}

func TestDAVThroughAPIHandler(t *testing.T) {
	e := newAuthEnv(t)
	propfind := func(remote, proto string) int {
		r := httptest.NewRequest("PROPFIND", "/dav/", nil)
		r.RemoteAddr = remote
		if proto != "" {
			r.Header.Set("X-Forwarded-Proto", proto)
		}
		r.SetBasicAuth(e.acct.Email, testPassword)
		rec := httptest.NewRecorder()
		e.h.ServeHTTP(rec, r)
		return rec.Code
	}
	// Plain HTTP, also with a forged header from an untrusted client.
	if code := propfind("198.51.100.10:5555", ""); code != http.StatusForbidden {
		t.Fatalf("plain HTTP: %d", code)
	}
	if code := propfind("198.51.100.10:5555", "https"); code != http.StatusForbidden {
		t.Fatalf("forged X-Forwarded-Proto: %d", code)
	}
	// Through a trusted reverse proxy that terminated TLS.
	cur := e.s.Settings.Get()
	cur.Web.BehindProxy = true
	cur.Web.TrustedProxies = []string{"10.0.0.0/8"}
	if err := e.s.Settings.Save(context.Background(), cur); err != nil {
		t.Fatal(err)
	}
	if code := propfind("10.0.0.2:4444", "https"); code != http.StatusMultiStatus {
		t.Fatalf("trusted proxy: %d", code)
	}
	// The CSRF header of the JSON API is not required for DAV writes.
	r := httptest.NewRequest("PUT", "/dav/addressbooks/anna@example.test/default/x.vcf",
		strings.NewReader("BEGIN:VCARD\r\nVERSION:3.0\r\nUID:x\r\nFN:X\r\nEND:VCARD\r\n"))
	r.RemoteAddr = "10.0.0.2:4444"
	r.Header.Set("X-Forwarded-Proto", "https")
	r.SetBasicAuth(e.acct.Email, testPassword)
	rec := httptest.NewRecorder()
	e.h.ServeHTTP(rec, r)
	if rec.Code != http.StatusCreated {
		t.Fatalf("PUT: %d %s", rec.Code, rec.Body.String())
	}
	books, _ := e.s.Store.ListCollections(context.Background(), e.acct.ID, store.KindAddressBook)
	if n := e.s.Store.CountObjects(context.Background(), books[0]); n != 1 {
		t.Fatalf("objects = %d", n)
	}
}

func id64(n int64) string { return strconv.FormatInt(n, 10) }
