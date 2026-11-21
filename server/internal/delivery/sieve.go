package delivery

// Sieve filtering during local delivery.
//
// The account's active script runs for messages bound for the inbox, after
// spam and virus handling. Messages already classified as junk (spam,
// DMARC quarantine, quarantined viruses) go to the junk folder without
// running the script, so a filter rule can never move spam back into a
// regular folder. Mail placed by role (e.g. Sent copies) is not filtered.
//
// The account vacation notice from the web interface stays independent of
// Sieve: it runs after the script whenever the message was stored in a
// folder, unless the script executed its own vacation action.

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/mail"
	"strings"
	"sync"

	"postwerk/internal/message"
	"postwerk/internal/sieve"
	"postwerk/internal/smtp"
	"postwerk/internal/store"
)

// redirectHeader marks messages redirected by a Sieve script; it is used to
// detect redirect loops between accounts.
const redirectHeader = "X-Sieve-Redirected-By"

// maxRedirectHops is the number of Sieve redirects a message may pass
// through before further redirects are ignored.
const maxRedirectHops = 8

var scriptCache = struct {
	sync.Mutex
	m map[[32]byte]*sieve.Script
}{m: map[[32]byte]*sieve.Script{}}

// CompileSieve compiles a script, caching compiled scripts by content.
func CompileSieve(content string) (*sieve.Script, error) {
	key := sha256.Sum256([]byte(content))
	scriptCache.Lock()
	sc, ok := scriptCache.m[key]
	scriptCache.Unlock()
	if ok {
		return sc, nil
	}
	sc, err := sieve.Compile(content)
	if err != nil {
		return nil, err
	}
	scriptCache.Lock()
	if len(scriptCache.m) >= 512 {
		scriptCache.m = map[[32]byte]*sieve.Script{}
	}
	scriptCache.m[key] = sc
	scriptCache.Unlock()
	return sc, nil
}

// SieveInput prepares a message for a script run. envTo is the original
// envelope recipient (with sub-address detail, if any).
func SieveInput(raw []byte, parsed *message.Parsed, envFrom, envTo string) *sieve.Message {
	m := sieve.ParseMessage(raw)
	if envFrom == "<>" {
		envFrom = ""
	}
	m.EnvelopeFrom, m.EnvelopeTo = envFrom, envTo
	if parsed == nil {
		var once sync.Once
		var text string
		m.BodyText = func() string {
			once.Do(func() {
				if p, err := message.Parse(raw, false); err == nil {
					text = p.Text
				}
			})
			return text
		}
	} else {
		m.BodyText = func() string { return parsed.Text }
	}
	return m
}

func (s *Service) sieveEnv(ctx context.Context, acct *store.Account) sieve.Env {
	return sieve.Env{MailboxExists: func(name string) bool {
		_, err := s.Store.MailboxByName(ctx, acct.ID, name)
		return err == nil
	}}
}

// SieveDryRun runs a script for the account without any side effects and
// returns the actions it would take.
func (s *Service) SieveDryRun(ctx context.Context, acct *store.Account, content string, raw []byte, envFrom, envTo string) (*sieve.Result, error) {
	sc, err := CompileSieve(content)
	if err != nil {
		return nil, err
	}
	if envTo == "" {
		envTo = acct.Email
	}
	return sieve.Execute(sc, SieveInput(raw, nil, envFrom, envTo), s.sieveEnv(ctx, acct))
}

