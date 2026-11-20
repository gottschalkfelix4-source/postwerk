// Package delivery implements the mail flow: inbound policy checks,
// filtering, local delivery, submission, forwarding and auto-replies.
package delivery

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"strings"
	"time"

	"postwerk/internal/ai"
	"postwerk/internal/auth"
	"postwerk/internal/av"
	"postwerk/internal/mailauth"
	"postwerk/internal/message"
	"postwerk/internal/metrics"
	"postwerk/internal/smtp"
	"postwerk/internal/spam"
	"postwerk/internal/store"
)

type Notifier interface{ Notify() }

type Service struct {
	Store    *store.Store
	Settings *store.SettingsCache
	Auth     *auth.Authenticator
	Spam     *spam.Filter
	AI       *ai.Worker
	Queue    Notifier
	Log      *slog.Logger
}

func (s *Service) hostname() string { return s.Settings.Get().Server.Hostname }

// ---- smtp.Backend ----

func (s *Service) Authenticate(ctx context.Context, ip, user, pass string) (*store.Account, error) {
	return s.Auth.Protocol(ctx, ip, user, pass, "smtp")
}

func (s *Service) Banned(ip string) bool { return s.Auth.Guard.Banned(ip) }

func (s *Service) CheckSender(ctx context.Context, env *smtp.Envelope, from string) *smtp.Reply {
	if env.Mode == smtp.ModeSubmission {
		if from == "" || !s.Store.SenderAllowed(ctx, env.Account, from) {
			return smtp.NewReply(553, "5.7.1", "Sender address not owned by authenticated user")
		}
	}
	return nil
}

func (s *Service) CheckRecipient(ctx context.Context, env *smtp.Envelope, rcpt string) *smtp.Reply {
	if env.Mode == smtp.ModeSubmission {
		return nil
	}
	local, external, err := s.Store.ResolveRecipient(ctx, rcpt)
	if err != nil {
		return smtp.NewReply(451, "4.3.0", "Temporary lookup failure")
	}
	if len(local) == 0 && len(external) == 0 {
		return smtp.NewReply(550, "5.1.1", "Mailbox unavailable")
	}
	if len(local) == 0 && len(external) == 1 && external[0] == store.Normalize(rcpt) {
		return smtp.NewReply(554, "5.7.1", "Relay access denied")
	}
	cfg := s.Settings.Get().Spam
	if cfg.Enabled && cfg.Greylisting.Enabled && env.RemoteIP != nil && !env.RemoteIP.IsLoopback() && !env.RemoteIP.IsPrivate() {
		ok, err := s.Store.Greylist(ctx, spam.GreylistKey(env.RemoteIP, env.From, rcpt), time.Duration(cfg.Greylisting.DelaySeconds)*time.Second)
		if err == nil && !ok {
			metrics.Messages.WithLabelValues("inbound", "greylisted").Inc()
			s.Log.Info("greylisted", "ip", env.RemoteIP.String(), "from", env.From, "rcpt", rcpt)
			return smtp.NewReply(451, "4.7.1", "Greylisted, please try again later")
		}
	}
	return nil
}

func (s *Service) Deliver(ctx context.Context, env *smtp.Envelope, data []byte) *smtp.Reply {
	if env.Mode == smtp.ModeSubmission {
		if _, err := s.Submit(ctx, env.Account, env.From, env.Rcpts, data, false); err != nil {
			var r *smtp.Reply
			if errors.As(err, &r) {
				return r
			}
			s.Log.Error("submission failed", "err", err)
			return smtp.NewReply(451, "4.3.0", "Temporary failure")
		}
		return nil
	}
	return s.Inbound(ctx, env, data)
}

// ---- inbound ----

