package api

import (
	"encoding/base64"
	"errors"
	"fmt"
	"mime"
	"net/http"
	"strconv"
	"strings"

	"postwerk/internal/message"
	"postwerk/internal/smtp"
	"postwerk/internal/spam"
	"postwerk/internal/store"
)

// errVirusBlocked is returned when a non-admin tries to download content of
// a message the virus scanner flagged.
var errVirusBlocked = errf(403, "virus", "Download gesperrt: Die Nachricht enthält einen Virus")

func virusBlocked(m *store.Message, u *store.Account) bool {
	return m.Virus != nil && *m.Virus != "" && u.Role != "admin"
}

// mailErr turns delivery errors into API errors with a German message.
func mailErr(err error) error {
	var r *smtp.Reply
	switch {
	case err == nil:
		return nil
	case errors.Is(err, store.ErrQuota):
		return errf(507, "quota", "Speicherplatz voll: Das Postfach hat sein Speicherlimit erreicht")
	case errors.As(err, &r):
		if r.Code == 552 {
			return errf(507, "quota", "Speicherplatz voll: Das Postfach hat sein Speicherlimit erreicht")
		}
		return errf(400, "rejected", "Nachricht abgelehnt: "+r.Text)
	}
	return err
}

// ---- mailboxes ----

func (s *Server) listMailboxes(w http.ResponseWriter, r *http.Request, u *store.Account) (any, error) {
	return s.Store.ListMailboxes(r.Context(), u.ID)
}

func (s *Server) createMailbox(w http.ResponseWriter, r *http.Request, u *store.Account) (any, error) {
	var req struct{ Name string }
	if err := decode(r, &req); err != nil {
		return nil, err
	}
	name := strings.TrimSpace(req.Name)
	if name == "" || len(name) > 100 {
		return nil, invalid("Ungültiger Ordnername")
	}
	return s.Store.CreateMailbox(r.Context(), u.ID, name)
}

func (s *Server) renameMailbox(w http.ResponseWriter, r *http.Request, u *store.Account) (any, error) {
	id, err := pathID(r, "id")
	if err != nil {
		return nil, err
	}
	var req struct{ Name string }
	if err := decode(r, &req); err != nil {
		return nil, err
	}
	name := strings.TrimSpace(req.Name)
	if name == "" {
		return nil, invalid("Ungültiger Ordnername")
	}
	if _, err := s.Store.MailboxOwned(r.Context(), u.ID, id); err != nil {
		return nil, err
	}
	if err := s.Store.RenameMailbox(r.Context(), u.ID, id, name); err != nil {
		return nil, err
	}
	all, err := s.Store.ListMailboxes(r.Context(), u.ID)
	if err != nil {
		return nil, err
	}
	for _, m := range all {
		if m.ID == id {
			return m, nil
		}
	}
	return nil, errNotFound
}

func (s *Server) deleteMailbox(w http.ResponseWriter, r *http.Request, u *store.Account) (any, error) {
	id, err := pathID(r, "id")
	if err != nil {
		return nil, err
	}
	return ok(), s.Store.DeleteMailbox(r.Context(), u.ID, id)
}

// ---- messages ----

func (s *Server) listMessages(w http.ResponseWriter, r *http.Request, u *store.Account) (any, error) {
	q := r.URL.Query()
	mq := store.MessageQuery{
		AccountID: u.ID, Query: q.Get("q"), Tag: q.Get("tag"), Category: q.Get("category"),
		Unread: q.Get("unread") == "true" || q.Get("unread") == "1",
		Limit:  queryInt(r, "limit", 50, 200), Offset: queryInt(r, "offset", 0, 0),
	}
	if mb := q.Get("mailbox"); mb != "" {
		id, err := strconv.ParseInt(mb, 10, 64)
		if err != nil {
			return nil, invalid("Ungültiger Ordner")
		}
		if _, err := s.Store.MailboxOwned(r.Context(), u.ID, id); err != nil {
			return nil, err
		}
		mq.MailboxID = id
	}
	items, total, err := s.Store.ListMessages(r.Context(), mq)
	if err != nil {
		return nil, err
	}
	return map[string]any{"total": total, "items": items}, nil
}

type messageDetail struct {
	*store.Message
	Cc          []store.Address      `json:"cc"`
	ReplyTo     []store.Address      `json:"replyTo"`
	MessageID   string               `json:"messageId"`
	InReplyTo   *string              `json:"inReplyTo"`
	TextBody    string               `json:"textBody"`
	HTMLBody    *string              `json:"htmlBody"`
	Attachments []message.Attachment `json:"attachments"`
	Auth        store.AuthResults    `json:"auth"`
	Virus       *string              `json:"virus"`
	AI          aiDetail             `json:"ai"`
}

