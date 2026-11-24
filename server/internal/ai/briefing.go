package ai

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"postwerk/internal/message"
	"postwerk/internal/store"
)

// Mailer delivers server-generated mail (delivery.Service implements it).
// It is declared here so the briefing does not import the delivery package.
type Mailer interface {
	SendSystemMail(ctx context.Context, from string, rcpts []string, raw []byte) error
	SystemSender(ctx context.Context) string
}

// BriefingResult reports what a daily briefing run did.
type BriefingResult struct {
	Accounts int      `json:"accounts"`
	Sent     int      `json:"sent"`
	Mails    int      `json:"mails"`
	Errors   []string `json:"errors,omitempty"`
}

// Briefing summarizes the messages each account received since the last run
// (min. since) and mails the summary to the account. Only accounts with at
// least one new message get a mail. The summary is built with the same
// injection protection as the per-message summaries; messages the model
// considers suspicious are listed without content.
func (c *Client) Briefing(ctx context.Context, st *store.Store, mailer Mailer, since time.Time, maxPerAccount int) (BriefingResult, error) {
	var res BriefingResult
	if mailer == nil {
		return res, errors.New("kein Mailversand für das Briefing konfiguriert")
	}
	accounts, err := st.AllAccountEmails(ctx)
	if err != nil {
		return res, err
	}
	if maxPerAccount <= 0 {
		maxPerAccount = 15
	}
	from := mailer.SystemSender(ctx)
	for accountID, email := range accounts {
		res.Accounts++
		msgs, err := st.MessagesSince(ctx, accountID, since, maxPerAccount)
		if err != nil {
			res.Errors = append(res.Errors, email+": "+err.Error())
			continue
		}
		if len(msgs) == 0 {
			continue
		}
		parsed := make([]*message.Parsed, len(msgs))
		for i, m := range msgs {
			p := &message.Parsed{}
			if raw, err := st.Blobs.Get(m.BlobHash); err == nil {
				if pp, err := message.Parse(raw, false); err == nil {
					p = pp
				}
			}
			parsed[i] = p
		}
		body := buildBriefingText(msgs, parsed)
		sum, err := c.Summarize(ctx, msgs, parsed)
		if err != nil {
			res.Errors = append(res.Errors, email+": "+err.Error())
			continue
		}
		body += "\n\n" + sum.Summary
		if len(sum.Bullets) > 0 {
			body += "\n\n" + strings.Join(sum.Bullets, "\n")
		}
		raw := message.Build(message.Draft{
			From:      store.Address{Name: "Postwerk", Email: from},
			To:        []store.Address{{Email: email}},
			Subject:   "Dein Postwerk-Tagesbriefing",
			Text:      body,
			MessageID: message.NewMessageID(store.DomainOf(from)),
			Extra:     map[string]string{"Auto-Submitted": "auto-generated", "Precedence": "bulk"},
		})
		if err := mailer.SendSystemMail(ctx, from, []string{email}, raw); err != nil {
			res.Errors = append(res.Errors, email+": "+err.Error())
			continue
		}
		res.Sent++
		res.Mails += len(msgs)
	}
	return res, nil
}

// buildBriefingText renders the per-message headings of a briefing so the
// mail is useful even if the model call fails.
func buildBriefingText(msgs []*store.Message, parsed []*message.Parsed) string {
	var b strings.Builder
	b.WriteString("Deine neuen Nachrichten seit dem letzten Briefing:\n")
	for _, m := range msgs {
		subject := m.Subject
		if subject == "" {
			subject = "(ohne Betreff)"
		}
		fmt.Fprintf(&b, "- %s — %s <%s>: %s\n", m.ReceivedAt.Format("02.01. 15:04"), headerValue(m.From.Name), headerValue(m.From.Email), headerValue(subject))
	}
	return strings.TrimRight(b.String(), "\n")
}

// briefingLogger returns a logger for the briefing loop.
func briefingLogger(log *slog.Logger) *slog.Logger {
	if log == nil {
		return slog.Default()
	}
	return log.With("component", "briefing")
}