func (s *Service) Inbound(ctx context.Context, env *smtp.Envelope, data []byte) *smtp.Reply {
	cfg := s.Settings.Get()
	log := s.Log.With("session", env.ID, "ip", env.RemoteIP.String(), "from", env.From)
	parsed, err := message.Parse(data, false)
	if err != nil {
		log.Warn("unparseable message", "err", err)
	}
	fromDomain := ""
	if parsed != nil {
		fromDomain = store.DomainOf(parsed.From.Email)
	}
	mailFrom := env.From
	if mailFrom == "<>" {
		mailFrom = ""
	}
	authRes := mailauth.Check(ctx, env.RemoteIP, env.Helo, mailFrom, fromDomain, data)

	var virus string
	if cfg.Antivirus.Enabled {
		v, err := av.Scan(cfg.Antivirus.ClamdAddress, data)
		if err != nil {
			log.Error("virus scan failed, accepting message", "err", err)
		} else if v != "" {
			virus = v
			s.Store.IncStat(ctx, "virus", 1)
			metrics.Messages.WithLabelValues("inbound", "virus").Inc()
			log.Warn("virus found", "virus", v, "action", cfg.Antivirus.Action)
			if cfg.Antivirus.Action != "quarantine" {
				s.Store.IncStat(ctx, "rejected", 1)
				return smtp.NewReply(554, "5.7.1", "Message contains a virus ("+v+")")
			}
		}
	}

	verdict := spam.Verdict{BayesP: -1}
	if cfg.Spam.Enabled {
		verdict = s.Spam.Score(ctx, spam.Input{IP: env.RemoteIP, Helo: env.Helo, Auth: authRes, Parsed: parsed, Raw: data})
		metrics.SpamScore.Observe(verdict.Score)
		if verdict.Reject {
			s.Store.IncStat(ctx, "rejected", 1)
			metrics.Messages.WithLabelValues("inbound", "rejected").Inc()
			log.Warn("rejected as spam", "score", verdict.Score, "rules", strings.Join(verdict.Rules, ", "))
			return smtp.NewReply(550, "5.7.1", fmt.Sprintf("Message rejected as spam (score %.1f)", verdict.Score))
		}
		s.Spam.AutoLearn(ctx, verdict)
	}

	// Trace headers.
	var hdr bytes.Buffer
	tlsInfo := ""
	if env.TLS {
		tlsInfo = " (TLS)"
	}
	fmt.Fprintf(&hdr, "Received: from %s (%s)\r\n\tby %s (Postwerk) with ESMTP%s id %s;\r\n\t%s\r\n",
		env.Helo, env.RemoteIP, s.hostname(), tlsInfo, env.ID, time.Now().Format(time.RFC1123Z))
	hdr.WriteString(authRes.Header(s.hostname()) + "\r\n")
	if cfg.Spam.Enabled {
		fmt.Fprintf(&hdr, "X-Spam-Score: %.1f\r\nX-Spam-Status: %s\r\n", verdict.Score, yesNo(verdict.IsSpam))
		if len(verdict.Rules) > 0 {
			fmt.Fprintf(&hdr, "X-Spam-Rules: %s\r\n", strings.Join(verdict.Rules, ", "))
		}
	}
	if virus != "" {
		fmt.Fprintf(&hdr, "X-Virus: %s\r\n", virus)
	}
	full := append(hdr.Bytes(), data...)

	delivered := 0
	var forwards []string
	seen := map[string]bool{}
	for _, rcpt := range env.Rcpts {
		local, external, err := s.Store.ResolveRecipient(ctx, rcpt)
		if err != nil {
			return smtp.NewReply(451, "4.3.0", "Temporary lookup failure")
		}
		for _, l := range local {
			if seen[l] {
				continue
			}
			seen[l] = true
			junk := verdict.IsSpam || virus != ""
			meta := deliveryMeta{Auth: authRes, SpamScore: verdict.Score, Junk: junk, Virus: virus, Tokens: verdict.Tokens}
			if err := s.deliverLocal(ctx, l, full, parsed, meta, env.From); err != nil {
				var r *smtp.Reply
				if errors.As(err, &r) && len(env.Rcpts) == 1 {
					return r
				}
				log.Error("local delivery failed", "rcpt", l, "err", err)
				continue
			}
			delivered++
		}
		for _, e := range external {
			if !seen[e] {
				seen[e] = true
				forwards = append(forwards, e)
			}
		}
	}
	if len(forwards) > 0 && !verdict.IsSpam && virus == "" {
		// Forwarding keeps the original envelope sender. SRS follows in a later release.
		if err := s.enqueue(ctx, env.From, forwards, full); err != nil {
			log.Error("forward enqueue failed", "err", err)
		}
	}
	if delivered == 0 && len(forwards) == 0 {
		return smtp.NewReply(451, "4.3.0", "Delivery failed, try again later")
	}
	s.Store.IncStat(ctx, "received", 1)
	if verdict.IsSpam {
		s.Store.IncStat(ctx, "spam", 1)
		metrics.Messages.WithLabelValues("inbound", "junk").Inc()
	} else {
		metrics.Messages.WithLabelValues("inbound", "delivered").Inc()
	}
	log.Info("message accepted", "rcpts", strings.Join(env.Rcpts, ","), "size", len(full), "spam", verdict.Score,
		"spf", authRes.SPF, "dkim", authRes.DKIM, "dmarc", authRes.DMARC)
	if s.AI != nil {
		s.AI.Notify()
	}
	return nil
}

