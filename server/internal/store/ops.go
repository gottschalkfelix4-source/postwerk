package store

import (
	"context"
	"database/sql"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// Operational helpers: housekeeping, queue insight, key/value cache.
//
// The extra table and index are created on first use with IF NOT EXISTS so
// they do not depend on the numbered schema migrations.

var opsSchema sync.Map // *sql.DB -> *sync.Once

func (s *Store) ensureOps(ctx context.Context) error {
	v, _ := opsSchema.LoadOrStore(s.DB, &sync.Once{})
	var err error
	v.(*sync.Once).Do(func() {
		_, err = s.DB.ExecContext(ctx, `
			CREATE TABLE IF NOT EXISTS retention_seen (
				message_id INTEGER PRIMARY KEY,
				mailbox_id INTEGER NOT NULL,
				seen_at INTEGER NOT NULL
			);
			CREATE INDEX IF NOT EXISTS messages_blob ON messages(blob_hash);`)
	})
	if err != nil {
		opsSchema.Delete(s.DB)
	}
	return err
}

// DeleteAccountAppPasswords revokes all app passwords of an account.
func (s *Store) DeleteAccountAppPasswords(ctx context.Context, accountID int64) error {
	_, err := s.DB.ExecContext(ctx, `DELETE FROM app_passwords WHERE account_id = ?`, accountID)
	return err
}

// ---- key/value ----

// KV returns a value stored with SetKV ("" if missing).
func (s *Store) KV(ctx context.Context, key string) (string, error) {
	var v string
	err := s.DB.QueryRowContext(ctx, `SELECT value FROM settings WHERE key = ?`, "kv:"+key).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return v, err
}

// SetKV stores a small value; an empty value deletes the key.
func (s *Store) SetKV(ctx context.Context, key, value string) error {
	if value == "" {
		_, err := s.DB.ExecContext(ctx, `DELETE FROM settings WHERE key = ?`, "kv:"+key)
		return err
	}
	_, err := s.DB.ExecContext(ctx, `INSERT INTO settings(key, value) VALUES (?, ?) ON CONFLICT(key) DO UPDATE SET value = excluded.value`, "kv:"+key, value)
	return err
}

// DeleteKVPrefix removes all keys starting with prefix whose suffix is not in keep.
func (s *Store) DeleteKVPrefix(ctx context.Context, prefix string, keep map[string]bool) (int, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT key FROM settings WHERE key LIKE ? ESCAPE '\'`, likePrefix("kv:"+prefix))
	if err != nil {
		return 0, err
	}
	var del []string
	for rows.Next() {
		var k string
		if rows.Scan(&k) == nil && !keep[strings.TrimPrefix(k, "kv:"+prefix)] {
			del = append(del, k)
		}
	}
	rows.Close()
	for _, k := range del {
		if _, err := s.DB.ExecContext(ctx, `DELETE FROM settings WHERE key = ?`, k); err != nil {
			return 0, err
		}
	}
	return len(del), nil
}

func likePrefix(p string) string {
	r := strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`)
	return r.Replace(p) + "%"
}

// ---- queue insight ----

// OldestQueued returns the creation time of the oldest queued message.
func (s *Store) OldestQueued(ctx context.Context) (time.Time, bool) {
	var t sql.NullInt64
	if err := s.DB.QueryRowContext(ctx, `SELECT MIN(created_at) FROM queue`).Scan(&t); err != nil || !t.Valid {
		return time.Time{}, false
	}
	return ts(t.Int64), true
}

// QueueIDs returns the IDs of all queued messages.
func (s *Store) QueueIDs(ctx context.Context) (map[int64]bool, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT id FROM queue`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[int64]bool{}
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out[id] = true
	}
	return out, rows.Err()
}

// ---- backup insight ----

// LastBackupRun returns the newest finished run and the newest successful one.
func (s *Store) LastBackupRun(ctx context.Context) (last *BackupRun, lastSuccess time.Time, err error) {
	runs, err := s.BackupRuns(ctx, 1)
	if err != nil {
		return nil, time.Time{}, err
	}
	if len(runs) > 0 {
		last = &runs[0]
	}
	var t sql.NullInt64
	if err := s.DB.QueryRowContext(ctx, `SELECT MAX(finished_at) FROM backup_runs WHERE status = 'success'`).Scan(&t); err == nil && t.Valid {
		lastSuccess = ts(t.Int64)
	}
	return last, lastSuccess, nil
}

// ---- housekeeping ----

