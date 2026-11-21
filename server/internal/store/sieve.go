package store

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

// Limits for stored Sieve scripts.
const (
	MaxSieveScripts    = 20
	MaxSieveScriptSize = 64 << 10
	MaxSieveNameLen    = 128
)

var (
	// ErrSieveActive is returned when deleting the active script.
	ErrSieveActive = errors.New("sieve script is active")
	// ErrSieveQuota is returned when an account would exceed the script limits.
	ErrSieveQuota = errors.New("sieve script limit reached")
	// ErrSieveName is returned for invalid script names.
	ErrSieveName = errors.New("invalid sieve script name")
)

type SieveScript struct {
	Name      string    `json:"name"`
	Content   string    `json:"content,omitempty"`
	Active    bool      `json:"active"`
	Size      int       `json:"size"`
	UpdatedAt time.Time `json:"updatedAt"`
}

// ValidSieveName reports whether name is acceptable as a script name
// (RFC 5804 section 1.6: no control characters, bounded length).
func ValidSieveName(name string) bool {
	if name == "" || len(name) > MaxSieveNameLen || !utf8.ValidString(name) || strings.TrimSpace(name) != name {
		return false
	}
	for _, r := range name {
		if unicode.IsControl(r) || r == 0x2028 || r == 0x2029 {
			return false
		}
	}
	return true
}

// ListSieveScripts returns the account's scripts without their content.
func (s *Store) ListSieveScripts(ctx context.Context, accountID int64) ([]SieveScript, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT name, active, length(CAST(content AS BLOB)), updated_at FROM sieve_scripts WHERE account_id = ? ORDER BY name`, accountID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []SieveScript{}
	for rows.Next() {
		var sc SieveScript
		var upd int64
		if err := rows.Scan(&sc.Name, &sc.Active, &sc.Size, &upd); err != nil {
			return nil, err
		}
		sc.UpdatedAt = ts(upd)
		out = append(out, sc)
	}
	return out, rows.Err()
}

func (s *Store) scanSieve(row *sql.Row) (*SieveScript, error) {
	var sc SieveScript
	var upd int64
	err := row.Scan(&sc.Name, &sc.Content, &sc.Active, &upd)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	sc.Size = len(sc.Content)
	sc.UpdatedAt = ts(upd)
	return &sc, nil
}

// GetSieveScript returns one script with its content.
func (s *Store) GetSieveScript(ctx context.Context, accountID int64, name string) (*SieveScript, error) {
	return s.scanSieve(s.DB.QueryRowContext(ctx, `SELECT name, content, active, updated_at FROM sieve_scripts WHERE account_id = ? AND name = ?`, accountID, name))
}

// ActiveSieveScript returns the account's active script or ErrNotFound.
func (s *Store) ActiveSieveScript(ctx context.Context, accountID int64) (*SieveScript, error) {
	return s.scanSieve(s.DB.QueryRowContext(ctx, `SELECT name, content, active, updated_at FROM sieve_scripts WHERE account_id = ? AND active = 1`, accountID))
}

// SieveHaveSpace reports whether a script of the given size could be stored
// under name without exceeding the limits.
func (s *Store) SieveHaveSpace(ctx context.Context, accountID int64, name string, size int) error {
	if size > MaxSieveScriptSize {
		return ErrSieveQuota
	}
	var count int
	var exists bool
	err := s.DB.QueryRowContext(ctx, `SELECT COUNT(*), COALESCE(MAX(name = ?), 0) FROM sieve_scripts WHERE account_id = ?`, name, accountID).Scan(&count, &exists)
	if err != nil {
		return err
	}
	if !exists && count >= MaxSieveScripts {
		return ErrSieveQuota
	}
	return nil
}

// PutSieveScript creates or replaces a script. The content must already be
// validated by the caller.
func (s *Store) PutSieveScript(ctx context.Context, accountID int64, name, content string) error {
	if !ValidSieveName(name) {
		return ErrSieveName
	}
	return s.tx(ctx, func(tx *sql.Tx) error {
		var count int
		var exists bool
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*), COALESCE(MAX(name = ?), 0) FROM sieve_scripts WHERE account_id = ?`, name, accountID).Scan(&count, &exists); err != nil {
			return err
		}
		if len(content) > MaxSieveScriptSize || (!exists && count >= MaxSieveScripts) {
			return ErrSieveQuota
		}
		t := now()
		_, err := tx.ExecContext(ctx, `INSERT INTO sieve_scripts(account_id, name, content, active, created_at, updated_at) VALUES (?,?,?,0,?,?)
			ON CONFLICT(account_id, name) DO UPDATE SET content = excluded.content, updated_at = excluded.updated_at`,
			accountID, name, content, t, t)
		return err
	})
}

