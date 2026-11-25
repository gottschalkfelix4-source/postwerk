package api

import (
	"context"
	"net/http"

	"postwerk/internal/message"
	"postwerk/internal/spam"
	"postwerk/internal/store"
)

// quarantineRoutes registers the admin quarantine (junk overview) endpoints.
func (s *Server) quarantineRoutes(m *http.ServeMux) {
	m.HandleFunc("GET /api/admin/quarantine", s.admin(s.quarantineList))
	m.HandleFunc("POST /api/admin/quarantine/{id}/release", s.admin(s.quarantineRelease))
	m.HandleFunc("POST /api/admin/quarantine/{id}/delete", s.admin(s.quarantineDelete))
}

// quarantineList shows the newest messages in the junk folders of all
// accounts; admins can release them (inbox + "not spam" training) or delete
// them (confirming the spam classification trains the Bayes filter).
func (s *Server) quarantineList(w http.ResponseWriter, r *http.Request, u *store.Account) (any, error) {
	const limit = 200
	items, _, err := s.Store.QuarantineList(r.Context(), limit)
	if err != nil {
		return nil, err
	}
	if items == nil {
		items = []store.JunkMessage{}
	}
	return map[string]any{"items": items, "limit": limit}, nil
}

func (s *Server) quarantineRelease(w http.ResponseWriter, r *http.Request, u *store.Account) (any, error) {
	id, err := pathID(r, "id")
	if err != nil {
		return nil, err
	}
	m, err := s.Store.GetMessageAny(r.Context(), id)
	if err != nil {
		return nil, errNotFound
	}
	junk, err := s.Store.MailboxByRole(r.Context(), m.AccountID, "junk")
	if err != nil || m.MailboxID != junk {
		return nil, errNotFound
	}
	inbox, err := s.Store.MailboxByRole(r.Context(), m.AccountID, "inbox")
	if err != nil {
		return nil, err
	}
	if err := s.Store.UpdateMessage(r.Context(), m.AccountID, m.ID, store.MessagePatch{MailboxID: &inbox}); err != nil {
		return nil, err
	}
	// Feedback loop: a released message trains the Bayes filter as "not spam".
	s.trainMessage(r.Context(), m, false)
	s.Log.Info("quarantine released", "component", "admin", "user", u.Email, "msg", m.ID, "account", m.AccountID)
	return ok(), nil
}

func (s *Server) quarantineDelete(w http.ResponseWriter, r *http.Request, u *store.Account) (any, error) {
	id, err := pathID(r, "id")
	if err != nil {
		return nil, err
	}
	m, err := s.Store.GetMessageAny(r.Context(), id)
	if err != nil {
		return nil, errNotFound
	}
	junk, err := s.Store.MailboxByRole(r.Context(), m.AccountID, "junk")
	if err != nil || m.MailboxID != junk {
		return nil, errNotFound
	}
	// Confirm the classification: train as spam, then remove the message.
	s.trainMessage(r.Context(), m, true)
	if err := s.Store.DeleteMessage(r.Context(), m.AccountID, m.ID); err != nil {
		return nil, err
	}
	s.Log.Info("quarantine confirmed as spam and deleted", "component", "admin", "user", u.Email, "msg", m.ID, "account", m.AccountID)
	return ok(), nil
}

// trainMessage trains the Bayes filter with the message (no-op when Bayes is
// disabled). It is the admin counterpart to the user-side train handler.
func (s *Server) trainMessage(ctx context.Context, m *store.Message, toSpam bool) {
	if !s.Settings.Get().Spam.Bayes.Enabled || s.Spam == nil {
		return
	}
	raw, err := s.Store.Blobs.Get(m.BlobHash)
	if err != nil {
		return
	}
	p, err := message.Parse(raw, false)
	if err != nil {
		return
	}
	s.Spam.Train(ctx, spam.Tokenize(p.Subject+" "+p.Text), toSpam, 1)
}