// applySieve runs the account's active script. handled is false when no
// script applies (or it failed) and the caller delivers normally.
func (s *Service) applySieve(ctx context.Context, acct *store.Account, raw []byte, parsed *message.Parsed, meta deliveryMeta, envFrom string) (bool, error) {
	active, err := s.Store.ActiveSieveScript(ctx, acct.ID)
	if err != nil {
		if !errors.Is(err, store.ErrNotFound) {
			s.Log.Error("loading sieve script failed, delivering to inbox", "user", acct.Email, "err", err)
		}
		return false, nil
	}
	log := s.Log.With("user", acct.Email, "script", active.Name)
	sc, err := CompileSieve(active.Content)
	if err != nil {
		log.Warn("active sieve script is invalid, delivering to inbox", "err", err)
		return false, nil
	}
	if parsed == nil {
		if parsed, err = message.Parse(raw, false); err != nil {
			parsed = &message.Parsed{}
		}
	}
	rcpt := meta.Rcpt
	if rcpt == "" {
		rcpt = acct.Email
	}
	res, err := sieve.Execute(sc, SieveInput(raw, parsed, envFrom, rcpt), s.sieveEnv(ctx, acct))
	if err != nil {
		// RFC 5228 2.10.6: a run-time error leads to the implicit keep.
		log.Warn("sieve run-time error, delivering to inbox", "err", err)
		return false, nil
	}
	actions := res.Final()

	for _, a := range actions {
		if a.Type == "reject" || a.Type == "ereject" {
			log.Info("sieve rejected message", "from", envFrom)
			return true, smtp.NewReply(550, "5.7.1", rejectText(a.Reason))
		}
	}

	type target struct {
		id    int64
		flags []string
	}
	var targets []target
	seen := map[int64]int{}
	addTarget := func(id int64, flags []string) {
		if i, ok := seen[id]; ok {
			targets[i].flags = mergeFlags(targets[i].flags, flags)
			return
		}
		seen[id] = len(targets)
		targets = append(targets, target{id, flags})
	}
	inbox, err := s.Store.MailboxByRole(ctx, acct.ID, "inbox")
	if err != nil {
		return true, err
	}
	var redirects []string
	var vacation *sieve.Vacation
	for _, a := range actions {
		switch a.Type {
		case "keep":
			addTarget(inbox, a.Flags)
		case "fileinto":
			id, err := s.Store.MailboxByName(ctx, acct.ID, a.Mailbox)
			if errors.Is(err, store.ErrNotFound) && a.Create {
				id, err = s.Store.EnsureMailbox(ctx, acct.ID, a.Mailbox)
			}
			if err != nil {
				log.Warn("sieve fileinto: mailbox unavailable, keeping in inbox", "mailbox", a.Mailbox, "err", err)
				id = inbox
			}
			addTarget(id, a.Flags)
		case "redirect":
			redirects = append(redirects, a.Address)
		case "vacation":
			vacation = a.Vacation
		case "discard":
			log.Info("sieve discarded message", "from", envFrom)
		}
	}

	// Redirect loop protection: an account never redirects the same
	// message twice, and long redirect chains stop.
	hops := message.CountHeader(raw, redirectHeader)
	var allowed []string
	for _, r := range redirects {
		if hops >= maxRedirectHops || redirectedBy(raw, acct.Email) || store.Normalize(r) == acct.Email {
			log.Warn("sieve redirect skipped (loop protection)", "to", r)
			continue
		}
		allowed = append(allowed, r)
	}
	if len(redirects) > 0 && len(allowed) == 0 && len(targets) == 0 {
		// Never lose a message because all its redirects were dropped.
		addTarget(inbox, res.KeepFlags)
	}

	storedRegular := false
	for _, t := range targets {
		m, err := s.StoreMessage(ctx, acct.ID, t.id, raw, parsed, &meta)
		if err != nil {
			if errors.Is(err, store.ErrQuota) {
				return true, smtp.NewReply(552, "5.2.2", "Mailbox full")
			}
			return true, err
		}
		if len(t.flags) > 0 {
			if err := s.Store.SetIMAPFlags(ctx, acct.ID, m.ID, imapFlags(t.flags)); err != nil {
				log.Warn("setting sieve flags failed", "err", err)
			}
		}
		if mb, err := s.Store.MailboxOwned(ctx, acct.ID, t.id); err == nil && (mb.Role == nil || (*mb.Role != "junk" && *mb.Role != "trash")) {
			storedRegular = true
		}
	}
	for _, r := range allowed {
		if err := s.sieveRedirect(ctx, acct, r, raw, envFrom, meta); err != nil {
			log.Error("sieve redirect failed", "to", r, "err", err)
			continue
		}
		log.Info("sieve redirected message", "to", r)
	}

	if envFrom != "" && envFrom != "<>" {
		switch {
		case vacation != nil:
			s.sieveVacation(ctx, acct, vacation, envFrom, parsed)
		case storedRegular:
			s.maybeVacation(ctx, acct, envFrom, parsed)
		}
	}
	return true, nil
}

func redirectedBy(raw []byte, email string) bool {
	for _, f := range bytes.Split(headerSection(raw), []byte("\n")) {
		name, value, ok := strings.Cut(string(f), ":")
		if ok && strings.EqualFold(strings.TrimSpace(name), redirectHeader) && store.Normalize(value) == email {
			return true
		}
	}
	return false
}

func headerSection(raw []byte) []byte {
	if i := bytes.Index(raw, []byte("\r\n\r\n")); i >= 0 {
		return raw[:i]
	}
	if i := bytes.Index(raw, []byte("\n\n")); i >= 0 {
		return raw[:i]
	}
	return raw
}