type aiDetail struct {
	Summary *string                `json:"summary"`
	Reasons []string               `json:"reasons"`
	Events  []store.ExtractedEvent `json:"events"`
	Tasks   []store.ExtractedTask  `json:"tasks"`
}

func (s *Server) detail(m *store.Message) (*messageDetail, error) {
	raw, err := s.Store.Blobs.Get(m.BlobHash)
	if err != nil {
		return nil, err
	}
	p, err := message.Parse(raw, false)
	if err != nil {
		p = &message.Parsed{Text: string(raw)}
	}
	d := &messageDetail{
		Message: m, Cc: m.Cc, ReplyTo: m.ReplyTo, MessageID: m.MessageID, TextBody: p.Text,
		Attachments: p.Attachments, Auth: m.Auth, Virus: m.Virus,
		AI: aiDetail{Summary: m.Summary, Reasons: m.AIReasons, Events: m.AIEvents, Tasks: m.AITasks},
	}
	if d.Attachments == nil {
		d.Attachments = []message.Attachment{}
	}
	if m.InReplyTo != "" {
		d.InReplyTo = &m.InReplyTo
	}
	if p.HTML != "" {
		d.HTMLBody = &p.HTML
	}
	if d.Auth.SPF == "" {
		d.Auth = store.AuthResults{SPF: "none", DKIM: "none", DMARC: "none"}
	}
	return d, nil
}

func (s *Server) getMessage(w http.ResponseWriter, r *http.Request, u *store.Account) (any, error) {
	id, err := pathID(r, "id")
	if err != nil {
		return nil, err
	}
	m, err := s.Store.GetMessage(r.Context(), u.ID, id)
	if err != nil {
		return nil, err
	}
	return s.detail(m)
}

