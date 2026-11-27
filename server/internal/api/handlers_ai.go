package api

import (
	"errors"
	"net/http"
	"strings"
	"time"

	"postwerk/internal/ai"
	"postwerk/internal/dms"
	"postwerk/internal/message"
	"postwerk/internal/metrics"
	"postwerk/internal/store"
)

func (s *Server) aiStatus(w http.ResponseWriter, r *http.Request, u *store.Account) (any, error) {
	all := s.Settings.Get()
	cfg := all.AI
	p := all.DMS.Paperless
	return map[string]any{"enabled": cfg.Enabled, "features": cfg.Features, "categories": cfg.Categories,
		"dmsEnabled": p.Enabled && p.URL != "" && p.Token != ""}, nil
}

func (s *Server) requireFeature(enabled bool) error {
	if !s.Settings.Get().AI.Enabled || !enabled {
		return ai.ErrDisabled
	}
	return nil
}

func (s *Server) parsed(m *store.Message) (*message.Parsed, error) {
	raw, err := s.Store.Blobs.Get(m.BlobHash)
	if err != nil {
		return nil, err
	}
	return message.Parse(raw, false)
}

func aiErr(feature string, err error) error {
	if err == nil {
		metrics.AIRequests.WithLabelValues(feature, "ok").Inc()
		return nil
	}
	metrics.AIRequests.WithLabelValues(feature, "error").Inc()
	if errors.Is(err, ai.ErrDisabled) {
		return err
	}
	return errf(502, "ai_error", "KI-Endpunkt: "+err.Error())
}

func (s *Server) aiSummarize(w http.ResponseWriter, r *http.Request, u *store.Account) (any, error) {
	if err := s.requireFeature(s.Settings.Get().AI.Features.Summarize); err != nil {
		return nil, err
	}
	var req struct {
		MessageID *int64 `json:"messageId"`
		ThreadID  *int64 `json:"threadId"`
	}
	if err := decode(r, &req); err != nil {
		return nil, err
	}
	var msgs []*store.Message
	switch {
	case req.ThreadID != nil:
		var err error
		if msgs, err = s.Store.ThreadMessages(r.Context(), u.ID, *req.ThreadID); err != nil {
			return nil, err
		}
		if len(msgs) > 15 {
			msgs = msgs[len(msgs)-15:]
		}
	case req.MessageID != nil:
		m, err := s.Store.GetMessage(r.Context(), u.ID, *req.MessageID)
		if err != nil {
			return nil, err
		}
		msgs = []*store.Message{m}
	default:
		return nil, invalid("messageId oder threadId angeben")
	}
	if len(msgs) == 0 {
		return nil, errNotFound
	}
	parsed := make([]*message.Parsed, len(msgs))
	for i, m := range msgs {
		parsed[i], _ = s.parsed(m)
	}
	res, err := s.AI.Summarize(r.Context(), msgs, parsed)
	if err := aiErr("summarize", err); err != nil {
		return nil, err
	}
	return res, nil
}

func (s *Server) aiReply(w http.ResponseWriter, r *http.Request, u *store.Account) (any, error) {
	if err := s.requireFeature(s.Settings.Get().AI.Features.ReplyDraft); err != nil {
		return nil, err
	}
	var req struct {
		MessageID   int64  `json:"messageId"`
		Instruction string `json:"instruction"`
		Tone        string `json:"tone"`
	}
	if err := decode(r, &req); err != nil {
		return nil, err
	}
	m, err := s.Store.GetMessage(r.Context(), u.ID, req.MessageID)
	if err != nil {
		return nil, err
	}
	p, _ := s.parsed(m)
	res, err := s.AI.DraftReply(r.Context(), store.Address{Name: u.Name, Email: u.Email}, m, p, req.Tone, strings.TrimSpace(req.Instruction))
	if err := aiErr("reply", err); err != nil {
		return nil, err
	}
	return res, nil
}

func (s *Server) aiSearch(w http.ResponseWriter, r *http.Request, u *store.Account) (any, error) {
	if err := s.requireFeature(s.Settings.Get().AI.Features.SemanticSearch); err != nil {
		return nil, err
	}
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	if q == "" {
		return map[string]any{"items": []any{}}, nil
	}
	limit := queryInt(r, "limit", 20, 100)
	vec, err := s.AI.Embed(r.Context(), q)
	if err := aiErr("search", err); err != nil {
		return nil, err
	}
	hits, err := s.Store.SimilarMessages(r.Context(), u.ID, vec, limit)
	if err != nil {
		return nil, err
	}
	type scored struct {
		*store.Message
		Score float64 `json:"score"`
	}
	items := []scored{}
	for _, h := range hits {
		if h.Score < 0.2 {
			continue
		}
		m, err := s.Store.GetMessage(r.Context(), u.ID, h.MessageID)
		if err != nil {
			continue
		}
		items = append(items, scored{m, h.Score})
	}
	return map[string]any{"items": items}, nil
}

