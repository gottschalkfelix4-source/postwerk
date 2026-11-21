package delivery

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"time"

	"postwerk/internal/metrics"
	"postwerk/internal/smtp"
	"postwerk/internal/store"
)

// SendLimitError reports that an account reached one of its send limits. It
// unwraps to the SMTP reply, so errors.As(err, **smtp.Reply) keeps working.
type SendLimitError struct {
	Kind  string // store.SendLimitRecipients | SendLimitHourly | SendLimitDaily
	Limit int
	Reply *smtp.Reply
}

func newSendLimitError(kind string, limit int) *SendLimitError {
	var r *smtp.Reply
	switch kind {
	case store.SendLimitRecipients:
		// RFC 5321 4.5.3.1.10: too many recipients is a temporary 452.
		r = smtp.NewReply(452, "4.5.3", "Too many recipients")
	case store.SendLimitHourly:
		r = smtp.NewReply(450, "4.7.1", "Hourly sending limit reached, try again later")
	default:
		r = smtp.NewReply(450, "4.7.1", "Daily recipient limit reached, try again later")
	}
	return &SendLimitError{Kind: kind, Limit: limit, Reply: r}
}

func (e *SendLimitError) Error() string { return e.Message() }
func (e *SendLimitError) Unwrap() error { return e.Reply }

// Message is the German explanation for the web interface and JMAP clients.
func (e *SendLimitError) Message() string {
	switch e.Kind {
	case store.SendLimitRecipients:
		return fmt.Sprintf("Zu viele Empfänger: Pro Nachricht sind höchstens %d Empfänger erlaubt.", e.Limit)
	case store.SendLimitHourly:
		return fmt.Sprintf("Sendelimit erreicht: Dieses Konto darf höchstens %d Nachrichten pro Stunde an externe Empfänger senden. Bitte später erneut versuchen.", e.Limit)
	}
	return fmt.Sprintf("Sendelimit erreicht: Dieses Konto darf höchstens %d externe Empfänger in 24 Stunden anschreiben. Bitte später erneut versuchen.", e.Limit)
}

// limitHitInterval suppresses repeated log lines, hit records and
// notifications while a client keeps retrying.
const limitHitInterval = time.Minute

// SendLimits returns the limits in effect for an account.
func (s *Service) SendLimits(ctx context.Context, acct *store.Account) store.SendLimits {
	lim, err := s.Store.SendLimitsFor(ctx, acct.ID, s.Settings.Get().Limits)
	if err != nil {
		s.Log.Error("loading send limit override failed", "user", acct.Email, "err", err)
	}
	return lim
}

// limitError records a limit hit (log, metric, ops warning, notification)
// and returns the error for the client.
func (s *Service) limitError(ctx context.Context, acct *store.Account, kind string, limit int, source string) *SendLimitError {
	metrics.SendLimitHits.WithLabelValues(kind).Inc()
	key := strconv.FormatInt(acct.ID, 10) + ":" + kind
	now := time.Now()
	s.hitMu.Lock()
	if s.lastHit == nil {
		s.lastHit = map[string]time.Time{}
	}
	fresh := now.Sub(s.lastHit[key]) >= limitHitInterval
	if fresh {
		s.lastHit[key] = now
	}
	s.hitMu.Unlock()
	if fresh {
		s.Log.Warn("send limit reached", "user", acct.Email, "kind", kind, "limit", limit, "source", source)
		if err := s.Store.RecordSendLimitHit(ctx, acct.ID, kind, now); err != nil {
			s.Log.Error("recording send limit hit failed", "err", err)
		}
		if s.OnSendLimit != nil {
			s.OnSendLimit(acct, kind, limit)
		}
	}
	return newSendLimitError(kind, limit)
}

// checkRecipientCount enforces the per-message recipient limit.
func (s *Service) checkRecipientCount(ctx context.Context, acct *store.Account, lim store.SendLimits, n int, source string) error {
	if lim.Exempt || lim.MaxRecipientsPerMessage <= 0 || n <= lim.MaxRecipientsPerMessage {
		return nil
	}
	return s.limitError(ctx, acct, store.SendLimitRecipients, lim.MaxRecipientsPerMessage, source)
}

// reserveSend counts a message with remote recipients against the windows.
func (s *Service) reserveSend(ctx context.Context, acct *store.Account, lim store.SendLimits, remote int, source string) error {
	err := s.Store.ReserveSend(ctx, acct.ID, remote, lim, time.Now())
	var ex *store.SendLimitExceeded
	if errors.As(err, &ex) {
		return s.limitError(ctx, acct, ex.Kind, ex.Limit, source)
	}
	return err
}

// checkSubmissionRecipient runs at RCPT TO on the submission ports: it
// refuses recipients beyond the per-message limit and, for remote
// recipients, an account whose windows are already used up.
func (s *Service) checkSubmissionRecipient(ctx context.Context, env *smtp.Envelope, rcpt string) *smtp.Reply {
	if env.Account == nil {
		return nil
	}
	lim := s.SendLimits(ctx, env.Account)
	if lim.Exempt {
		return nil
	}
	if err := s.checkRecipientCount(ctx, env.Account, lim, len(env.Rcpts)+1, "smtp"); err != nil {
		return err.(*SendLimitError).Reply
	}
	if lim.MaxMessagesPerHour <= 0 && lim.MaxRecipientsPerDay <= 0 {
		return nil
	}
	_, external, err := s.Store.ResolveRecipient(ctx, rcpt)
	if err != nil || len(external) == 0 {
		return nil
	}
	var ex *store.SendLimitExceeded
	if err := s.Store.CheckSendWindow(ctx, env.Account.ID, lim, time.Now()); errors.As(err, &ex) {
		return s.limitError(ctx, env.Account, ex.Kind, ex.Limit, "smtp").Reply
	}
	return nil
}

// limitRedirects drops Sieve redirects to remote targets that would exceed
// the account's send limits. Redirects count like messages the account sends.
func (s *Service) limitRedirects(ctx context.Context, acct *store.Account, targets []string, log *slog.Logger) []string {
	var lim *store.SendLimits
	out := make([]string, 0, len(targets))
	for _, t := range targets {
		_, external, err := s.Store.ResolveRecipient(ctx, t)
		if err != nil || len(external) == 0 {
			out = append(out, t)
			continue
		}
		if lim == nil {
			l := s.SendLimits(ctx, acct)
			lim = &l
		}
		if err := s.reserveSend(ctx, acct, *lim, len(external), "sieve"); err != nil {
			var le *SendLimitError
			if errors.As(err, &le) {
				log.Warn("sieve redirect skipped (send limit)", "to", t, "kind", le.Kind, "limit", le.Limit)
				continue
			}
			log.Error("send limit check failed, redirecting anyway", "to", t, "err", err)
		}
		out = append(out, t)
	}
	return out
}