func (s *Server) rawMessage(w http.ResponseWriter, r *http.Request, u *store.Account) (any, error) {
	id, err := pathID(r, "id")
	if err != nil {
		return nil, err
	}
	m, err := s.Store.GetMessage(r.Context(), u.ID, id)
	if err != nil {
		return nil, err
	}
	if virusBlocked(m, u) {
		return nil, errVirusBlocked
	}
	raw, err := s.Store.Blobs.Get(m.BlobHash)
	if err != nil {
		return nil, err
	}
	w.Header().Set("Content-Type", "message/rfc822")
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="nachricht-%d.eml"`, id))
	w.Write(raw)
	return nil, nil
}

func (s *Server) loadAttachment(r *http.Request, u *store.Account, msgID, attID int64) (*store.Message, *message.Attachment, error) {
	m, err := s.Store.GetMessage(r.Context(), u.ID, msgID)
	if err != nil {
		return nil, nil, err
	}
	raw, err := s.Store.Blobs.Get(m.BlobHash)
	if err != nil {
		return nil, nil, err
	}
	p, err := message.Parse(raw, true)
	if err != nil {
		return nil, nil, err
	}
	for i := range p.Attachments {
		if p.Attachments[i].ID == attID {
			return m, &p.Attachments[i], nil
		}
	}
	return nil, nil, errNotFound
}

func (s *Server) attachment(w http.ResponseWriter, r *http.Request, u *store.Account) (any, error) {
	id, err := pathID(r, "id")
	if err != nil {
		return nil, err
	}
	attID, err := pathID(r, "att")
	if err != nil {
		return nil, err
	}
	m, a, err := s.loadAttachment(r, u, id, attID)
	if err != nil {
		return nil, err
	}
	if virusBlocked(m, u) {
		return nil, errVirusBlocked
	}
	name := message.SanitizeFilename(a.Filename)
	if name == "" {
		name = "anhang"
	}
	disp := mime.FormatMediaType("attachment", map[string]string{"filename": name})
	if disp == "" {
		disp = "attachment"
	}
	// Always download; never render untrusted content inline on our origin.
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Disposition", disp)
	w.Header().Set("Content-Length", strconv.Itoa(len(a.Data)))
	w.Write(a.Data)
	return nil, nil
}

func (s *Server) patchMessage(w http.ResponseWriter, r *http.Request, u *store.Account) (any, error) {
	id, err := pathID(r, "id")
	if err != nil {
		return nil, err
	}
	var req struct {
		Seen      *bool     `json:"seen"`
		Flagged   *bool     `json:"flagged"`
		MailboxID *int64    `json:"mailboxId"`
		Tags      *[]string `json:"tags"`
	}
	if err := decode(r, &req); err != nil {
		return nil, err
	}
	if err := s.Store.UpdateMessage(r.Context(), u.ID, id, store.MessagePatch{Seen: req.Seen, Flagged: req.Flagged, MailboxID: req.MailboxID, Tags: req.Tags}); err != nil {
		return nil, err
	}
	return s.Store.GetMessage(r.Context(), u.ID, id)
}

// trashOrDelete moves a message to the trash, or deletes it if already there.
func (s *Server) trashOrDelete(r *http.Request, u *store.Account, m *store.Message) error {
	trash, err := s.Store.MailboxByRole(r.Context(), u.ID, "trash")
	if err != nil || m.MailboxID == trash {
		return s.Store.DeleteMessage(r.Context(), u.ID, m.ID)
	}
	return s.Store.UpdateMessage(r.Context(), u.ID, m.ID, store.MessagePatch{MailboxID: &trash})
}

func (s *Server) deleteMessage(w http.ResponseWriter, r *http.Request, u *store.Account) (any, error) {
	id, err := pathID(r, "id")
	if err != nil {
		return nil, err
	}
	m, err := s.Store.GetMessage(r.Context(), u.ID, id)
	if err != nil {
		return nil, err
	}
	return ok(), s.trashOrDelete(r, u, m)
}

func (s *Server) train(r *http.Request, m *store.Message, toSpam bool) {
	raw, err := s.Store.Blobs.Get(m.BlobHash)
	if err != nil {
		return
	}
	p, err := message.Parse(raw, false)
	if err != nil {
		return
	}
	tokens := spam.Tokenize(p.Subject + " " + p.Text)
	if s.Settings.Get().Spam.Bayes.Enabled {
		s.Spam.Train(r.Context(), tokens, toSpam, 1)
	}
}

func (s *Server) bulk(w http.ResponseWriter, r *http.Request, u *store.Account) (any, error) {
	var req struct {
		IDs       []int64 `json:"ids"`
		Action    string  `json:"action"`
		MailboxID *int64  `json:"mailboxId"`
	}
	if err := decode(r, &req); err != nil {
		return nil, err
	}
	if len(req.IDs) > 1000 {
		return nil, invalid("Zu viele Nachrichten auf einmal")
	}
	ctx := r.Context()
	t, f := true, false
	count := 0
	for _, id := range req.IDs {
		m, err := s.Store.GetMessage(ctx, u.ID, id)
		if err != nil {
			continue
		}
		var p store.MessagePatch
		switch req.Action {
		case "read":
			p.Seen = &t
		case "unread":
			p.Seen = &f
		case "flag":
			p.Flagged = &t
		case "unflag":
			p.Flagged = &f
		case "move":
			if req.MailboxID == nil {
				return nil, invalid("Zielordner fehlt")
			}
			p.MailboxID = req.MailboxID
		case "delete":
			if err := s.trashOrDelete(r, u, m); err == nil {
				count++
			}
			continue
		case "spam", "notspam":
			role := "junk"
			if req.Action == "notspam" {
				role = "inbox"
			}
			mb, err := s.Store.MailboxByRole(ctx, u.ID, role)
			if err != nil {
				return nil, err
			}
			p.MailboxID = &mb
			s.train(r, m, req.Action == "spam")
		default:
			return nil, invalid("Unbekannte Aktion")
		}
		if err := s.Store.UpdateMessage(ctx, u.ID, id, p); err == nil {
			count++
		}
	}
	return map[string]any{"ok": true, "count": count}, nil
}

func (s *Server) thread(w http.ResponseWriter, r *http.Request, u *store.Account) (any, error) {
	id, err := pathID(r, "id")
	if err != nil {
		return nil, err
	}
	msgs, err := s.Store.ThreadMessages(r.Context(), u.ID, id)
	if err != nil {
		return nil, err
	}
	if len(msgs) == 0 {
		return nil, errNotFound
	}
	out := []*messageDetail{}
	for _, m := range msgs {
		d, err := s.detail(m)
		if err != nil {
			continue
		}
		out = append(out, d)
	}
	return out, nil
}

func (s *Server) tags(w http.ResponseWriter, r *http.Request, u *store.Account) (any, error) {
	return s.Store.TagCounts(r.Context(), u.ID)
}

// ---- compose ----

type composeReq struct {
	To          []string `json:"to"`
	Cc          []string `json:"cc"`
	Bcc         []string `json:"bcc"`
	Subject     string   `json:"subject"`
	Text        string   `json:"text"`
	HTML        string   `json:"html"`
	InReplyTo   *int64   `json:"inReplyTo"`
	DraftID     *int64   `json:"draftId"`
	Attachments []struct {
		Filename    string `json:"filename"`
		ContentType string `json:"contentType"`
		DataBase64  string `json:"dataBase64"`
	} `json:"attachments"`
}

func parseAddrList(list []string) ([]store.Address, error) {
	out := []store.Address{}
	for _, a := range list {
		a = strings.TrimSpace(a)
		if a == "" {
			continue
		}
		name, email := "", a
		if i := strings.LastIndexByte(a, '<'); i >= 0 && strings.HasSuffix(a, ">") {
			name = strings.Trim(strings.TrimSpace(a[:i]), `"`)
			email = a[i+1 : len(a)-1]
		}
		email = store.Normalize(email)
		if !validEmail(email) {
			return nil, invalid("Ungültige Adresse: " + a)
		}
		out = append(out, store.Address{Name: name, Email: email})
	}
	return out, nil
}

