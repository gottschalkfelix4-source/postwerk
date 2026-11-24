package api

import (
	"context"
	"net/http"
	"time"

	"postwerk/internal/store"
)

// undoRoutes registers the webmail "Undo Send" endpoints.
func (s *Server) undoRoutes(m *http.ServeMux) {
	m.HandleFunc("POST /api/mail/send/pending/{id}/undo", s.user(s.undoPendingSend))
	m.HandleFunc("GET /api/mail/send/pending", s.user(s.listPendingSends))
}

// undoSeconds returns the configured undo window for webmail sends.
func (s *Server) undoSeconds() int {
	n := s.Settings.Get().Web.UndoSendSeconds
	if n < 0 {
		return 0
	}
	if n > 300 {
		return 300
	}
	return n
}

// parkSend stores a webmail send for the undo window and returns the pending
// id. It is used by the send handler instead of immediate submission.
func (s *Server) parkSend(ctx context.Context, u *store.Account, from string, rcpts []string, raw []byte) (*store.PendingSend, error) {
	sec := s.undoSeconds()
	if sec == 0 {
		return nil, nil
	}
	return s.Store.SavePendingSend(ctx, u.ID, from, rcpts, raw, time.Now().Add(time.Duration(sec)*time.Second))
}

func (s *Server) undoPendingSend(w http.ResponseWriter, r *http.Request, u *store.Account) (any, error) {
	id, err := pathID(r, "id")
	if err != nil {
		return nil, err
	}
	if err := s.Store.DeletePendingSend(r.Context(), u.ID, id); err != nil {
		if err == store.ErrNotFound {
			return nil, errNotFound
		}
		return nil, err
	}
	s.Log.Info("send undone", "component", "webmail", "user", u.Email, "pending", id)
	return ok(), nil
}

func (s *Server) listPendingSends(w http.ResponseWriter, r *http.Request, u *store.Account) (any, error) {
	pend, err := s.Store.DuePendingSends(r.Context(), time.Now().Add(time.Hour))
	if err != nil {
		return nil, err
	}
	out := []*store.PendingSend{}
	for _, p := range pend {
		if p.AccountID == u.ID {
			out = append(out, p)
		}
	}
	return map[string]any{"items": out}, nil
}
