package dyndns

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"postwerk/internal/store"
)

func TestUpdaterOnlyOnChange(t *testing.T) {
	ip := "203.0.113.7"
	var updates atomic.Int32
	var gotHost, gotIP, gotUser string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/ip":
			fmt.Fprint(w, ip)
		case "/nic/update":
			u, p, _ := r.BasicAuth()
			if p != "geheim" {
				w.WriteHeader(401)
				fmt.Fprint(w, "badauth")
				return
			}
			updates.Add(1)
			gotHost, gotIP, gotUser = r.URL.Query().Get("hostname"), r.URL.Query().Get("myip"), u
			fmt.Fprintf(w, "good %s", r.URL.Query().Get("myip"))
		}
	}))
	defer srv.Close()
	ctx := context.Background()
	st, _ := store.Open(t.TempDir())
	defer st.Close()
	settings, _ := st.LoadSettings(ctx, "mail.example.invalid")
	cfg := settings.Get()
	cfg.DynDNS = store.DynDNSSettings{Enabled: true, UpdateURL: srv.URL + "/nic/update", Hostnames: []string{"mail.example.invalid"},
		Username: "mail.example.invalid", Password: "geheim", IPCheckURL: srv.URL + "/ip", IntervalMinutes: 5}
	settings.Save(ctx, cfg)
	u := New(settings, slog.New(slog.NewTextHandler(io.Discard, nil)))

	s, err := u.Check(ctx, false)
	if err != nil || updates.Load() != 1 || gotHost != "mail.example.invalid" || gotIP != ip || gotUser != "mail.example.invalid" {
		t.Fatalf("first update: err=%v n=%d host=%s ip=%s", err, updates.Load(), gotHost, gotIP)
	}
	if s.LastResult == nil || *s.LastResult != "good "+ip {
		t.Errorf("status = %+v", s)
	}
	u.Check(ctx, false)
	if updates.Load() != 1 {
		t.Fatalf("unchanged IP must not trigger an update (got %d)", updates.Load())
	}
	ip = "203.0.113.99"
	u.Check(ctx, false)
	if updates.Load() != 2 || gotIP != ip {
		t.Fatalf("changed IP must trigger an update")
	}
	// Wrong password pauses further attempts.
	cfg.DynDNS.Password = "falsch"
	settings.Save(ctx, cfg)
	ip = "203.0.113.100"
	s, err = u.Check(ctx, false)
	if err == nil || s.Error == nil || s.PausedUntil == nil {
		t.Fatalf("badauth must report and pause: %+v", s)
	}
	before := updates.Load()
	u.Check(ctx, false)
	if updates.Load() != before {
		t.Fatalf("paused updater must not call the provider")
	}
}

func TestInterpret(t *testing.T) {
	cases := map[string]bool{"good 1.2.3.4": true, "nochg 1.2.3.4": true, "good 1.2.3.4\nnochg 1.2.3.4": true,
		"abuse": false, "nohost": false, "911": false, "badauth": false}
	for body, want := range cases {
		if ok, _, _ := interpret(200, body); ok != want {
			t.Errorf("interpret(%q) = %v", body, ok)
		}
	}
}