// sieveRedirect sends the message on to target: local accounts get it
// directly, remote targets through the queue with an SRS envelope sender.
func (s *Service) sieveRedirect(ctx context.Context, acct *store.Account, target string, raw []byte, envFrom string, meta deliveryMeta) error {
	data := append([]byte(redirectHeader+": "+acct.Email+"\r\n"), raw...)
	local, external, err := s.Store.ResolveRecipient(ctx, target)
	if err != nil {
		return err
	}
	if len(local) == 0 && len(external) == 0 {
		return store.ErrNotFound
	}
	var firstErr error
	for _, l := range local {
		m := deliveryMeta{Auth: meta.Auth, SpamScore: meta.SpamScore, Rcpt: target}
		if err := s.deliverLocal(ctx, l, data, nil, m, envFrom); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	if len(external) > 0 {
		if err := s.enqueue(ctx, s.forwardSender(ctx, envFrom, store.DomainOf(acct.Email)), external, s.SealForward(ctx, store.DomainOf(acct.Email), meta.Auth, data)); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

// sieveVacation sends the auto-reply of a Sieve vacation action (RFC 5230).
func (s *Service) sieveVacation(ctx context.Context, acct *store.Account, v *sieve.Vacation, sender string, parsed *message.Parsed) {
	lower := store.Normalize(sender)
	if automatedSender(lower) || lower == acct.Email {
		return
	}
	addressed := addressedTo(acct, parsed)
	if !addressed {
		extra := map[string]bool{}
		for _, a := range v.Addresses {
			extra[store.Normalize(a)] = true
		}
		for _, l := range [][]store.Address{parsed.To, parsed.Cc} {
			for _, a := range l {
				if extra[store.Normalize(a.Email)] {
					addressed = true
				}
			}
		}
	}
	if !addressed {
		return
	}
	if as := parsed.Header.Get("Auto-Submitted"); as != "" && !strings.EqualFold(as, "no") {
		return
	}
	prec := strings.ToLower(parsed.Header.Get("Precedence"))
	if prec == "bulk" || prec == "list" || prec == "junk" || parsed.Header.Get("List-Id") != "" ||
		parsed.Header.Get("List-Unsubscribe") != "" || parsed.Header.Get("List-Post") != "" {
		return
	}
	handle := v.Handle
	if handle == "" {
		sum := sha256.Sum256([]byte(v.Subject + "\x00" + v.From + "\x00" + v.Reason))
		handle = hex.EncodeToString(sum[:8])
	}
	if !s.Store.SieveVacationShouldReply(ctx, acct.ID, "sieve:"+handle+":"+lower, v.Days) {
		return
	}
	from := store.Address{Name: acct.Name, Email: acct.Email}
	if v.From != "" {
		if a, err := parseAddress(v.From); err == nil && s.Store.SenderAllowed(ctx, acct, a.Email) {
			from = a
		}
	}
	subject := v.Subject
	if subject == "" {
		subject = "Auto: " + parsed.Subject
	}
	body := v.Reason
	if v.Mime {
		// The reason is a MIME entity; send its content as text.
		if i := strings.Index(body, "\r\n\r\n"); i >= 0 {
			body = body[i+4:]
		}
	}
	draft := message.Draft{
		From:      from,
		To:        []store.Address{{Email: lower}},
		Subject:   subject,
		Text:      body,
		MessageID: message.NewMessageID(store.DomainOf(from.Email)),
		Extra:     map[string]string{"Auto-Submitted": "auto-replied"},
	}
	if parsed.MessageID != "" {
		draft.InReplyTo = parsed.MessageID
		draft.References = append(append([]string(nil), parsed.References...), parsed.MessageID)
	}
	raw := s.sign(ctx, from.Email, message.Build(draft))
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
	s.Log.Info("sieve vacation reply sent", "user", acct.Email, "to", lower)
}

func parseAddress(s string) (store.Address, error) {
	a, err := mail.ParseAddress(s)
	if err != nil {
		return store.Address{}, err
	}
	return store.Address{Name: a.Name, Email: store.Normalize(a.Address)}, nil
}

// imapFlags maps Sieve flags to message flags and keywords.
func imapFlags(flags []string) store.IMAPFlags {
	var f store.IMAPFlags
	for _, fl := range flags {
		switch strings.ToLower(fl) {
		case `\seen`:
			f.Seen = true
		case `\flagged`:
			f.Flagged = true
		case `\answered`:
			f.Answered = true
		case `\draft`:
			f.Draft = true
		case `\deleted`:
			f.Deleted = true
		default:
			if !strings.HasPrefix(fl, `\`) {
				f.Keywords = append(f.Keywords, fl)
			}
		}
	}
	return f
}

func mergeFlags(a, b []string) []string {
	out := append([]string(nil), a...)
	for _, f := range b {
		dup := false
		for _, x := range out {
			if strings.EqualFold(x, f) {
				dup = true
			}
		}
		if !dup {
			out = append(out, f)
		}
	}
	return out
}

// rejectText turns a script's reject reason into a single-line ASCII SMTP
// reply text.
func rejectText(reason string) string {
	repl := strings.NewReplacer("ä", "ae", "ö", "oe", "ü", "ue", "Ä", "Ae", "Ö", "Oe", "Ü", "Ue", "ß", "ss")
	reason = repl.Replace(reason)
	var b strings.Builder
	space := false
	for _, r := range reason {
		switch {
		case r == '\r' || r == '\n' || r == '\t' || r == ' ':
			space = true
			continue
		case r < 0x20 || r == 0x7f:
			continue
		case r > 0x7e:
			r = '?'
		}
		if space && b.Len() > 0 {
			b.WriteByte(' ')
		}
		space = false
		b.WriteRune(r)
		if b.Len() >= 200 {
			break
		}
	}
	if b.Len() == 0 {
		return "Message rejected by recipient"
	}
	return b.String()
}