// SetActiveSieveScript activates the named script and deactivates all
// others. An empty name deactivates every script.
func (s *Store) SetActiveSieveScript(ctx context.Context, accountID int64, name string) error {
	return s.tx(ctx, func(tx *sql.Tx) error {
		if name != "" {
			var id int64
			err := tx.QueryRowContext(ctx, `SELECT id FROM sieve_scripts WHERE account_id = ? AND name = ?`, accountID, name).Scan(&id)
			if errors.Is(err, sql.ErrNoRows) {
				return ErrNotFound
			}
			if err != nil {
				return err
			}
		}
		if _, err := tx.ExecContext(ctx, `UPDATE sieve_scripts SET active = 0 WHERE account_id = ? AND active = 1`, accountID); err != nil {
			return err
		}
		if name == "" {
			return nil
		}
		_, err := tx.ExecContext(ctx, `UPDATE sieve_scripts SET active = 1 WHERE account_id = ? AND name = ?`, accountID, name)
		return err
	})
}

// DeleteSieveScript removes a script; the active script cannot be deleted.
func (s *Store) DeleteSieveScript(ctx context.Context, accountID int64, name string) error {
	return s.tx(ctx, func(tx *sql.Tx) error {
		var active bool
		err := tx.QueryRowContext(ctx, `SELECT active FROM sieve_scripts WHERE account_id = ? AND name = ?`, accountID, name).Scan(&active)
		if errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		if active {
			return ErrSieveActive
		}
		_, err = tx.ExecContext(ctx, `DELETE FROM sieve_scripts WHERE account_id = ? AND name = ?`, accountID, name)
		return err
	})
}

// RenameSieveScript renames a script (ErrNotFound, ErrConflict).
func (s *Store) RenameSieveScript(ctx context.Context, accountID int64, oldName, newName string) error {
	if !ValidSieveName(newName) {
		return ErrSieveName
	}
	return s.tx(ctx, func(tx *sql.Tx) error {
		var n int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM sieve_scripts WHERE account_id = ? AND name = ?`, accountID, newName).Scan(&n); err != nil {
			return err
		}
		if n > 0 && oldName != newName {
			return ErrConflict
		}
		res, err := tx.ExecContext(ctx, `UPDATE sieve_scripts SET name = ?, updated_at = ? WHERE account_id = ? AND name = ?`, newName, now(), accountID, oldName)
		if err != nil {
			return err
		}
		if c, _ := res.RowsAffected(); c == 0 {
			return ErrNotFound
		}
		return nil
	})
}

// SieveVacationShouldReply records a Sieve vacation reply under key (handle
// and sender) and reports whether one is due, at most once per days. It
// shares the vacation_replies table with the account vacation notice.
func (s *Store) SieveVacationShouldReply(ctx context.Context, accountID int64, key string, days int) bool {
	var sent int64
	err := s.DB.QueryRowContext(ctx, `SELECT sent_at FROM vacation_replies WHERE account_id = ? AND sender = ?`, accountID, key).Scan(&sent)
	if err == nil && time.Since(ts(sent)) < time.Duration(days)*24*time.Hour {
		return false
	}
	_, _ = s.DB.ExecContext(ctx, `INSERT INTO vacation_replies(account_id, sender, sent_at) VALUES (?,?,?)
		ON CONFLICT(account_id, sender) DO UPDATE SET sent_at = excluded.sent_at`, accountID, key, now())
	return true
}

// MailboxByName finds a mailbox of the account by name. "INBOX" (any case)
// and the inbox's own name select the inbox; other names match exactly,
// then case-insensitively.
func (s *Store) MailboxByName(ctx context.Context, accountID int64, name string) (int64, error) {
	if strings.EqualFold(name, "INBOX") {
		return s.MailboxByRole(ctx, accountID, "inbox")
	}
	var id int64
	err := s.DB.QueryRowContext(ctx, `SELECT id FROM mailboxes WHERE account_id = ? AND name = ?`, accountID, name).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		err = s.DB.QueryRowContext(ctx, `SELECT id FROM mailboxes WHERE account_id = ? AND lower(name) = lower(?) ORDER BY id LIMIT 1`, accountID, name).Scan(&id)
	}
	if errors.Is(err, sql.ErrNoRows) {
		return 0, ErrNotFound
	}
	return id, err
}
