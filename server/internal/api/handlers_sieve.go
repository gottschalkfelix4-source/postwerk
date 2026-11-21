package api

import (
	"errors"
	"net/http"
	"unicode/utf8"

	"postwerk/internal/message"
	"postwerk/internal/sieve"
	"postwerk/internal/store"
)

// sieveRoutes registers the Sieve filter endpoints (user scope).
func (s *Server) sieveRoutes(m *http.ServeMux) {
	m.HandleFunc("GET /api/mail/sieve", s.user(s.listSieve))
	m.HandleFunc("POST /api/mail/sieve/check", s.user(s.checkSieve))
	m.HandleFunc("POST /api/mail/sieve/test", s.user(s.testSieve))
	m.HandleFunc("POST /api/mail/sieve/deactivate", s.user(s.deactivateSieve))
	m.HandleFunc("GET /api/mail/sieve/{name}", s.user(s.getSieve))
	m.HandleFunc("PUT /api/mail/sieve/{name}", s.user(s.putSieve))
	m.HandleFunc("DELETE /api/mail/sieve/{name}", s.user(s.deleteSieve))
	m.HandleFunc("POST /api/mail/sieve/{name}/activate", s.user(s.activateSieve))
}

type sieveList struct {
	Scripts      []store.SieveScript `json:"scripts"`
	Active       *string             `json:"active"`
	Capabilities []string            `json:"capabilities"`
	MaxSize      int                 `json:"maxSize"`
	MaxScripts   int                 `json:"maxScripts"`
}

func (s *Server) listSieve(w http.ResponseWriter, r *http.Request, u *store.Account) (any, error) {
	list, err := s.Store.ListSieveScripts(r.Context(), u.ID)
	if err != nil {
		return nil, err
	}
	out := sieveList{Scripts: list, Capabilities: sieve.Capabilities(), MaxSize: store.MaxSieveScriptSize, MaxScripts: store.MaxSieveScripts}
	for _, sc := range list {
		if sc.Active {
			name := sc.Name
			out.Active = &name
		}
	}
	return out, nil
}

func sieveName(r *http.Request) (string, error) {
	name := r.PathValue("name")
	if !store.ValidSieveName(name) {
		return "", invalid("Ungültiger Skriptname")
	}
	return name, nil
}

func (s *Server) getSieve(w http.ResponseWriter, r *http.Request, u *store.Account) (any, error) {
	name, err := sieveName(r)
	if err != nil {
		return nil, err
	}
	return s.Store.GetSieveScript(r.Context(), u.ID, name)
}

// writeSieveError answers 400 with the German message and the position of
// a script error. It returns false when err is not a script error.
func (s *Server) writeSieveError(w http.ResponseWriter, err error) bool {
	var se *sieve.Error
	if !errors.As(err, &se) {
		return false
	}
	s.writeJSON(w, http.StatusBadRequest, map[string]any{
		"error": "invalid_script", "message": se.Error(), "line": se.Line, "column": se.Column, "detail": se.Msg,
	})
	return true
}

// validateSieve checks size, encoding and syntax of a script.
func (s *Server) validateSieve(w http.ResponseWriter, content string) (bool, error) {
	if len(content) > store.MaxSieveScriptSize {
		return false, invalid("Das Skript ist zu groß (höchstens 64 KB)")
	}
	if !utf8.ValidString(content) {
		return false, invalid("Das Skript muss UTF-8-kodiert sein")
	}
	if err := sieve.Check(content); err != nil {
		if s.writeSieveError(w, err) {
			return false, nil
		}
		return false, err
	}
	return true, nil
}

func (s *Server) checkSieve(w http.ResponseWriter, r *http.Request, u *store.Account) (any, error) {
	var req struct {
		Content string `json:"content"`
	}
	if err := decode(r, &req); err != nil {
		return nil, err
	}
	ok, err := s.validateSieve(w, req.Content)
	if !ok {
		return nil, err
	}
	return map[string]bool{"ok": true}, nil
}

func (s *Server) putSieve(w http.ResponseWriter, r *http.Request, u *store.Account) (any, error) {
	name, err := sieveName(r)
	if err != nil {
		return nil, err
	}
	var req struct {
		Content  string `json:"content"`
		Activate bool   `json:"activate"`
	}
	if err := decode(r, &req); err != nil {
		return nil, err
	}
	if ok, err := s.validateSieve(w, req.Content); !ok {
		return nil, err
	}
	if err := s.Store.PutSieveScript(r.Context(), u.ID, name, req.Content); err != nil {
		if errors.Is(err, store.ErrSieveQuota) {
			return nil, errf(409, "limit", "Höchstens 20 Skripte mit je 64 KB sind möglich")
		}
		return nil, err
	}
	if req.Activate {
		if err := s.Store.SetActiveSieveScript(r.Context(), u.ID, name); err != nil {
			return nil, err
		}
	}
	return s.Store.GetSieveScript(r.Context(), u.ID, name)
}

