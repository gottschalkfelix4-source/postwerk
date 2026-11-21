package api

import (
	"context"
	"net/http"
	"time"

	"postwerk/internal/ai"
	"postwerk/internal/store"
)

// Sign-in with a ChatGPT subscription for the AI provider "chatgpt". The
// device-code flow runs on the server; the browser only shows the code and
// polls the status. Tokens never leave the server.

func (s *Server) chatgptStatus(w http.ResponseWriter, r *http.Request, u *store.Account) (any, error) {
	return s.AI.ChatGPTStatus(r.Context())
}

func (s *Server) chatgptLogin(w http.ResponseWriter, r *http.Request, u *store.Account) (any, error) {
	l, err := s.AI.StartChatGPTLogin(r.Context())
	if err != nil {
		return nil, errf(502, "chatgpt_error", err.Error())
	}
	s.Log.Info("chatgpt sign-in started", "component", "ai", "by", u.Email)
	return l, nil
}

func (s *Server) chatgptLoginStatus(w http.ResponseWriter, r *http.Request, u *store.Account) (any, error) {
	l, ok := s.AI.ChatGPTLoginStatus(r.PathValue("id"))
	if !ok {
		return nil, errNotFound
	}
	return l, nil
}

func (s *Server) chatgptLogout(w http.ResponseWriter, r *http.Request, u *store.Account) (any, error) {
	if err := s.AI.LogoutChatGPT(r.Context()); err != nil {
		return nil, err
	}
	s.Log.Info("chatgpt signed out", "component", "ai", "by", u.Email)
	return ok(), nil
}

// chatGPTWarnings reports a lost sign-in and an exhausted plan limit.
func (s *Server) chatGPTWarnings(ctx context.Context, cfg store.AISettings) []dashWarning {
	if s.AI == nil || ai.Provider(cfg) != ai.ProviderChatGPT {
		return nil
	}
	var out []dashWarning
	a, err := s.Store.ChatGPTAuth(ctx)
	switch {
	case err != nil:
		out = append(out, dashWarning{ID: "ai-chatgpt-auth", Severity: "error", Title: "ChatGPT-Anmeldung nicht lesbar", Detail: err.Error(), Link: "/admin/settings/ai"})
	case a != nil && a.Error != "":
		out = append(out, dashWarning{ID: "ai-chatgpt-auth", Severity: "error", Title: "Verbindung zu ChatGPT getrennt",
			Detail: a.Error + " Die KI-Funktionen sind bis dahin ohne Funktion (Einstellungen → KI → Mit ChatGPT anmelden).", Link: "/admin/settings/ai"})
	case cfg.Enabled && !a.Connected():
		out = append(out, dashWarning{ID: "ai-chatgpt-auth", Severity: "warning", Title: "KI: Nicht mit ChatGPT angemeldet",
			Detail: "Als KI-Anbieter ist das ChatGPT-Abo gewählt, aber niemand ist angemeldet.", Link: "/admin/settings/ai"})
	}
	if l := s.AI.ChatGPTLimit(); l != nil && cfg.Enabled {
		detail := l.Msg + " Die Analyse neuer Mails pausiert so lange und wird danach nachgeholt."
		if !l.RetryAt.IsZero() && time.Until(l.RetryAt) > 24*time.Hour {
			detail += " Das Limit lässt sich nur über ein größeres Abo oder einen anderen KI-Anbieter umgehen."
		}
		out = append(out, dashWarning{ID: "ai-chatgpt-limit", Severity: "warning", Title: "ChatGPT-Nutzungslimit erreicht", Detail: detail, Link: "/admin/settings/ai"})
	}
	return out
}
