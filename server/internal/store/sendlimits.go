package store

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

// Outbound send limits per account. They protect the server's reputation
// when an account is taken over: a sliding window over the last hour and the
// last 24 hours is kept in SQLite (send_log) so it survives restarts.

// SendLimitSettings are the server-wide defaults (settings section "limits").
// A value of 0 disables the respective limit.
type SendLimitSettings struct {
	// MaxRecipientsPerMessage counts every envelope recipient, local or remote.
	MaxRecipientsPerMessage int `json:"maxRecipientsPerMessage"`
	// MaxMessagesPerHour counts messages with at least one remote recipient.
	MaxMessagesPerHour int `json:"maxMessagesPerHour"`
	// MaxRecipientsPerDay counts remote recipients in the last 24 hours.
	MaxRecipientsPerDay int `json:"maxRecipientsPerDay"`
}

// Hard upper bounds for the configurable values.
const (
	MaxSendLimitRecipientsPerMessage = 1000
	MaxSendLimitValue                = 1_000_000
)

func applySendLimitDefaults(s *Settings) {
	s.Limits = SendLimitSettings{MaxRecipientsPerMessage: 100, MaxMessagesPerHour: 300, MaxRecipientsPerDay: 2000}
}

// SendLimitOverride replaces the defaults for one account. A nil field keeps
// the default, 0 disables that limit. Exempt disables all limits.
type SendLimitOverride struct {
	MaxRecipientsPerMessage *int `json:"maxRecipientsPerMessage"`
	MaxMessagesPerHour      *int `json:"maxMessagesPerHour"`
	MaxRecipientsPerDay     *int `json:"maxRecipientsPerDay"`
	Exempt                  bool `json:"exempt"`
}

// IsZero reports whether the override changes nothing.
func (o SendLimitOverride) IsZero() bool {
	return !o.Exempt && o.MaxRecipientsPerMessage == nil && o.MaxMessagesPerHour == nil && o.MaxRecipientsPerDay == nil
}

// SendLimits are the limits in effect for one account (0 = unlimited).
type SendLimits struct {
	MaxRecipientsPerMessage int  `json:"maxRecipientsPerMessage"`
	MaxMessagesPerHour      int  `json:"maxMessagesPerHour"`
	MaxRecipientsPerDay     int  `json:"maxRecipientsPerDay"`
	Exempt                  bool `json:"exempt"`
}

// Effective combines the defaults with an optional override.
func (d SendLimitSettings) Effective(o *SendLimitOverride) SendLimits {
	l := SendLimits{MaxRecipientsPerMessage: d.MaxRecipientsPerMessage, MaxMessagesPerHour: d.MaxMessagesPerHour, MaxRecipientsPerDay: d.MaxRecipientsPerDay}
	if o == nil {
		return l
	}
	if o.Exempt {
		return SendLimits{Exempt: true}
	}
	if o.MaxRecipientsPerMessage != nil {
		l.MaxRecipientsPerMessage = *o.MaxRecipientsPerMessage
	}
	if o.MaxMessagesPerHour != nil {
		l.MaxMessagesPerHour = *o.MaxMessagesPerHour
	}
	if o.MaxRecipientsPerDay != nil {
		l.MaxRecipientsPerDay = *o.MaxRecipientsPerDay
	}
	return l
}

// SendUsage is the outbound volume of an account in the sliding windows.
type SendUsage struct {
	MessagesLastHour   int `json:"messagesLastHour"`
	RecipientsLastHour int `json:"recipientsLastHour"`
	MessagesLastDay    int `json:"messagesLastDay"`
	RecipientsLastDay  int `json:"recipientsLastDay"`
}

// Kinds of exceeded limits.
const (
	SendLimitRecipients = "recipients" // per message
	SendLimitHourly     = "hourly"     // messages per hour
	SendLimitDaily      = "daily"      // recipients per day
)

// SendLimitExceeded is returned by ReserveSend when a window is exhausted.
type SendLimitExceeded struct {
	Kind  string
	Limit int
	Usage SendUsage
}