func (s *Server) activateSieve(w http.ResponseWriter, r *http.Request, u *store.Account) (any, error) {
	name, err := sieveName(r)
	if err != nil {
		return nil, err
	}
	sc, err := s.Store.GetSieveScript(r.Context(), u.ID, name)
	if err != nil {
		return nil, err
	}
	// Scripts stored before a validation change must still compile.
	if err := sieve.Check(sc.Content); err != nil {
		if s.writeSieveError(w, err) {
			return nil, nil
		}
		return nil, err
	}
	if err := s.Store.SetActiveSieveScript(r.Context(), u.ID, name); err != nil {
		return nil, err
	}
	return ok(), nil
}

func (s *Server) deactivateSieve(w http.ResponseWriter, r *http.Request, u *store.Account) (any, error) {
	if err := s.Store.SetActiveSieveScript(r.Context(), u.ID, ""); err != nil {
		return nil, err
	}
	return ok(), nil
}

func (s *Server) deleteSieve(w http.ResponseWriter, r *http.Request, u *store.Account) (any, error) {
	name, err := sieveName(r)
	if err != nil {
		return nil, err
	}
	if err := s.Store.DeleteSieveScript(r.Context(), u.ID, name); err != nil {
		if errors.Is(err, store.ErrSieveActive) {
			return nil, errf(409, "active", "Das aktive Skript kann nicht gelöscht werden – zuerst deaktivieren")
		}
		return nil, err
	}
	return ok(), nil
}

type sieveTestResult struct {
	Actions      []sieve.Action `json:"actions"`
	ImplicitKeep bool           `json:"implicitKeep"`
	Script       string         `json:"script"`
	Error        string         `json:"error,omitempty"`
}

// testSieve runs a script (the given content or the active script) against
// an existing message or an uploaded raw message without side effects.
func (s *Server) testSieve(w http.ResponseWriter, r *http.Request, u *store.Account) (any, error) {
	var req struct {
		Content   *string `json:"content"`
		MessageID int64   `json:"messageId"`
		Raw       string  `json:"raw"`
		EnvFrom   string  `json:"envelopeFrom"`
		EnvTo     string  `json:"envelopeTo"`
	}
	if err := decode(r, &req); err != nil {
		return nil, err
	}
	scriptName := ""
	var content string
	if req.Content != nil {
		content = *req.Content
		if ok, err := s.validateSieve(w, content); !ok {
			return nil, err
		}
	} else {
		active, err := s.Store.ActiveSieveScript(r.Context(), u.ID)
		if errors.Is(err, store.ErrNotFound) {
			return nil, invalid("Es ist kein Skript aktiv")
		}
		if err != nil {
			return nil, err
		}
		content, scriptName = active.Content, active.Name
	}
	var raw []byte
	switch {
	case req.MessageID > 0:
		m, err := s.Store.GetMessage(r.Context(), u.ID, req.MessageID)
		if err != nil {
			return nil, err
		}
		if raw, err = s.Store.Blobs.Get(m.BlobHash); err != nil {
			return nil, err
		}
	case req.Raw != "":
		raw = []byte(req.Raw)
	default:
		return nil, invalid("Bitte eine Nachricht auswählen oder eine Rohnachricht angeben")
	}
	envFrom := req.EnvFrom
	if envFrom == "" {
		if p, err := message.Parse(raw, false); err == nil {
			envFrom = p.From.Email
		}
	}
	res, err := s.Delivery.SieveDryRun(r.Context(), u, content, raw, envFrom, req.EnvTo)
	if err != nil {
		var se *sieve.Error
		if !errors.As(err, &se) {
			return nil, err
		}
		// Run-time errors lead to the implicit keep during delivery.
		return sieveTestResult{Actions: []sieve.Action{{Type: "keep", Implicit: true}}, ImplicitKeep: true, Script: scriptName, Error: se.Error()}, nil
	}
	return sieveTestResult{Actions: res.Final(), ImplicitKeep: res.ImplicitKeep, Script: scriptName}, nil
}
