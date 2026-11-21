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
	"sync"
	"time"

	"postwerk/internal/ai"
	"postwerk/internal/auth"
	"postwerk/internal/av"
	"postwerk/internal/mailauth"
	"postwerk/internal/message"
	"postwerk/internal/metrics"
	"postwerk/internal/smtp"
	"postwerk/internal/spam"
	"postwerk/internal/srs"
	"postwerk/internal/store"
)

// MaxHops is the maximum number of Received headers an inbound message may
// carry before it is treated as a mail loop.
const MaxHops = 30

type Notifier interface{ Notify() }

type Service struct {
	Store    *store.Store
	Settings *store.SettingsCache
	Auth     *auth.Authenticator
	Spam     *spam.Filter
	AI       *ai.Worker
	Queue    Notifier
	Log      *slog.Logger

	// AuthCheck replaces the SPF/DKIM/DMARC evaluation (nil: mailauth.Check).
	AuthCheck func(ctx context.Context, ip net.IP, helo, mailFrom, fromDomain string, raw []byte) mailauth.Result
	// VirusScan replaces the ClamAV client (nil: av.Scan).
	VirusScan func(addr string, data []byte) (string, error)

	srsMu sync.Mutex
	srs   *srs.Rewriter
}

func (s *Service) hostname() string { return s.Settings.Get().Server.Hostname }

func (s *Service) checkAuth(ctx context.Context, ip net.IP, helo, mailFrom, fromDomain string, raw []byte) mailauth.Result {
	if s.AuthCheck != nil {
		return s.AuthCheck(ctx, ip, helo, mailFrom, fromDomain, raw)
	}
	return mailauth.Check(ctx, ip, helo, mailFrom, fromDomain, raw)
}

func (s *Service) scan(addr string, data []byte) (string, error) {
	if s.VirusScan != nil {
		return s.VirusScan(addr, data)
	}
	return av.Scan(addr, data)
}

// rewriter returns the SRS rewriter, loading the persisted secret once.
func (s *Service) rewriter(ctx context.Context) (*srs.Rewriter, error) {
	s.srsMu.Lock()
	defer s.srsMu.Unlock()
	if s.srs != nil {
		return s.srs, nil
	}
	secret, err := s.Store.SRSSecret(ctx)
	if err != nil {
		return nil, err
	}
	s.srs = &srs.Rewriter{Secret: secret}
	return s.srs, nil
}

func (s *Service) isLocalDomain(ctx context.Context, domain string) bool {
	if domain == "" {
		return false
	}
	ok, err := s.Store.DomainExists(ctx, domain)
	return err == nil && ok
}

// localSRS reports whether addr is an SRS address of one of our domains.
func (s *Service) localSRS(ctx context.Context, addr string) bool {
	return srs.IsSRS(addr) && s.isLocalDomain(ctx, store.DomainOf(addr))
}

// reverseSRS validates an SRS address of ours and returns the address the
// message has to go back to.
func (s *Service) reverseSRS(ctx context.Context, addr string) (string, error) {
	rw, err := s.rewriter(ctx)
	if err != nil {
		return "", err
	}
	return rw.Reverse(addr)
}

