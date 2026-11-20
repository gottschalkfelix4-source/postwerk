package api

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"strconv"
	"time"

	"postwerk/internal/dyndns"
	"postwerk/internal/queue"
	"postwerk/internal/store"
)

func (s *Server) dyndnsPresets(w http.ResponseWriter, r *http.Request, u *store.Account) (any, error) {
	return dyndns.Presets, nil
}

func (s *Server) dyndnsStatus(w http.ResponseWriter, r *http.Request, u *store.Account) (any, error) {
	return s.DynDNS.Status(r.Context()), nil
}

func (s *Server) dyndnsUpdate(w http.ResponseWriter, r *http.Request, u *store.Account) (any, error) {
	var req struct {
		Force bool `json:"force"`
	}
	if b, _ := io.ReadAll(io.LimitReader(r.Body, 1024)); len(b) > 0 {
		_ = json.Unmarshal(b, &req)
	}
	if !s.Settings.Get().DynDNS.Enabled {
		return nil, invalid("DynDNS ist nicht aktiviert – bitte zuerst speichern")
	}
	ctx, cancel := context.WithTimeout(r.Context(), 40*time.Second)
	defer cancel()
	st, _ := s.DynDNS.Check(ctx, req.Force)
	return st, nil
}

func (s *Server) relayPresets(w http.ResponseWriter, r *http.Request, u *store.Account) (any, error) {
	return map[string]any{"presets": queue.RelayPresets, "envOverride": s.Cfg.RelayHost != ""}, nil
}

func (s *Server) relayTest(w http.ResponseWriter, r *http.Request, u *store.Account) (any, error) {
	var relay queue.Relay
	if s.Cfg.RelayHost != "" {
		host := s.Cfg.RelayHost
		if _, _, err := net.SplitHostPort(host); err != nil {
			host = net.JoinHostPort(host, "587")
		}
		relay = queue.Relay{Host: host, User: s.Cfg.RelayUser, Password: s.Cfg.RelayPassword, Security: "starttls"}
	} else {
		c := s.Settings.Get().Relay
		if c.Host == "" {
			return map[string]any{"ok": false, "detail": "Kein Smarthost eingetragen"}, nil
		}
		relay = queue.Relay{Host: net.JoinHostPort(c.Host, strconv.Itoa(c.Port)), Security: c.Security, User: c.Username, Password: c.Password}
	}
	ctx, cancel := context.WithTimeout(r.Context(), 40*time.Second)
	defer cancel()
	start := time.Now()
	if err := queue.TestRelay(ctx, relay, s.Settings.Get().Server.Hostname); err != nil {
		return map[string]any{"ok": false, "detail": err.Error()}, nil
	}
	detail := "Verbindung, TLS"
	if relay.User != "" {
		detail += " und Anmeldung"
	}
	detail += " erfolgreich (" + relay.Host + ", " + time.Since(start).Round(time.Millisecond).String() + ")"
	return map[string]any{"ok": true, "detail": detail}, nil
}