func (s *Server) aiExtract(w http.ResponseWriter, r *http.Request, u *store.Account) (any, error) {
	if err := s.requireFeature(s.Settings.Get().AI.Features.ExtractEvents); err != nil {
		return nil, err
	}
	var req struct {
		MessageID int64 `json:"messageId"`
	}
	if err := decode(r, &req); err != nil {
		return nil, err
	}
	m, err := s.Store.GetMessage(r.Context(), u.ID, req.MessageID)
	if err != nil {
		return nil, err
	}
	p, _ := s.parsed(m)
	res, err := s.AI.Extract(r.Context(), m, p)
	if err := aiErr("extract", err); err != nil {
		return nil, err
	}
	_ = s.Store.SetAIEvents(r.Context(), m.ID, res.Events, res.Tasks)
	return res, nil
}

func (s *Server) aiReclassify(w http.ResponseWriter, r *http.Request, u *store.Account) (any, error) {
	if err := s.requireFeature(true); err != nil {
		return nil, err
	}
	var req struct {
		MessageID int64 `json:"messageId"`
	}
	if err := decode(r, &req); err != nil {
		return nil, err
	}
	m, err := s.Store.GetMessage(r.Context(), u.ID, req.MessageID)
	if err != nil {
		return nil, err
	}
	p, _ := s.parsed(m)
	start := time.Now()
	a, err := s.AI.Analyze(r.Context(), m, p, s.AIWorker.FolderChoices(r.Context(), m))
	if err := aiErr("classify", err); err != nil {
		return nil, err
	}
	if err := s.AIWorker.Apply(r.Context(), m, a, start); err != nil {
		return nil, err
	}
	return s.Store.GetMessage(r.Context(), u.ID, m.ID)
}

// aiSort analyzes one of the user's inbox messages on request and files it
// into the folder the AI picks, even when automatic filing is switched off.
func (s *Server) aiSort(w http.ResponseWriter, r *http.Request, u *store.Account) (any, error) {
	if err := s.requireFeature(true); err != nil {
		return nil, err
	}
	var req struct {
		MessageID int64 `json:"messageId"`
	}
	if err := decode(r, &req); err != nil {
		return nil, err
	}
	m, err := s.Store.GetMessage(r.Context(), u.ID, req.MessageID)
	if err != nil {
		return nil, err
	}
	p, _ := s.parsed(m)
	res, err := s.AIWorker.Sort(r.Context(), m, p)
	if err := aiErr("sort", err); err != nil {
		return nil, err
	}
	return res, nil
}

// ---- DMS ----

func (s *Server) dmsForward(w http.ResponseWriter, r *http.Request, u *store.Account) (any, error) {
	var req struct {
		MessageID    int64 `json:"messageId"`
		AttachmentID int64 `json:"attachmentId"`
	}
	if err := decode(r, &req); err != nil {
		return nil, err
	}
	m, a, err := s.loadAttachment(r, u, req.MessageID, req.AttachmentID)
	if err != nil {
		return nil, err
	}
	// Infected mail never goes on to the shared document archive, not even
	// on an admin's request.
	if m.Virus != nil && *m.Virus != "" {
		return nil, errf(403, "virus", "Weiterleiten gesperrt: Die Nachricht enthält einen Virus")
	}
	cfg := s.Settings.Get().DMS.Paperless
	if err := dms.Forward(r.Context(), cfg, a.Filename, a.ContentType, a.Data, m.Subject); err != nil {
		if errors.Is(err, dms.ErrDisabled) {
			return nil, errf(503, "dms_disabled", "Paperless ist nicht eingerichtet")
		}
		// Paperless errors can carry its internal address; details go to the log.
		s.Log.Warn("paperless forward failed", "component", "dms", "user", u.Email, "err", err)
		return nil, errf(502, "dms_error", "Paperless hat die Datei nicht angenommen. Details stehen im Server-Log.")
	}
	s.Log.Info("attachment forwarded to paperless", "component", "dms", "user", u.Email, "file", a.Filename)
	return ok(), nil
}

// aiWorkerStatus shows the state of the background analysis: pause, queue,
// failed messages and why analyzed messages stayed in the inbox.
func (s *Server) aiWorkerStatus(w http.ResponseWriter, r *http.Request, u *store.Account) (any, error) {
	cfg := s.Settings.Get().AI
	queue, err := s.Store.AIQueueStats(r.Context())
	if err != nil {
		return nil, err
	}
	failures, err := s.Store.AIFailures(r.Context(), 20)
	if err != nil {
		return nil, err
	}
	out := map[string]any{
		"enabled":  cfg.Enabled,
		"autoFile": cfg.AutoFile.Enabled,
		"mode":     cfg.AutoFile.Mode,
		"queue":    queue,
		"failures": failures,
	}
	if s.AIWorker != nil {
		out["worker"] = s.AIWorker.Status()
	}
	return out, nil
}

// aiRetry makes all failed messages due again and ends a pause.
func (s *Server) aiRetry(w http.ResponseWriter, r *http.Request, u *store.Account) (any, error) {
	n, err := s.Store.ResetAIRetries(r.Context())
	if err != nil {
		return nil, err
	}
	if s.AIWorker != nil {
		s.AIWorker.Resume()
		s.AIWorker.Notify()
	}
	s.Log.Info("ai analysis retry requested", "component", "ai", "by", u.Email, "messages", n)
	return map[string]any{"reset": n}, nil
}