func (e *SendLimitExceeded) Error() string { return "send limit exceeded: " + e.Kind }

// GetSendLimitOverride returns the override of an account (nil if none).
func (s *Store) GetSendLimitOverride(ctx context.Context, accountID int64) (*SendLimitOverride, error) {
	var a, b, c sql.NullInt64
	var o SendLimitOverride
	err := s.DB.QueryRowContext(ctx, `SELECT max_rcpts_msg, max_msgs_hour, max_rcpts_day, exempt FROM account_send_limits WHERE account_id = ?`, accountID).
		Scan(&a, &b, &c, &o.Exempt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	o.MaxRecipientsPerMessage, o.MaxMessagesPerHour, o.MaxRecipientsPerDay = nullInt(a), nullInt(b), nullInt(c)
	return &o, nil
}

// SetSendLimitOverride stores (or with a zero override removes) the override.
func (s *Store) SetSendLimitOverride(ctx context.Context, accountID int64, o SendLimitOverride) error {
	if o.IsZero() {
		_, err := s.DB.ExecContext(ctx, `DELETE FROM account_send_limits WHERE account_id = ?`, accountID)
		return err
	}
	_, err := s.DB.ExecContext(ctx, `INSERT INTO account_send_limits(account_id, max_rcpts_msg, max_msgs_hour, max_rcpts_day, exempt)
		VALUES(?, ?, ?, ?, ?)
		ON CONFLICT(account_id) DO UPDATE SET max_rcpts_msg = excluded.max_rcpts_msg, max_msgs_hour = excluded.max_msgs_hour,
			max_rcpts_day = excluded.max_rcpts_day, exempt = excluded.exempt`,
		accountID, intArg(o.MaxRecipientsPerMessage), intArg(o.MaxMessagesPerHour), intArg(o.MaxRecipientsPerDay), o.Exempt)
	return err
}

// SendLimitsFor returns the limits in effect for an account.
func (s *Store) SendLimitsFor(ctx context.Context, accountID int64, defaults SendLimitSettings) (SendLimits, error) {
	o, err := s.GetSendLimitOverride(ctx, accountID)
	if err != nil {
		return defaults.Effective(nil), err
	}
	return defaults.Effective(o), nil
}

func usageQuery(ctx context.Context, q interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}, accountID int64, at time.Time) (SendUsage, error) {
	var u SendUsage
	hour, day := at.Add(-time.Hour).Unix(), at.Add(-24*time.Hour).Unix()
	err := q.QueryRowContext(ctx, `SELECT
			COALESCE(SUM(CASE WHEN at > ? THEN 1 ELSE 0 END), 0),
			COALESCE(SUM(CASE WHEN at > ? THEN rcpts ELSE 0 END), 0),
			COUNT(*), COALESCE(SUM(rcpts), 0)
		FROM send_log WHERE account_id = ? AND at > ?`, hour, hour, accountID, day).
		Scan(&u.MessagesLastHour, &u.RecipientsLastHour, &u.MessagesLastDay, &u.RecipientsLastDay)
	return u, err
}

// SendUsage returns the outbound volume of an account at the given time.
func (s *Store) SendUsage(ctx context.Context, accountID int64, at time.Time) (SendUsage, error) {
	return usageQuery(ctx, s.DB, accountID, at)
}

// ReserveSend checks the hourly and daily windows for a message with rcpts
// remote recipients and records it when both still have room. It returns a
// *SendLimitExceeded otherwise. Messages without remote recipients are not
// recorded.
func (s *Store) ReserveSend(ctx context.Context, accountID int64, rcpts int, lim SendLimits, at time.Time) error {
	if rcpts <= 0 || lim.Exempt {
		return nil
	}
	return s.tx(ctx, func(tx *sql.Tx) error {
		u, err := usageQuery(ctx, tx, accountID, at)
		if err != nil {
			return err
		}
		if lim.MaxMessagesPerHour > 0 && u.MessagesLastHour+1 > lim.MaxMessagesPerHour {
			return &SendLimitExceeded{Kind: SendLimitHourly, Limit: lim.MaxMessagesPerHour, Usage: u}
		}
		if lim.MaxRecipientsPerDay > 0 && u.RecipientsLastDay+rcpts > lim.MaxRecipientsPerDay {
			return &SendLimitExceeded{Kind: SendLimitDaily, Limit: lim.MaxRecipientsPerDay, Usage: u}
		}
		_, err = tx.ExecContext(ctx, `INSERT INTO send_log(account_id, at, rcpts) VALUES(?, ?, ?)`, accountID, at.Unix(), rcpts)
		return err
	})
}