func yesNo(b bool) string {
	if b {
		return "Yes"
	}
	return "No"
}

type deliveryMeta struct {
	Auth      mailauth.Result
	SpamScore float64
	Junk      bool
	Virus     string
	Tokens    []string
	Mailbox   string // role override, e.g. "sent"
	Seen      bool
}

// deliverLocal stores raw for the account with the given address.
func (s *Service) deliverLocal(ctx context.Context, email string, raw []byte, parsed *message.Parsed, meta deliveryMeta, envFrom string) error {
	acct, err := s.Store.GetAccountByEmail(ctx, email)
	if err != nil {
		return err
	}
	if acct.QuotaBytes > 0 && s.Store.UsedBytes(ctx, acct.ID)+int64(len(raw)) > acct.QuotaBytes {
		return smtp.NewReply(552, "5.2.2", "Mailbox full")
	}
	role := "inbox"
	if meta.Junk {
		role = "junk"
	}
	if meta.Mailbox != "" {
		role = meta.Mailbox
	}
	mbID, err := s.Store.MailboxByRole(ctx, acct.ID, role)
	if err != nil {
		return err
	}
	m, err := s.StoreMessage(ctx, acct.ID, mbID, raw, parsed, &meta)
	if err != nil {
		return err
	}
	_ = m
	if role == "inbox" && envFrom != "" && envFrom != "<>" {
		s.maybeVacation(ctx, acct, envFrom, parsed)
	}
	return nil
}

// StoreMessage writes the blob and metadata into a mailbox.
func (s *Service) StoreMessage(ctx context.Context, accountID, mailboxID int64, raw []byte, parsed *message.Parsed, meta *deliveryMeta) (*store.Message, error) {
	if parsed == nil {
		var err error
		if parsed, err = message.Parse(raw, false); err != nil {
			parsed = &message.Parsed{}
		}
	}
	hash, err := s.Store.Blobs.Put(raw)
	if err != nil {
		return nil, err
	}
	auth := store.AuthResults{SPF: "none", DKIM: "none", DMARC: "none"}
	score := 0.0
	var virus *string
	if meta != nil {
		if meta.Auth.SPF != "" {
			auth = store.AuthResults{SPF: meta.Auth.SPF, DKIM: meta.Auth.DKIM, DMARC: meta.Auth.DMARC}
		}
		score = meta.SpamScore
		if meta.Virus != "" {
			virus = &meta.Virus
		}
	}
	received := time.Now().UTC()
	m := &store.Message{
		AccountID: accountID, MailboxID: mailboxID,
		MessageID: parsed.MessageID, InReplyTo: parsed.InReplyTo, References: parsed.References,
		From: parsed.From, To: parsed.To, Cc: parsed.Cc, ReplyTo: parsed.ReplyTo,
		Subject: parsed.Subject, Preview: message.Preview(parsed.Text, 200),
		ReceivedAt: received, Size: int64(len(raw)), HasAttachments: len(parsed.Attachments) > 0,
		SpamScore: score, Auth: auth, Virus: virus, BlobHash: hash,
		SearchText: parsed.Subject + " " + parsed.From.Name + " " + parsed.From.Email + " " + addrText(parsed.To) + " " + message.Preview(parsed.Text, 20000),
	}
	if err := s.Store.InsertMessage(ctx, m); err != nil {
		return nil, err
	}
	return m, nil
}

func addrText(l []store.Address) string {
	var b strings.Builder
	for _, a := range l {
		b.WriteString(a.Name + " " + a.Email + " ")
	}
	return b.String()
}

// ---- outbound ----