// forwardSender returns the envelope sender for forwarding a message from
// envFrom through domain: an SRS address unless the sender is null or one of
// our own domains.
func (s *Service) forwardSender(ctx context.Context, envFrom, domain string) string {
	if envFrom == "" || envFrom == "<>" {
		return ""
	}
	if s.isLocalDomain(ctx, store.DomainOf(envFrom)) && !srs.IsSRS(envFrom) {
		return envFrom
	}
	rw, err := s.rewriter(ctx)
	if err == nil {
		var addr string
		if addr, err = rw.Forward(envFrom, domain); err == nil {
			return addr
		}
	}
	s.Log.Error("srs rewrite failed, forwarding with original sender", "from", envFrom, "err", err)
	return envFrom
}

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
	if s.localSRS(ctx, rcpt) {
		// Bounce to a message we forwarded: only valid, unexpired SRS
		// addresses signed with our secret are accepted.
		if _, err := s.reverseSRS(ctx, rcpt); err != nil {
			s.Log.Info("invalid srs recipient", "rcpt", rcpt, "err", err)
			return smtp.NewReply(550, "5.1.1", "Invalid or expired SRS address")
		}
	} else {
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
		if len(external) == 0 && s.allFull(ctx, local) {
			return smtp.NewReply(552, "5.2.2", "Mailbox full")
		}
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

// allFull reports whether every listed account has already used up its quota.
func (s *Service) allFull(ctx context.Context, emails []string) bool {
	if len(emails) == 0 {
		return false
	}
	for _, e := range emails {
		a, err := s.Store.GetAccountByEmail(ctx, e)
		if err != nil || a.QuotaBytes <= 0 || s.Store.UsedBytes(ctx, a.ID) < a.QuotaBytes {
			return false
		}
	}
	return true
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

// headerFromDomain returns the domain of the header From for DMARC. A
// missing, duplicated or unparseable From header, or one with addresses of
// different domains, cannot be evaluated and is rejected (RFC 7489 6.6.1).
func headerFromDomain(parsed *message.Parsed) (string, *smtp.Reply) {
	if parsed == nil || parsed.FromErr != nil || len(parsed.FromAll) == 0 {
		return "", smtp.NewReply(550, "5.7.1", "Message rejected: missing or malformed From header")
	}
	domain := store.DomainOf(parsed.FromAll[0].Email)
	for _, a := range parsed.FromAll[1:] {
		if store.DomainOf(a.Email) != domain {
			return "", smtp.NewReply(550, "5.7.1", "Message rejected: From header with addresses of different domains")
		}
	}
	if domain == "" {
		return "", smtp.NewReply(550, "5.7.1", "Message rejected: missing or malformed From header")
	}
	return domain, nil
}

// enforceSenderPolicy applies the local anti-spoofing rule and the DMARC
// reject policy. It runs independently of the spam filter.
func (s *Service) enforceSenderPolicy(ctx context.Context, mailFrom, fromDomain string, res mailauth.Result) *smtp.Reply {
	// Mail claiming one of our own domains must be authenticated for it:
	// our users submit through port 587, never unauthenticated on port 25.
	check := func(domain string) *smtp.Reply {
		if !s.isLocalDomain(ctx, domain) || res.AuthenticatedFor(domain) {
			return nil
		}
		if res.SPF == "temperror" {
			return smtp.NewReply(451, "4.7.1", "Temporary authentication failure, try again later")
		}
		return smtp.NewReply(550, "5.7.1", "Spoofed local sender")
	}
	if r := check(fromDomain); r != nil {
		return r
	}
	if mailFrom != "" {
		if r := check(store.DomainOf(mailFrom)); r != nil {
			return r
		}
	}
	if res.DMARC == "fail" && res.DMARCPolicy == "reject" {
		return smtp.NewReply(550, "5.7.1", "Message rejected due to DMARC policy of "+fromDomain)
	}
	return nil
}

// stripTrustHeaders removes headers from an incoming message that would
// otherwise look as if this server had written them.
func stripTrustHeaders(data []byte, host string) []byte {
	return message.StripHeaders(data, func(name, value string) bool {
		n := strings.ToLower(name)
		switch {
		case n == "x-spam" || strings.HasPrefix(n, "x-spam-") || strings.HasPrefix(n, "x-postwerk-") || n == "x-virus":
			return true
		case n == "authentication-results":
			id := value
			if i := strings.IndexByte(id, ';'); i >= 0 {
				id = id[:i]
			}
			if f := strings.Fields(id); len(f) > 0 {
				id = f[0]
			}
			return host != "" && strings.EqualFold(strings.TrimSuffix(id, "."), strings.TrimSuffix(host, "."))
		}
		return false
	})
}

func (s *Service) reject(ctx context.Context, log *slog.Logger, r *smtp.Reply, msg string, args ...any) *smtp.Reply {
	s.Store.IncStat(ctx, "rejected", 1)
	metrics.Messages.WithLabelValues("inbound", "rejected").Inc()
	log.Warn(msg, append(args, "reply", r.Error())...)
	return r
}

type failedRcpt struct {
	rcpt  string
	reply *smtp.Reply // nil for internal errors
}

func (s *Service) Inbound(ctx context.Context, env *smtp.Envelope, data []byte) *smtp.Reply {
	cfg := s.Settings.Get()
	log := s.Log.With("session", env.ID, "ip", env.RemoteIP.String(), "from", env.From)
	if n := message.CountHeader(data, "Received"); n > MaxHops {
		return s.reject(ctx, log, smtp.NewReply(554, "5.4.6", "Too many hops"), "mail loop suspected", "received", n)
	}
	parsed, err := message.Parse(data, false)
	if err != nil {
		log.Warn("unparseable message", "err", err)
	}
	fromDomain, rr := headerFromDomain(parsed)
	if rr != nil {
		return s.reject(ctx, log, rr, "invalid from header")
	}
	mailFrom := env.From
	if mailFrom == "<>" {
		mailFrom = ""
	}
	authRes := s.checkAuth(ctx, env.RemoteIP, env.Helo, mailFrom, fromDomain, data)
	if r := s.enforceSenderPolicy(ctx, mailFrom, fromDomain, authRes); r != nil {
		if r.Code >= 500 {
			return s.reject(ctx, log, r, "sender policy", "header_from", fromDomain, "spf", authRes.SPF, "dkim", authRes.DKIM, "dmarc", authRes.DMARC)
		}
		log.Warn("sender policy deferred", "reply", r.Error())
		return r
	}
	// DMARC p=quarantine (after pct/sp) goes to the junk folder.
	quarantine := authRes.DMARC == "fail" && authRes.DMARCPolicy == "quarantine"

	var virus string
	if cfg.Antivirus.Enabled {
		v, err := s.scan(cfg.Antivirus.ClamdAddress, data)
		if err != nil {
			if errors.Is(err, av.ErrSizeLimit) {
				log.Error("virus scan failed: message exceeds the clamd stream limit, raise StreamMaxLength (and MaxScanSize/MaxFileSize) in clamd.conf to at least the maximum message size; deferring message", "size", len(data))
			} else {
				log.Error("virus scan failed, deferring message (if this only happens for large messages, check StreamMaxLength in clamd.conf)", "err", err, "size", len(data))
			}
			return smtp.NewReply(451, "4.7.1", "Virus scanner temporarily unavailable, try again later")
		}
		if v != "" {
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
			return s.reject(ctx, log, smtp.NewReply(550, "5.7.1", fmt.Sprintf("Message rejected as spam (score %.1f)", verdict.Score)),
				"rejected as spam", "score", verdict.Score, "rules", strings.Join(verdict.Rules, ", "))
		}
		s.Spam.AutoLearn(ctx, verdict)
	}
	junk := verdict.IsSpam || quarantine

	// Resolve all recipients first so the forwarding decision can take
	// local deliveries into account.
	var locals []string
	forwards := map[string][]string{} // forwarding domain -> targets
	var forwardCount int
	var srsTargets []string
	seen := map[string]bool{}
	for _, rcpt := range env.Rcpts {
		if s.localSRS(ctx, rcpt) {
			orig, err := s.reverseSRS(ctx, rcpt)
			if err != nil {
				log.Warn("invalid srs recipient", "rcpt", rcpt, "err", err)
				continue
			}
			if !seen["srs:"+orig] {
				seen["srs:"+orig] = true
				srsTargets = append(srsTargets, orig)
			}
			continue
		}
		local, external, err := s.Store.ResolveRecipient(ctx, rcpt)
		if err != nil {
			return smtp.NewReply(451, "4.3.0", "Temporary lookup failure")
		}
		for _, l := range local {
			if !seen[l] {
				seen[l] = true
				locals = append(locals, l)
			}
		}
		dom := store.DomainOf(rcpt)
		for _, e := range external {
			if !seen[e] {
				seen[e] = true
				forwards[dom] = append(forwards[dom], e)
				forwardCount++
			}
		}
	}
	onlyForward := len(locals) == 0 && len(srsTargets) == 0
	if forwardCount > 0 && virus != "" {
		if onlyForward {
			s.Store.IncStat(ctx, "rejected", 1)
			return smtp.NewReply(554, "5.7.1", "Message contains a virus ("+virus+")")
		}
		log.Warn("virus not forwarded to external targets, local copies quarantined", "virus", virus, "targets", forwardCount)
		forwards = nil
	}

	// Trace headers. Headers that claim to come from us are removed first.
	data = stripTrustHeaders(data, s.hostname())
	var hdr bytes.Buffer
	tlsInfo := ""
	if env.TLS {
		tlsInfo = " (TLS)"
	}
	fmt.Fprintf(&hdr, "Received: from %s (%s)\r\n\tby %s (Postwerk) with ESMTP%s id %s;\r\n\t%s\r\n",
		env.Helo, env.RemoteIP, s.hostname(), tlsInfo, env.ID, time.Now().Format(time.RFC1123Z))
	hdr.WriteString(authRes.Header(s.hostname()) + "\r\n")
	if cfg.Spam.Enabled {
		fmt.Fprintf(&hdr, "X-Spam-Score: %.1f\r\nX-Spam-Status: %s\r\n", verdict.Score, yesNo(junk))
		if len(verdict.Rules) > 0 {
			fmt.Fprintf(&hdr, "X-Spam-Rules: %s\r\n", strings.Join(verdict.Rules, ", "))
		}
	}
	if junk {
		hdr.WriteString("X-Spam-Flag: YES\r\n")
	}
	if quarantine {
		hdr.WriteString("X-Postwerk-DMARC: quarantine\r\n")
	}
	if virus != "" {
		fmt.Fprintf(&hdr, "X-Virus: %s\r\n", virus)
	}
	full := append(hdr.Bytes(), data...)

	delivered := 0
	var failed []failedRcpt
	for _, l := range locals {
		meta := deliveryMeta{Auth: authRes, SpamScore: verdict.Score, Junk: junk || virus != "", Virus: virus, Tokens: verdict.Tokens}
		if err := s.deliverLocal(ctx, l, full, parsed, meta, env.From); err != nil {
			var r *smtp.Reply
			errors.As(err, &r)
			log.Error("local delivery failed", "rcpt", l, "err", err)
			failed = append(failed, failedRcpt{l, r})
			continue
		}
		delivered++
	}
	// Bounces to SRS addresses go back to the original sender, always with
	// a null envelope sender so they cannot loop.
	for _, orig := range srsTargets {
		if err := s.deliverTo(ctx, orig, full, deliveryMeta{Auth: authRes, SpamScore: verdict.Score, Junk: junk}); err != nil {
			log.Error("srs reverse forward failed", "to", orig, "err", err)
			continue
		}
		delivered++
	}
	forwarded := 0
	for dom, targets := range forwards {
		sender := s.forwardSender(ctx, env.From, dom)
		if err := s.enqueue(ctx, sender, targets, full); err != nil {
			log.Error("forward enqueue failed", "err", err)
			continue
		}
		forwarded += len(targets)
	}

	if delivered == 0 && forwarded == 0 {
		// Nothing was accepted: a permanent per-recipient failure (mailbox
		// full) can be reported directly, everything else is retried.
		if len(failed) > 0 && failed[0].reply != nil && failed[0].reply.Code >= 500 && len(failed) == len(locals) && len(srsTargets) == 0 && forwardCount == 0 {
			return failed[0].reply
		}
		return smtp.NewReply(451, "4.3.0", "Delivery failed, try again later")
	}
	// Partial success: the sender must learn about the recipients that
	// could not be served instead of losing the message silently.
	if len(failed) > 0 {
		s.bounceFailed(ctx, env.From, failed, full)
	}
	s.Store.IncStat(ctx, "received", 1)
	if junk {
		s.Store.IncStat(ctx, "spam", 1)
		metrics.Messages.WithLabelValues("inbound", "junk").Inc()
	} else {
		metrics.Messages.WithLabelValues("inbound", "delivered").Inc()
	}
	log.Info("message accepted", "rcpts", strings.Join(env.Rcpts, ","), "size", len(full), "spam", verdict.Score,
		"spf", authRes.SPF, "dkim", authRes.DKIM, "dmarc", authRes.DMARC, "forwarded", forwarded)
	if s.AI != nil {
		s.AI.Notify()
	}
	return nil
}

// bounceFailed sends one DSN per failure reason.
func (s *Service) bounceFailed(ctx context.Context, envFrom string, failed []failedRcpt, raw []byte) {
	byReason := map[string][]string{}
	var order []string
	for _, f := range failed {
		reason := failureReason(f.reply)
		if _, ok := byReason[reason]; !ok {
			order = append(order, reason)
		}
		byReason[reason] = append(byReason[reason], f.rcpt)
	}
	for _, reason := range order {
		s.Bounce(ctx, envFrom, byReason[reason], reason, raw)
	}
}

func failureReason(r *smtp.Reply) string {
	switch {
	case r == nil:
		return "Interner Fehler bei der Zustellung"
	case r.Code == 552:
		return "Das Postfach des Empfängers ist voll (" + r.Error() + ")"
	default:
		return r.Error()
	}
}

// deliverTo delivers raw to an address that may be local or remote, using a
// null envelope sender for remote targets.
func (s *Service) deliverTo(ctx context.Context, addr string, raw []byte, meta deliveryMeta) error {
	local, external, err := s.Store.ResolveRecipient(ctx, addr)
	if err != nil {
		return err
	}
	var firstErr error
	for _, l := range local {
		if err := s.deliverLocal(ctx, l, raw, nil, meta, ""); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	if len(external) > 0 {
		if err := s.enqueue(ctx, "", external, raw); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	if len(local) == 0 && len(external) == 0 && firstErr == nil {
		firstErr = store.ErrNotFound
	}
	return firstErr
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
	if _, err := s.StoreMessage(ctx, acct.ID, mbID, raw, parsed, &meta); err != nil {
		if errors.Is(err, store.ErrQuota) {
			return smtp.NewReply(552, "5.2.2", "Mailbox full")
		}
		return err
	}
	if role == "inbox" && envFrom != "" && envFrom != "<>" {
		s.maybeVacation(ctx, acct, envFrom, parsed)
	}
	return nil
}

// StoreMessage writes the blob and metadata into a mailbox. It fails with
// store.ErrQuota when the account quota would be exceeded.
func (s *Service) StoreMessage(ctx context.Context, accountID, mailboxID int64, raw []byte, parsed *message.Parsed, meta *deliveryMeta) (*store.Message, error) {
	if err := s.Store.CheckQuota(ctx, accountID, int64(len(raw))); err != nil {
		return nil, err
	}
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

// checkSubmittedHeaders verifies that the From header (and Sender, if
// present) of a submitted message belong to the account.
func (s *Service) checkSubmittedHeaders(ctx context.Context, acct *store.Account, data []byte) error {
	p, err := message.Parse(data, false)
	if err != nil {
		return smtp.NewReply(553, "5.7.1", "Message header could not be parsed")
	}
	if p.FromErr != nil {
		if errors.Is(p.FromErr, message.ErrFromMultiple) {
			return smtp.NewReply(553, "5.7.1", "Multiple From headers are not allowed")
		}
		return smtp.NewReply(553, "5.7.1", "Missing or invalid From header")
	}
	if len(p.FromAll) != 1 {
		return smtp.NewReply(553, "5.7.1", "From header must contain exactly one address")
	}
	if !s.Store.SenderAllowed(ctx, acct, p.FromAll[0].Email) {
		return smtp.NewReply(553, "5.7.1", "From header not owned by authenticated user")
	}
	sender, err := message.SenderAddress(p.Header)
	if err != nil {
		return smtp.NewReply(553, "5.7.1", "Invalid Sender header")
	}
	if sender != nil && !s.Store.SenderAllowed(ctx, acct, sender.Email) {
		return smtp.NewReply(553, "5.7.1", "Sender header not owned by authenticated user")
	}
	return nil
}

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
	// Header From (and Sender) must belong to the account as well.
	if err := s.checkSubmittedHeaders(ctx, acct, data); err != nil {
		return nil, err
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
	var failed []failedRcpt
	for _, r := range rcpts {
		local, external, err := s.Store.ResolveRecipient(ctx, r)
		if err != nil {
			return nil, err
		}
		for _, l := range local {
			if err := s.deliverLocal(ctx, l, signed, nil, deliveryMeta{}, from); err != nil {
				var rep *smtp.Reply
				errors.As(err, &rep)
				s.Log.Warn("local delivery failed", "rcpt", l, "err", err)
				failed = append(failed, failedRcpt{l, rep})
			}
		}
		remote = append(remote, external...)
	}
	if len(remote) > 0 {
		if err := s.enqueue(ctx, from, remote, signed); err != nil {
			return nil, err
		}
	}
	if len(failed) > 0 {
		s.bounceFailed(ctx, from, failed, signed)
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
// SRS senders of forwarded mail are reversed to the address the message
// originally came from.
func (s *Service) Bounce(ctx context.Context, origFrom string, failed []string, reason string, orig []byte) {
	if origFrom == "" || origFrom == "<>" {
		return
	}
	if s.localSRS(ctx, origFrom) {
		back, err := s.reverseSRS(ctx, origFrom)
		if err != nil {
			s.Log.Warn("bounce to invalid srs address dropped", "to", origFrom, "err", err)
			return
		}
		origFrom = back
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

// addressedTo reports whether one of the account's addresses appears in the
// To or Cc header (RFC 3834 section 2).
func addressedTo(acct *store.Account, parsed *message.Parsed) bool {
	mine := map[string]bool{store.Normalize(acct.Email): true}
	for _, a := range acct.Aliases {
		mine[store.Normalize(a)] = true
	}
	for _, l := range [][]store.Address{parsed.To, parsed.Cc} {
		for _, a := range l {
			if mine[store.Normalize(a.Email)] {
				return true
			}
		}
	}
	return false
}

// automatedSender reports whether an envelope sender belongs to a bounce,
// list or other automated source that must never get an auto-reply.
func automatedSender(sender string) bool {
	if sender == "" || sender == "<>" || srs.IsSRS(sender) {
		return true
	}
	local := strings.ToLower(sender)
	if i := strings.LastIndexByte(local, '@'); i >= 0 {
		local = local[:i]
	}
	if base, _, ok := strings.Cut(local, "+"); ok {
		local = base
	}
	switch local {
	case "mailer-daemon", "postmaster", "listserv", "majordomo", "bounce", "bounces", "daemon":
		return true
	}
	return strings.HasPrefix(local, "noreply") || strings.HasPrefix(local, "no-reply") || strings.HasPrefix(local, "do-not-reply") ||
		strings.HasPrefix(local, "donotreply") || strings.HasPrefix(local, "owner-") || strings.HasPrefix(local, "bounce-") ||
		strings.HasPrefix(local, "bounces-") || strings.HasSuffix(local, "-request") || strings.HasSuffix(local, "-bounces") ||
		strings.HasSuffix(local, "-owner")
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
	if parsed == nil || !addressedTo(acct, parsed) {
		return
	}
	if parsed.Header.Get("Auto-Submitted") != "" && !strings.EqualFold(parsed.Header.Get("Auto-Submitted"), "no") {
		return
	}
	prec := strings.ToLower(parsed.Header.Get("Precedence"))
	if prec == "bulk" || prec == "list" || prec == "junk" || parsed.Header.Get("List-Id") != "" ||
		parsed.Header.Get("List-Unsubscribe") != "" || parsed.Header.Get("List-Post") != "" {
		return
	}
	lower := store.Normalize(sender)
	if automatedSender(lower) || lower == acct.Email {
		return
	}
	if !s.Store.VacationShouldReply(ctx, acct.ID, lower) {
		return
	}
	subject := v.Subject
	if subject == "" {
		subject = "Abwesend: " + parsed.Subject
	}
	draft := message.Draft{
		From:      store.Address{Name: acct.Name, Email: acct.Email},
		To:        []store.Address{{Email: lower}},
		Subject:   subject,
		Text:      v.Body,
		MessageID: message.NewMessageID(store.DomainOf(acct.Email)),
		Extra:     map[string]string{"Auto-Submitted": "auto-replied", "Precedence": "bulk"},
	}
	if parsed.MessageID != "" {
		draft.InReplyTo = parsed.MessageID
	}
	raw := s.sign(ctx, acct.Email, message.Build(draft))
	local, external, err := s.Store.ResolveRecipient(ctx, lower)
	if err != nil {
		return
	}
	for _, l := range local {
		_ = s.deliverLocal(ctx, l, raw, nil, deliveryMeta{}, "")
	}
	if len(external) > 0 {
		_ = s.enqueue(ctx, "", external, raw)
	}
	s.Log.Info("vacation reply sent", "user", acct.Email, "to", lower)
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
	if err := s.Store.CheckQuota(ctx, acct.ID, int64(len(raw))); err != nil {
		if errors.Is(err, store.ErrQuota) {
			return nil, fmt.Errorf("Quota von %s überschritten: %w", acct.Email, err)
		}
		return nil, err
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