// CheckSendWindow reports a *SendLimitExceeded when the account cannot send
// even a single message with one remote recipient right now.
func (s *Store) CheckSendWindow(ctx context.Context, accountID int64, lim SendLimits, at time.Time) error {
	if lim.Exempt || (lim.MaxMessagesPerHour <= 0 && lim.MaxRecipientsPerDay <= 0) {
		return nil
	}
	u, err := s.SendUsage(ctx, accountID, at)
	if err != nil {
		return err
	}
	if lim.MaxMessagesPerHour > 0 && u.MessagesLastHour >= lim.MaxMessagesPerHour {
		return &SendLimitExceeded{Kind: SendLimitHourly, Limit: lim.MaxMessagesPerHour, Usage: u}
	}
	if lim.MaxRecipientsPerDay > 0 && u.RecipientsLastDay >= lim.MaxRecipientsPerDay {
		return &SendLimitExceeded{Kind: SendLimitDaily, Limit: lim.MaxRecipientsPerDay, Usage: u}
	}
	return nil
}

// RecordSendLimitHit remembers that an account ran into a limit.
func (s *Store) RecordSendLimitHit(ctx context.Context, accountID int64, kind string, at time.Time) error {
	_, err := s.DB.ExecContext(ctx, `INSERT INTO send_limit_hits(account_id, at, kind) VALUES(?, ?, ?)`, accountID, at.Unix(), kind)
	return err
}

// SendLimitHitSummary describes the limit hits of one account in a period.
type SendLimitHitSummary struct {
	AccountID int64     `json:"accountId"`
	Email     string    `json:"email"`
	Hits      int       `json:"hits"`
	LastHit   time.Time `json:"lastHit"`
	LastKind  string    `json:"lastKind"`
}

// SendLimitHitsSince lists the accounts that hit a limit after since.
func (s *Store) SendLimitHitsSince(ctx context.Context, since time.Time) ([]SendLimitHitSummary, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT h.account_id, COALESCE(a.email, ''), COUNT(*), MAX(h.at),
			(SELECT kind FROM send_limit_hits x WHERE x.account_id = h.account_id ORDER BY x.at DESC, x.id DESC LIMIT 1)
		FROM send_limit_hits h LEFT JOIN accounts a ON a.id = h.account_id
		WHERE h.at > ? GROUP BY h.account_id ORDER BY MAX(h.at) DESC`, since.Unix())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []SendLimitHitSummary
	for rows.Next() {
		var h SendLimitHitSummary
		var last int64
		if err := rows.Scan(&h.AccountID, &h.Email, &h.Hits, &last, &h.LastKind); err != nil {
			return nil, err
		}
		h.LastHit = ts(last)
		out = append(out, h)
	}
	return out, rows.Err()
}

// PruneSendLog removes window entries older than 24 hours and limit hits
// older than a week.
func (s *Store) PruneSendLog(ctx context.Context, at time.Time) error {
	if _, err := s.DB.ExecContext(ctx, `DELETE FROM send_log WHERE at <= ?`, at.Add(-24*time.Hour).Unix()); err != nil {
		return err
	}
	_, err := s.DB.ExecContext(ctx, `DELETE FROM send_limit_hits WHERE at <= ?`, at.Add(-7*24*time.Hour).Unix())
	return err
}

func nullInt(v sql.NullInt64) *int {
	if !v.Valid {
		return nil
	}
	i := int(v.Int64)
	return &i
}

func intArg(p *int) any {
	if p == nil {
		return nil
	}
	return *p
}