// Submit sends a message for an authenticated account: it ensures Date and
// Message-ID, DKIM-signs, delivers local recipients directly and queues the
// rest. With saveSent a copy goes into the account's Sent folder.
func (s *Service) Submit(ctx context.Context, acct *store.Account, from string, rcpts []string, data []byte, saveSent bool) (*store.Message, error) {
	if message.HeaderValue(data, "Date") == "" {
		data = append([]byte("Date: "+time.Now().Format(time.RFC1123Z)+"\r\n"), data...)
	}
	if message.HeaderValue(data, "Message-ID") == "" {
		data = append([]byte("Message-ID: <"+message.NewMessageID(store.DomainOf(from))+">\r\n"), data...)
	}
	// Header From must belong to the account as well.
	if p, err := message.Parse(data, false); err == nil && p.From.Email != "" && !s.Store.SenderAllowed(ctx, acct, p.From.Email) {
		return nil, smtp.NewReply(553, "5.7.1", "From header not owned by authenticated user")
	}
	signed := s.sign(ctx, from, data)

	var sent *store.Message
	if saveSent {
		if mb, err := s.Store.MailboxByRole(ctx, acct.ID, "sent"); err == nil {
			m, err := s.StoreMessage(ctx, acct.ID, mb, signed, nil, nil)
			if err != nil {
				return nil, err
			}
			_ = s.Store.UpdateMessage(ctx, acct.ID, m.ID, store.MessagePatch{Seen: ptr(true)})
			sent = m
		}
	}

	var remote []string
	for _, r := range rcpts {
		local, external, err := s.Store.ResolveRecipient(ctx, r)
		if err != nil {
			return nil, err
		}
		for _, l := range local {
			if err := s.deliverLocal(ctx, l, signed, nil, deliveryMeta{}, from); err != nil {
				s.Log.Warn("local delivery failed", "rcpt", l, "err", err)
			}
		}
		remote = append(remote, external...)
	}
	if len(remote) > 0 {
		if err := s.enqueue(ctx, from, remote, signed); err != nil {
			return nil, err
		}
	}
	s.Store.IncStat(ctx, "sent", 1)
	metrics.Messages.WithLabelValues("outbound", "accepted").Inc()
	s.Log.Info("message submitted", "user", acct.Email, "from", from, "rcpts", strings.Join(rcpts, ","), "size", len(signed))
	if s.AI != nil {
		s.AI.Notify()
	}
	return sent, nil
}

func ptr[T any](v T) *T { return &v }

func (s *Service) sign(ctx context.Context, from string, data []byte) []byte {
	d, err := s.Store.GetDomain(ctx, store.DomainOf(from))
	if err != nil {
		return data
	}
	signed, err := mailauth.Sign(data, d.Name, d.DKIMSelector, d.DKIMPrivateKey)
	if err != nil {
		s.Log.Error("dkim signing failed", "domain", d.Name, "err", err)
		return data
	}
	return signed
}

func (s *Service) enqueue(ctx context.Context, from string, rcpts []string, data []byte) error {
	hash, err := s.Store.Blobs.Put(data)
	if err != nil {
		return err
	}
	if _, err := s.Store.Enqueue(ctx, from, rcpts, hash, int64(len(data))); err != nil {
		return err
	}
	if s.Queue != nil {
		s.Queue.Notify()
	}
	return nil
}

// Bounce sends a delivery status notification back to the original sender.
func (s *Service) Bounce(ctx context.Context, origFrom string, failed []string, reason string, orig []byte) {
	if origFrom == "" || origFrom == "<>" {
		return
	}
	host := s.hostname()
	headers := orig
	if i := bytes.Index(orig, []byte("\r\n\r\n")); i >= 0 {
		headers = orig[:i]
	}
	text := fmt.Sprintf("Dies ist eine automatische Nachricht von %s.\n\nIhre Nachricht konnte an folgende Empfänger nicht zugestellt werden:\n\n  %s\n\nGrund: %s\n\n--- Kopfzeilen der ursprünglichen Nachricht ---\n\n%s\n",
		host, strings.Join(failed, "\n  "), reason, string(headers))
	raw := message.Build(message.Draft{
		From:      store.Address{Name: "Mail Delivery System", Email: "mailer-daemon@" + host},
		To:        []store.Address{{Email: origFrom}},
		Subject:   "Unzustellbar: Nachricht konnte nicht zugestellt werden",
		Text:      text,
		MessageID: message.NewMessageID(host),
		Extra:     map[string]string{"Auto-Submitted": "auto-replied"},
	})
	local, external, err := s.Store.ResolveRecipient(ctx, origFrom)
	if err != nil {
		return
	}
	for _, l := range local {
		_ = s.deliverLocal(ctx, l, raw, nil, deliveryMeta{}, "")
	}
	if len(external) > 0 {
		_ = s.enqueue(ctx, "", external, raw)
	}
	metrics.Messages.WithLabelValues("outbound", "bounced").Inc()
}

