package api

import (
	"context"
	"net/http/httptest"
	"testing"

	"postwerk/internal/config"
	"postwerk/internal/store"
)

func newTestServer(t *testing.T, web store.WebSettings) *Server {
	t.Helper()
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	settings, _ := st.LoadSettings(context.Background(), "mail.example.com")
	cur := settings.Get()
	cur.Web = web
	settings.Save(context.Background(), cur)
	return &Server{Cfg: &config.Config{}, Store: st, Settings: settings}
}

func TestClientIPTrustedProxy(t *testing.T) {
	cases := []struct {
		name   string
		web    store.WebSettings
		remote string
		xff    string
		proto  string
		want   string
		https  bool
	}{
		{"no proxy configured ignores header", store.WebSettings{}, "203.0.113.5:1234", "1.2.3.4", "https", "203.0.113.5", false},
		{"empty list trusts loopback", store.WebSettings{BehindProxy: true}, "127.0.0.1:1234", "198.51.100.7", "https", "198.51.100.7", true},
		{"empty list does not trust private networks", store.WebSettings{BehindProxy: true}, "172.18.0.2:1234", "198.51.100.7", "https", "172.18.0.2", false},
		{"listed private proxy", store.WebSettings{BehindProxy: true, TrustedProxies: []string{"172.18.0.0/16"}}, "172.18.0.2:1234", "198.51.100.7", "https", "198.51.100.7", true},
		{"spoofed header from untrusted peer", store.WebSettings{BehindProxy: true}, "203.0.113.5:1234", "10.0.0.1", "https", "203.0.113.5", false},
		{"client prepends fake ip", store.WebSettings{BehindProxy: true, TrustedProxies: []string{"172.18.0.0/16"}}, "172.18.0.2:1234", "6.6.6.6, 198.51.100.7", "https", "198.51.100.7", true},
		{"explicit list excludes other private", store.WebSettings{BehindProxy: true, TrustedProxies: []string{"192.168.188.10"}}, "172.18.0.2:1234", "198.51.100.7", "", "172.18.0.2", false},
		{"cidr in list", store.WebSettings{BehindProxy: true, TrustedProxies: []string{"192.168.188.0/24"}}, "192.168.188.10:1", "198.51.100.7", "http", "198.51.100.7", false},
	}
	for _, c := range cases {
		s := newTestServer(t, c.web)
		r := httptest.NewRequest("GET", "/", nil)
		r.RemoteAddr = c.remote
		r.Header.Set("X-Forwarded-For", c.xff)
		if c.proto != "" {
			r.Header.Set("X-Forwarded-Proto", c.proto)
		}
		ci := s.resolveClient(r)
		if ci.client != c.want || ci.https != c.https {
			t.Errorf("%s: client=%s https=%v, want %s %v", c.name, ci.client, ci.https, c.want, c.https)
		}
	}
}

func TestClientIPMultipleForwardedForLines(t *testing.T) {
	s := newTestServer(t, store.WebSettings{BehindProxy: true, TrustedProxies: []string{"172.18.0.0/16"}})
	r := httptest.NewRequest("GET", "/", nil)
	r.RemoteAddr = "172.18.0.2:1234"
	// A client-supplied line followed by the line the proxy appended.
	r.Header.Add("X-Forwarded-For", "6.6.6.6")
	r.Header.Add("X-Forwarded-For", "198.51.100.7, 172.18.0.5")
	if ci := s.resolveClient(r); ci.client != "198.51.100.7" {
		t.Fatalf("client = %s, want the right-most untrusted address", ci.client)
	}
}

func TestForwardedProtoOnlyFromTrustedProxy(t *testing.T) {
	s := newTestServer(t, store.WebSettings{BehindProxy: true, TrustedProxies: []string{"192.168.1.10"}})
	r := httptest.NewRequest("GET", "/", nil)
	r.RemoteAddr = "192.168.1.99:1234"
	r.Header.Set("X-Forwarded-Proto", "https")
	if s.resolveClient(r).https || s.secureCookie(r) {
		t.Fatal("X-Forwarded-Proto from an untrusted peer must be ignored")
	}
	r.RemoteAddr = "192.168.1.10:1234"
	if !s.resolveClient(r).https || !s.secureCookie(r) {
		t.Fatal("X-Forwarded-Proto from the trusted proxy must be honoured")
	}
}
