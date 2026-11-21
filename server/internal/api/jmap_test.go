package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"postwerk/internal/jmap"
)

// TestJMAPMounted checks the JMAP routes behind the API middleware: cookie
// authentication needs the CSRF header for POST, Basic auth needs HTTPS.
func TestJMAPMounted(t *testing.T) {
	e := newAuthEnv(t)
	e.s.JMAP = &jmap.Server{Store: e.s.Store, Auth: e.s.Auth, Log: e.s.Log}
	e.h = e.s.Handler()
	cookie := e.login(t, "")

	body := `{"using":["urn:ietf:params:jmap:core","urn:ietf:params:jmap:mail"],"methodCalls":[["Mailbox/get",{"accountId":"A1"},"0"]]}`
	post := func(csrf bool) *httptest.ResponseRecorder {
		r := httptest.NewRequest("POST", "/jmap/api", strings.NewReader(body))
		r.RemoteAddr = "198.51.100.10:5555"
		r.Header.Set("Content-Type", "application/json")
		if csrf {
			r.Header.Set("X-Postwerk", "1")
		}
		r.AddCookie(cookie)
		rec := httptest.NewRecorder()
		e.h.ServeHTTP(rec, r)
		return rec
	}
	if rec := post(false); rec.Code != http.StatusForbidden {
		t.Fatalf("cookie without CSRF header: %d", rec.Code)
	}
	rec := post(true)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"Mailbox/get"`) {
		t.Fatalf("cookie with CSRF header: %d %s", rec.Code, rec.Body.String())
	}

	r := httptest.NewRequest("GET", "/.well-known/jmap", nil)
	r.RemoteAddr = "198.51.100.10:5555"
	r.SetBasicAuth(e.acct.Email, testPassword)
	rec = httptest.NewRecorder()
	e.h.ServeHTTP(rec, r)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("basic auth over http: %d", rec.Code)
	}
}