// PurgeOldMailboxMessages deletes messages that have been in mailboxes with
// the given role (trash, junk) for longer than maxAge. The time is counted
// from when housekeeping first saw the message there. At most limit messages
// are deleted per call.
func (s *Store) PurgeOldMailboxMessages(ctx context.Context, role string, maxAge time.Duration, limit int) (int, error) {
	if err := s.ensureOps(ctx); err != nil {
		return 0, err
	}
	// Forget messages that left such a mailbox (moved or deleted).
	if _, err := s.DB.ExecContext(ctx, `DELETE FROM retention_seen WHERE NOT EXISTS (
		SELECT 1 FROM messages m WHERE m.id = retention_seen.message_id AND m.mailbox_id = retention_seen.mailbox_id)`); err != nil {
		return 0, err
	}
	if _, err := s.DB.ExecContext(ctx, `INSERT OR IGNORE INTO retention_seen(message_id, mailbox_id, seen_at)
		SELECT m.id, m.mailbox_id, ? FROM messages m JOIN mailboxes b ON b.id = m.mailbox_id WHERE b.role = ?`, now(), role); err != nil {
		return 0, err
	}
	cutoff := time.Now().Add(-maxAge).Unix()
	rows, err := s.DB.QueryContext(ctx, `SELECT m.account_id, m.id FROM retention_seen r
		JOIN messages m ON m.id = r.message_id AND m.mailbox_id = r.mailbox_id
		JOIN mailboxes b ON b.id = m.mailbox_id
		WHERE b.role = ? AND r.seen_at < ? LIMIT ?`, role, cutoff, limit)
	if err != nil {
		return 0, err
	}
	type ref struct{ account, id int64 }
	var del []ref
	for rows.Next() {
		var r ref
		if rows.Scan(&r.account, &r.id) == nil {
			del = append(del, r)
		}
	}
	rows.Close()
	n := 0
	for _, r := range del {
		if ctx.Err() != nil {
			break
		}
		if err := s.DeleteMessage(ctx, r.account, r.id); err != nil && !errors.Is(err, ErrNotFound) {
			return n, err
		}
		_, _ = s.DB.ExecContext(ctx, `DELETE FROM retention_seen WHERE message_id = ?`, r.id)
		n++
	}
	return n, nil
}

// PurgeVacationReplies forgets vacation auto-replies older than maxAge.
func (s *Store) PurgeVacationReplies(ctx context.Context, maxAge time.Duration) (int64, error) {
	res, err := s.DB.ExecContext(ctx, `DELETE FROM vacation_replies WHERE sent_at < ?`, time.Now().Add(-maxAge).Unix())
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// blobReferenced reports whether a message or queue entry uses the blob.
func (s *Store) blobReferenced(ctx context.Context, hash string) (bool, error) {
	var n int
	err := s.DB.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM messages WHERE blob_hash = ?) OR EXISTS(SELECT 1 FROM queue WHERE blob_hash = ?)
		OR EXISTS(SELECT 1 FROM jmap_uploads WHERE blob_hash = ?)`, hash, hash, hash).Scan(&n)
	return n != 0, err
}

// PurgeOrphanBlobs deletes blob files that no message or queue entry
// references and that are older than minAge (so blobs written just before
// their row is inserted are never touched). Leftover temp files older than
// minAge are removed as well.
func (s *Store) PurgeOrphanBlobs(ctx context.Context, minAge time.Duration) (files int, bytes int64, err error) {
	if err := s.ensureOps(ctx); err != nil {
		return 0, 0, err
	}
	cutoff := time.Now().Add(-minAge)
	root := s.Blobs.dir
	walkErr := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil // skip unreadable entries
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if d.IsDir() {
			return nil
		}
		info, err := d.Info()
		if err != nil || info.ModTime().After(cutoff) {
			return nil
		}
		rel, err := filepath.Rel(root, p)
		if err != nil {
			return nil
		}
		parts := strings.Split(filepath.ToSlash(rel), "/")
		if len(parts) != 2 {
			return nil
		}
		if strings.HasSuffix(parts[1], ".tmp") {
			if os.Remove(p) == nil {
				files++
				bytes += info.Size()
			}
			return nil
		}
		hash := parts[0] + parts[1]
		if len(parts[0]) != 2 || !isHex(hash) {
			return nil
		}
		used, err := s.blobReferenced(ctx, hash)
		if err != nil || used {
			return err
		}
		if os.Remove(p) == nil {
			files++
			bytes += info.Size()
		}
		return nil
	})
	return files, bytes, walkErr
}

func isHex(s string) bool {
	for _, c := range s {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return s != ""
}
