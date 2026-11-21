package store

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"
)

// MailEncryption is an account's opt-in to encrypted mail storage: incoming
// messages are encrypted with PublicKey before they are written.
type MailEncryption struct {
	AccountID   int64      `json:"-"`
	Kind        string     `json:"kind"` // pgp | smime
	PublicKey   string     `json:"publicKey"`
	Fingerprint string     `json:"fingerprint"`
	Description string     `json:"description"`
	Algorithm   string     `json:"algorithm"`
	Expires     *time.Time `json:"expires"`
	HideSubject bool       `json:"hideSubject"`
	CreatedAt   time.Time  `json:"createdAt"`
	UpdatedAt   time.Time  `json:"updatedAt"`
}

// GetMailEncryption returns the account's configuration or ErrNotFound.
func (s *Store) GetMailEncryption(ctx context.Context, accountID int64) (*MailEncryption, error) {
	var e MailEncryption
	var exp sql.NullInt64
	var created, updated int64
	err := s.DB.QueryRowContext(ctx, `SELECT account_id, kind, public_key, fingerprint, description, algorithm, expires_at, hide_subject, created_at, updated_at
		FROM mail_encryption WHERE account_id = ?`, accountID).Scan(&e.AccountID, &e.Kind, &e.PublicKey, &e.Fingerprint, &e.Description, &e.Algorithm, &exp, &e.HideSubject, &created, &updated)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	e.Expires = nullTime(exp)
	e.CreatedAt, e.UpdatedAt = ts(created), ts(updated)
	return &e, nil
}

// SetMailEncryption creates or replaces the account's configuration.
func (s *Store) SetMailEncryption(ctx context.Context, e *MailEncryption) error {
	var exp any
	if e.Expires != nil {
		exp = e.Expires.Unix()
	}
	_, err := s.DB.ExecContext(ctx, `INSERT INTO mail_encryption(account_id, kind, public_key, fingerprint, description, algorithm, expires_at, hide_subject, created_at, updated_at)
		VALUES (?,?,?,?,?,?,?,?,?,?)
		ON CONFLICT(account_id) DO UPDATE SET kind = excluded.kind, public_key = excluded.public_key, fingerprint = excluded.fingerprint,
		description = excluded.description, algorithm = excluded.algorithm, expires_at = excluded.expires_at, hide_subject = excluded.hide_subject,
		updated_at = excluded.updated_at`,
		e.AccountID, e.Kind, e.PublicKey, e.Fingerprint, e.Description, e.Algorithm, exp, e.HideSubject, now(), now())
	return err
}

// DeleteMailEncryption turns encrypted storage off (already encrypted
// messages stay encrypted).
func (s *Store) DeleteMailEncryption(ctx context.Context, accountID int64) error {
	_, err := s.DB.ExecContext(ctx, `DELETE FROM mail_encryption WHERE account_id = ?`, accountID)
	return err
}

// MessageRef identifies a stored message and its blob.
type MessageRef struct {
	ID       int64
	BlobHash string
}

// EncryptionCandidates lists the account's messages outside the drafts
// folder (drafts stay editable and are never encrypted).
func (s *Store) EncryptionCandidates(ctx context.Context, accountID int64) ([]MessageRef, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT m.id, m.blob_hash FROM messages m JOIN mailboxes b ON b.id = m.mailbox_id
		WHERE m.account_id = ? AND (b.role IS NULL OR b.role != 'drafts') ORDER BY m.id`, accountID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []MessageRef
	for rows.Next() {
		var r MessageRef
		if err := rows.Scan(&r.ID, &r.BlobHash); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// EncryptedContent is the metadata of a message after its blob was
// replaced by an encrypted version.
type EncryptedContent struct {
	BlobHash   string
	Size       int64
	Subject    string
	SearchText string
}

// ReplaceMessageContent swaps a message's blob for its encrypted form. The
// message gets a new UID (IMAP clients must not keep the old content under
// the same UID), previews, AI results and embeddings derived from the
// plaintext are removed, and the old blob is released.
func (s *Store) ReplaceMessageContent(ctx context.Context, accountID, id int64, c EncryptedContent) error {
	var oldHash string
	err := s.tx(ctx, func(tx *sql.Tx) error {
		var mailboxID int64
		if err := tx.QueryRowContext(ctx, `SELECT mailbox_id, blob_hash FROM messages WHERE id = ? AND account_id = ?`, id, accountID).Scan(&mailboxID, &oldHash); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return ErrNotFound
			}
			return err
		}
		var uid int64
		if err := tx.QueryRowContext(ctx, `UPDATE mailboxes SET uid_next = uid_next + 1 WHERE id = ? RETURNING uid_next - 1`, mailboxID).Scan(&uid); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE messages SET blob_hash = ?, size = ?, uid = ?, subject = ?, preview = '', search_text = ?,
			has_attachments = 0, ai_summary = NULL, ai_reasons_json = '[]', ai_events_json = '[]', ai_tasks_json = '[]', ai_done = 1
			WHERE id = ?`, c.BlobHash, c.Size, uid, c.Subject, strings.ToLower(c.SearchText), id); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, `DELETE FROM embeddings WHERE message_id = ?`, id)
		return err
	})
	if err != nil {
		return err
	}
	if oldHash != c.BlobHash {
		s.releaseBlob(ctx, oldHash)
	}
	s.notify(accountID)
	return nil
}