func (s *Service) maybeVacation(ctx context.Context, acct *store.Account, sender string, parsed *message.Parsed) {
	v, err := s.Store.GetVacation(ctx, acct.ID)
	if err != nil || !v.Enabled {
		return
	}
	today := time.Now().Format("2006-01-02")
	if v.From != nil && *v.From != "" && today < (*v.From)[:min(10, len(*v.From))] {
		return
	}
	if v.Until != nil && *v.Until != "" && today > (*v.Until)[:min(10, len(*v.Until))] {
		return
	}
	if parsed != nil && parsed.Header.Get("Auto-Submitted") != "" && !strings.EqualFold(parsed.Header.Get("Auto-Submitted"), "no") {
		return
	}
	if parsed != nil {
		prec := strings.ToLower(parsed.Header.Get("Precedence"))
		if prec == "bulk" || prec == "list" || prec == "junk" || parsed.Header.Get("List-Id") != "" || parsed.Header.Get("List-Unsubscribe") != "" {
			return
		}
	}
	lower := strings.ToLower(sender)
	if strings.HasPrefix(lower, "mailer-daemon@") || strings.HasPrefix(lower, "noreply") || strings.HasPrefix(lower, "no-reply") || lower == acct.Email {
		return
	}
	if !s.Store.VacationShouldReply(ctx, acct.ID, lower) {
		return
	}
	subject := v.Subject
	if subject == "" && parsed != nil {
		subject = "Abwesend: " + parsed.Subject
	}
	draft := message.Draft{
		From:      store.Address{Name: acct.Name, Email: acct.Email},
		To:        []store.Address{{Email: sender}},
		Subject:   subject,
		Text:      v.Body,
		MessageID: message.NewMessageID(store.DomainOf(acct.Email)),
		Extra:     map[string]string{"Auto-Submitted": "auto-replied", "Precedence": "bulk"},
	}
	if parsed != nil && parsed.MessageID != "" {
		draft.InReplyTo = parsed.MessageID
	}
	raw := s.sign(ctx, acct.Email, message.Build(draft))
	local, external, err := s.Store.ResolveRecipient(ctx, sender)
	if err != nil {
		return
	}
	for _, l := range local {
		_ = s.deliverLocal(ctx, l, raw, nil, deliveryMeta{}, "")
	}
	if len(external) > 0 {
		_ = s.enqueue(ctx, "", external, raw)
	}
	s.Log.Info("vacation reply sent", "user", acct.Email, "to", sender)
}

// ClientIP extracts the IP of a remote address string.
func ClientIP(addr string) net.IP {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		host = addr
	}
	return net.ParseIP(host)
}

// ImportOptions controls how an imported message is stored.
type ImportOptions struct {
	ReceivedAt time.Time
	Seen       bool
	Flagged    bool
	Answered   bool
	// SkipAI marks the message as already analyzed so the AI worker ignores it.
	SkipAI bool
}

// ImportMessage stores a message from a migration or file import without
// running spam filtering. It respects the account quota.
func (s *Service) ImportMessage(ctx context.Context, acct *store.Account, mailboxID int64, raw []byte, opt ImportOptions) (*store.Message, error) {
	if acct.QuotaBytes > 0 && s.Store.UsedBytes(ctx, acct.ID)+int64(len(raw)) > acct.QuotaBytes {
		return nil, fmt.Errorf("Quota von %s überschritten", acct.Email)
	}
	parsed, err := message.Parse(raw, false)
	if err != nil {
		parsed = &message.Parsed{}
	}
	hash, err := s.Store.Blobs.Put(raw)
	if err != nil {
		return nil, err
	}
	received := opt.ReceivedAt
	if received.IsZero() {
		received = parsed.Date
	}
	if received.IsZero() {
		received = time.Now()
	}
	m := &store.Message{
		AccountID: acct.ID, MailboxID: mailboxID,
		MessageID: parsed.MessageID, InReplyTo: parsed.InReplyTo, References: parsed.References,
		From: parsed.From, To: parsed.To, Cc: parsed.Cc, ReplyTo: parsed.ReplyTo,
		Subject: parsed.Subject, Preview: message.Preview(parsed.Text, 200),
		ReceivedAt: received.UTC(), Size: int64(len(raw)), HasAttachments: len(parsed.Attachments) > 0,
		Seen: opt.Seen, Flagged: opt.Flagged, Answered: opt.Answered, AIDone: opt.SkipAI,
		Auth:       store.AuthResults{SPF: "none", DKIM: "none", DMARC: "none"},
		BlobHash:   hash,
		SearchText: parsed.Subject + " " + parsed.From.Name + " " + parsed.From.Email + " " + addrText(parsed.To) + " " + message.Preview(parsed.Text, 20000),
	}
	if err := s.Store.InsertMessage(ctx, m); err != nil {
		return nil, err
	}
	return m, nil
}

// ImportKey returns the duplicate-detection keys for raw message bytes.
func ImportKey(raw []byte) (idKey, hashKey string) {
	sum := sha256.Sum256(raw)
	hashKey = "h:" + hex.EncodeToString(sum[:])
	if id := strings.Trim(message.HeaderValue(raw, "Message-ID"), "<> \t"); id != "" {
		idKey = "id:" + id
	}
	return
}
