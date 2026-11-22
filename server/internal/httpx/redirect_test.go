package httpx

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestSameHostRedirect(t *testing.T) {
	var leaked string
	evil := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		leaked = r.Header.Get("X-Api-Key")
	}))
	defer evil.Close()
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/away":
			http.Redirect(w, r, evil.URL+"/steal", http.StatusTemporaryRedirect)
		case "/slash":
			http.Redirect(w, r, "/slash/", http.StatusMovedPermanently)
		case "/slash/":
			w.Write([]byte(r.Header.Get("X-Api-Key")))
		}
	}))
	defer srv.Close()
	c := &http.Client{CheckRedirect: SameHostRedirect}
	do := func(path string) *http.Response {
		req, _ := http.NewRequest("GET", srv.URL+path, nil)
		req.Header.Set("X-Api-Key", "secret")
		resp, err := c.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		return resp
	}
	if resp := do("/away"); resp.StatusCode != http.StatusTemporaryRedirect || leaked != "" {
		t.Fatalf("cross-host redirect followed: %d, leaked %q", resp.StatusCode, leaked)
	}
	if resp := do("/slash"); resp.StatusCode != http.StatusOK {
		t.Fatalf("same-host redirect not followed: %d", resp.StatusCode)
	}
}
