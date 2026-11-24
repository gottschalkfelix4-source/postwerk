package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"
)

// PendingSend is a webmail send that is parked briefly so the sender can
// cancel it ("Undo Send"). The raw message is kept in the database; it is
// submitted to the delivery service once send_at is reached.
type PendingSend struct {
	ID        int64     `json:"id"`
	AccountID int64     `json:"-"`
	From      string    `json:"-"`
	Rcpts     []string  `json:"-"`
	Raw       []byte    `json:"-"`
	CreatedAt time.Time `json:"createdAt"`
	SendAt    time.Time `json:"sendAt"`
}

// SavePendingSend parks a send until sendAt.
func (s *Store) SavePendingSend(ctx context.Context, accountID int64, from string, rcpts []string, raw []byte, sendAt time.Time) (*PendingSend, error) {
	rcptJSON, err := json.Marshal(rcpts)
	if err != nil {
		return nil, err
	}
	res, err := s.DB.ExecContext(ctx, `INSERT INTO pending_sends(account_id, from_addr, rcpts, raw, created_at, send_at)
		VALUES(?, ?, ?, ?, ?, ?)`, accountID, from, string(rcptJSON), raw, now(), sendAt.Unix())
	if err != nil {
		return nil, err
	}
	id, _ := res.LastInsertId()
	return s.GetPendingSend(ctx, id)
}

// GetPendingSend loads a parked send.
func (s *Store) GetPendingSend(ctx context.Context, id int64) (*PendingSend, error) {
	p, err := scanPendingSend(s.DB.QueryRowContext(ctx, `SELECT id, account_id, from_addr, rcpts, raw, created_at, send_at FROM pending_sends WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return p, err
}

// DuePendingSends returns the parked sends whose time has come, oldest first.
func (s *Store) DuePendingSends(ctx context.Context, at time.Time) ([]*PendingSend, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT id, account_id, from_addr, rcpts, raw, created_at, send_at
		FROM pending_sends WHERE send_at <= ? ORDER BY send_at, id`, at.Unix())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*PendingSend
	for rows.Next() {
		p, err := scanPendingSend(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// DeletePendingSend cancels a parked send. Ownership is enforced by the
// caller with the account id.
func (s *Store) DeletePendingSend(ctx context.Context, accountID, id int64) error {
	res, err := s.DB.ExecContext(ctx, `DELETE FROM pending_sends WHERE id = ? AND account_id = ?`, id, accountID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// PurgePendingSends removes all parked sends (shutdown, restore).
func (s *Store) PurgePendingSends(ctx context.Context) error {
	_, err := s.DB.ExecContext(ctx, `DELETE FROM pending_sends`)
	return err
}

func scanPendingSend(sc interface{ Scan(...any) error }) (*PendingSend, error) {
	var p PendingSend
	var created, sendAt int64
	var rcpts string
	if err := sc.Scan(&p.ID, &p.AccountID, &p.From, &rcpts, &p.Raw, &created, &sendAt); err != nil {
		return nil, err
	}
	if err := json.Unmarshal([]byte(rcpts), &p.Rcpts); err != nil {
		return nil, err
	}
	p.CreatedAt, p.SendAt = ts(created), ts(sendAt)
	return &p, nil
}
