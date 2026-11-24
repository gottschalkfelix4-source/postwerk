package store

import (
	"context"
	"strings"
	"time"
)

// Unsubscriber remembers that an account unsubscribed from a newsletter
// sender. Mail from that sender is filed into the account's "Newsletter"
// folder instead of the inbox (see delivery/sieve.go).
type Unsubscriber struct {
	ID        int64     `json:"id"`
	AccountID int64     `json:"accountId"`
	Sender    string    `json:"sender"` // normalized address or domain
	CreatedAt time.Time `json:"createdAt"`
}

// AddUnsubscriber remembers the sender (address and its domain) together. The
// domain entry makes the rule effective even when the newsletter changes its
// sending sub-address (no-reply+id@domain).
func (s *Store) AddUnsubscriber(ctx context.Context, accountID int64, sender string) error {
	sender = Normalize(sender)
	if sender == "" {
		return nil
	}
	_, err := s.DB.ExecContext(ctx, `INSERT OR IGNORE INTO unsubscribers(account_id, sender, created_at) VALUES (?, ?, ?)`,
		accountID, sender, now())
	if err != nil {
		return err
	}
	if d := DomainOf(sender); d != "" && d != sender {
		_, err = s.DB.ExecContext(ctx, `INSERT OR IGNORE INTO unsubscribers(account_id, sender, created_at) VALUES (?, ?, ?)`,
			accountID, d, now())
	}
	return err
}

// RemoveUnsubscriber forgets the sender again.
func (s *Store) RemoveUnsubscriber(ctx context.Context, accountID int64, sender string) error {
	sender = Normalize(sender)
	_, err := s.DB.ExecContext(ctx, `DELETE FROM unsubscribers WHERE account_id = ? AND sender IN (?, ?)`,
		accountID, sender, DomainOf(sender))
	return err
}

// ListUnsubscribers returns the senders an account unsubscribed from.
func (s *Store) ListUnsubscribers(ctx context.Context, accountID int64) ([]Unsubscriber, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT id, account_id, sender, created_at FROM unsubscribers
		WHERE account_id = ? ORDER BY created_at DESC, id DESC`, accountID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Unsubscriber
	for rows.Next() {
		var u Unsubscriber
		var created int64
		if err := rows.Scan(&u.ID, &u.AccountID, &u.Sender, &created); err != nil {
			return nil, err
		}
		u.CreatedAt = ts(created)
		out = append(out, u)
	}
	return out, rows.Err()
}

// IsUnsubscribed reports whether the account unsubscribed from the address or
// any of its parent domains (newsletter+id@sub.example.com matches
// sub.example.com and example.com).
func (s *Store) IsUnsubscribed(ctx context.Context, accountID int64, from string) bool {
	from = Normalize(from)
	if from == "" {
		return false
	}
	sender := func() []string {
		var out []string
		for d := DomainOf(from); d != ""; {
			out = append(out, d)
			if i := strings.IndexByte(d, '.'); i >= 0 {
				d = d[i+1:]
			} else {
				break
			}
		}
		return out
	}()
	args := []any{accountID}
	placeholders := make([]string, 0, len(sender)+1)
	placeholders = append(placeholders, "?")
	args = append(args, from)
	for _, d := range sender {
		placeholders = append(placeholders, "?")
		args = append(args, d)
	}
	var n int
	err := s.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM unsubscribers WHERE account_id = ? AND sender IN (`+strings.Join(placeholders, ",")+`)`, args...).Scan(&n)
	return err == nil && n > 0
}