func (s *Server) buildDraft(r *http.Request, u *store.Account, req *composeReq) ([]byte, []string, *store.Message, error) {
	to, err := parseAddrList(req.To)
	if err != nil {
		return nil, nil, nil, err
	}
	cc, err := parseAddrList(req.Cc)
	if err != nil {
		return nil, nil, nil, err
	}
	bcc, err := parseAddrList(req.Bcc)
	if err != nil {
		return nil, nil, nil, err
	}
	d := message.Draft{
		From: store.Address{Name: u.Name, Email: u.Email}, To: to, Cc: cc,
		Subject: req.Subject, Text: req.Text, HTML: req.HTML,
		MessageID: message.NewMessageID(store.DomainOf(u.Email)),
	}
	var orig *store.Message
	if req.InReplyTo != nil {
		orig, err = s.Store.GetMessage(r.Context(), u.ID, *req.InReplyTo)
		if err == nil && orig.MessageID != "" {
			d.InReplyTo = orig.MessageID
			if raw, err := s.Store.Blobs.Get(orig.BlobHash); err == nil {
				if p, err := message.Parse(raw, false); err == nil {
					d.References = append(p.References, orig.MessageID)
				}
			}
		}
	}
	for _, a := range req.Attachments {
		data, err := base64.StdEncoding.DecodeString(a.DataBase64)
		if err != nil {
			return nil, nil, nil, invalid("Anhang " + a.Filename + " ist nicht korrekt kodiert")
		}
		// message.Build validates the content type and encodes the name.
		ct := "application/octet-stream"
		if mt, _, err := mime.ParseMediaType(a.ContentType); err == nil && strings.Count(mt, "/") == 1 {
			ct = mt
		}
		d.Attach = append(d.Attach, message.OutAttachment{Filename: message.SanitizeFilename(a.Filename), ContentType: ct, Data: data})
	}
	var rcpts []string
	for _, l := range [][]store.Address{to, cc, bcc} {
		for _, a := range l {
			rcpts = append(rcpts, a.Email)
		}
	}
	return message.Build(d), rcpts, orig, nil
}

func (s *Server) send(w http.ResponseWriter, r *http.Request, u *store.Account) (any, error) {
	var req composeReq
	if err := decode(r, &req); err != nil {
		return nil, err
	}
	raw, rcpts, orig, err := s.buildDraft(r, u, &req)
	if err != nil {
		return nil, err
	}
	if len(rcpts) == 0 {
		return nil, invalid("Mindestens einen Empfänger angeben")
	}
	if int64(len(raw)) > s.Settings.Get().Server.MaxMessageSize {
		return nil, invalid("Nachricht ist zu groß")
	}
	sent, err := s.Delivery.Submit(r.Context(), u, u.Email, rcpts, raw, true)
	if err != nil {
		return nil, mailErr(err)
	}
	if orig != nil {
		t := true
		_ = s.Store.UpdateMessage(r.Context(), u.ID, orig.ID, store.MessagePatch{Answered: &t})
	}
	if req.DraftID != nil {
		if d, err := s.Store.GetMessage(r.Context(), u.ID, *req.DraftID); err == nil {
			_ = s.Store.DeleteMessage(r.Context(), u.ID, d.ID)
		}
	}
	id := int64(0)
	if sent != nil {
		id = sent.ID
	}
	return map[string]any{"ok": true, "id": id}, nil
}

func (s *Server) saveDraft(w http.ResponseWriter, r *http.Request, u *store.Account) (any, error) {
	var req composeReq
	if err := decode(r, &req); err != nil {
		return nil, err
	}
	raw, _, _, err := s.buildDraft(r, u, &req)
	if err != nil {
		return nil, err
	}
	mb, err := s.Store.MailboxByRole(r.Context(), u.ID, "drafts")
	if err != nil {
		return nil, err
	}
	m, err := s.Delivery.StoreMessage(r.Context(), u.ID, mb, raw, nil, nil)
	if err != nil {
		return nil, mailErr(err)
	}
	t := true
	_ = s.Store.UpdateMessage(r.Context(), u.ID, m.ID, store.MessagePatch{Seen: &t})
	if req.DraftID != nil {
		if old, err := s.Store.GetMessage(r.Context(), u.ID, *req.DraftID); err == nil && old.MailboxID == mb {
			_ = s.Store.DeleteMessage(r.Context(), u.ID, old.ID)
		}
	}
	return map[string]any{"id": m.ID}, nil
}
