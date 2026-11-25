package store

import (
	"context"
	"strings"
	"time"
)

// JunkMessage is a message sitting in the junk (quarantine) folder of any
// account, listed for the admin quarantine view.
type JunkMessage struct {
	Message
	AccountID int64  `json:"accountId"`
	Account   string `json:"account"` // email address of the owning account
}

// QuarantineList returns messages in junk folders across all accounts,
// newest first, for the admin quarantine page. accountIDs maps message ids
// to the owning account email (resolved in one batch query).
func (s *Store) QuarantineList(ctx context.Context, limit int) ([]JunkMessage, map[int64]string, error) {
	// No JOIN here: msgCols uses unqualified names (id) which would become
	// ambiguous with mailboxes.id.
	rows, err := s.DB.QueryContext(ctx, `SELECT `+msgCols+` FROM messages
		WHERE mailbox_id IN (SELECT id FROM mailboxes WHERE role = 'junk')
		ORDER BY received_at DESC, id DESC LIMIT ?`, limit)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()
	var out []JunkMessage
	ids := map[int64]bool{}
	for rows.Next() {
		var j JunkMessage
		m, err := scanMessage(rows)
		if err != nil {
			return nil, nil, err
		}
		j.Message = *m
		j.AccountID = m.AccountID
		ids[m.AccountID] = true
		out = append(out, j)
	}
	if err := rows.Err(); err != nil {
		return nil, nil, err
	}
	emails := map[int64]string{}
	if len(ids) > 0 {
		ph := make([]string, 0, len(ids))
		args := make([]any, 0, len(ids))
		for id := range ids {
			ph = append(ph, "?")
			args = append(args, id)
		}
		a, err := s.DB.QueryContext(ctx, `SELECT id, email FROM accounts WHERE id IN (`+strings.Join(ph, ",")+`)`, args...)
		if err != nil {
			return nil, nil, err
		}
		defer a.Close()
		for a.Next() {
			var id int64
			var email string
			if err := a.Scan(&id, &email); err == nil {
				emails[id] = email
			}
		}
	}
	for i := range out {
		out[i].Account = emails[out[i].AccountID]
	}
	return out, emails, nil
}

// MessagesSince returns the account's messages received after since, newest
// first (limited lookback for AI briefings).
func (s *Store) MessagesSince(ctx context.Context, accountID int64, since time.Time, limit int) ([]*Message, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT `+msgCols+` FROM messages
		WHERE account_id = ? AND received_at > ? ORDER BY received_at DESC, id DESC LIMIT ?`,
		accountID, since.Unix(), limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Message
	for rows.Next() {
		m, err := scanMessage(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// AllAccountEmails returns the addresses of all enabled accounts.
func (s *Store) AllAccountEmails(ctx context.Context) (map[int64]string, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT id, email FROM accounts WHERE disabled = 0`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[int64]string{}
	for rows.Next() {
		var id int64
		var email string
		if err := rows.Scan(&id, &email); err != nil {
			return nil, err
		}
		out[id] = email
	}
	return out, rows.Err()
}
