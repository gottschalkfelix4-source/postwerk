// Package store persists everything Postwerk knows: directory, messages,
// queue, settings and statistics. Metadata lives in SQLite, raw messages in a
// content-addressed blob directory.
package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	_ "modernc.org/sqlite"
)

var ErrNotFound = errors.New("not found")
var ErrConflict = errors.New("conflict")

type Store struct {
	DB    *sql.DB
	Blobs *BlobStore
}

func Open(dataDir string) (*Store, error) {
	if err := os.MkdirAll(dataDir, 0o750); err != nil {
		return nil, err
	}
	dsn := "file:" + filepath.ToSlash(filepath.Join(dataDir, "postwerk.db")) +
		"?_pragma=journal_mode(WAL)&_pragma=busy_timeout(10000)&_pragma=foreign_keys(1)&_pragma=synchronous(NORMAL)&_txlock=immediate"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(8)
	db.SetConnMaxIdleTime(5 * time.Minute)
	if err := migrate(db); err != nil {
		return nil, fmt.Errorf("migrate: %w", err)
	}
	blobs, err := NewBlobStore(filepath.Join(dataDir, "blobs"))
	if err != nil {
		return nil, err
	}
	return &Store{DB: db, Blobs: blobs}, nil
}

func (s *Store) Close() error { return s.DB.Close() }

func (s *Store) tx(ctx context.Context, fn func(*sql.Tx) error) error {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	if err := fn(tx); err != nil {
		_ = tx.Rollback()
		return err
	}
	return tx.Commit()
}

var migrations = []string{
	`CREATE TABLE settings (key TEXT PRIMARY KEY, value TEXT NOT NULL);

	CREATE TABLE domains (
		name TEXT PRIMARY KEY,
		catch_all TEXT,
		dkim_selector TEXT NOT NULL,
		dkim_private_key TEXT NOT NULL,
		created_at INTEGER NOT NULL
	);

	CREATE TABLE accounts (
		id INTEGER PRIMARY KEY,
		email TEXT NOT NULL UNIQUE,
		name TEXT NOT NULL DEFAULT '',
		password_hash TEXT NOT NULL,
		role TEXT NOT NULL DEFAULT 'user',
		quota_bytes INTEGER NOT NULL DEFAULT 0,
		disabled INTEGER NOT NULL DEFAULT 0,
		totp_secret TEXT NOT NULL DEFAULT '',
		totp_enabled INTEGER NOT NULL DEFAULT 0,
		created_at INTEGER NOT NULL
	);

	CREATE TABLE aliases (
		id INTEGER PRIMARY KEY,
		address TEXT NOT NULL UNIQUE,
		targets TEXT NOT NULL,
		created_at INTEGER NOT NULL
	);

	CREATE TABLE app_passwords (
		id INTEGER PRIMARY KEY,
		account_id INTEGER NOT NULL REFERENCES accounts(id) ON DELETE CASCADE,
		name TEXT NOT NULL,
		hash TEXT NOT NULL,
		created_at INTEGER NOT NULL,
		last_used_at INTEGER
	);

	CREATE TABLE sessions (
		token_hash TEXT PRIMARY KEY,
		account_id INTEGER NOT NULL REFERENCES accounts(id) ON DELETE CASCADE,
		created_at INTEGER NOT NULL,
		expires_at INTEGER NOT NULL
	);

	CREATE TABLE mailboxes (
		id INTEGER PRIMARY KEY,
		account_id INTEGER NOT NULL REFERENCES accounts(id) ON DELETE CASCADE,
		name TEXT NOT NULL,
		role TEXT,
		uid_next INTEGER NOT NULL DEFAULT 1,
		uid_validity INTEGER NOT NULL,
		created_at INTEGER NOT NULL,
		UNIQUE(account_id, name)
	);

	CREATE TABLE messages (
		id INTEGER PRIMARY KEY,
		account_id INTEGER NOT NULL REFERENCES accounts(id) ON DELETE CASCADE,
		mailbox_id INTEGER NOT NULL REFERENCES mailboxes(id) ON DELETE CASCADE,
		thread_id INTEGER NOT NULL DEFAULT 0,
		uid INTEGER NOT NULL,
		message_id TEXT NOT NULL DEFAULT '',
		in_reply_to TEXT NOT NULL DEFAULT '',
		subject TEXT NOT NULL DEFAULT '',
		from_name TEXT NOT NULL DEFAULT '',
		from_email TEXT NOT NULL DEFAULT '',
		to_json TEXT NOT NULL DEFAULT '[]',
		cc_json TEXT NOT NULL DEFAULT '[]',
		reply_to_json TEXT NOT NULL DEFAULT '[]',
		preview TEXT NOT NULL DEFAULT '',
		received_at INTEGER NOT NULL,
		size INTEGER NOT NULL,
		seen INTEGER NOT NULL DEFAULT 0,
		flagged INTEGER NOT NULL DEFAULT 0,
		answered INTEGER NOT NULL DEFAULT 0,
		draft INTEGER NOT NULL DEFAULT 0,
		has_attachments INTEGER NOT NULL DEFAULT 0,
		tags_json TEXT NOT NULL DEFAULT '[]',
		category TEXT,
		priority TEXT NOT NULL DEFAULT 'normal',
		spam_score REAL NOT NULL DEFAULT 0,
		auth_json TEXT NOT NULL DEFAULT '{}',
		virus TEXT,
		ai_summary TEXT,
		ai_reasons_json TEXT NOT NULL DEFAULT '[]',
		ai_events_json TEXT NOT NULL DEFAULT '[]',
		ai_tasks_json TEXT NOT NULL DEFAULT '[]',
		ai_done INTEGER NOT NULL DEFAULT 0,
		search_text TEXT NOT NULL DEFAULT '',
		blob_hash TEXT NOT NULL
	);
	CREATE INDEX messages_mailbox ON messages(mailbox_id, received_at DESC);
	CREATE INDEX messages_account ON messages(account_id, received_at DESC);
	CREATE INDEX messages_thread ON messages(account_id, thread_id);
	CREATE INDEX messages_msgid ON messages(account_id, message_id);

	CREATE TABLE embeddings (
		message_id INTEGER PRIMARY KEY REFERENCES messages(id) ON DELETE CASCADE,
		account_id INTEGER NOT NULL,
		vector BLOB NOT NULL
	);
	CREATE INDEX embeddings_account ON embeddings(account_id);

	CREATE TABLE queue (
		id INTEGER PRIMARY KEY,
		from_addr TEXT NOT NULL,
		rcpts_json TEXT NOT NULL,
		blob_hash TEXT NOT NULL,
		size INTEGER NOT NULL,
		attempts INTEGER NOT NULL DEFAULT 0,
		next_attempt INTEGER NOT NULL,
		last_error TEXT,
		created_at INTEGER NOT NULL
	);

	CREATE TABLE events (
		id INTEGER PRIMARY KEY,
		account_id INTEGER NOT NULL REFERENCES accounts(id) ON DELETE CASCADE,
		title TEXT NOT NULL,
		start TEXT NOT NULL,
		end_time TEXT,
		location TEXT,
		description TEXT,
		source_message_id INTEGER
	);

	CREATE TABLE tasks (
		id INTEGER PRIMARY KEY,
		account_id INTEGER NOT NULL REFERENCES accounts(id) ON DELETE CASCADE,
		title TEXT NOT NULL,
		due TEXT,
		done INTEGER NOT NULL DEFAULT 0,
		source_message_id INTEGER,
		created_at INTEGER NOT NULL
	);

	CREATE TABLE vacation (
		account_id INTEGER PRIMARY KEY REFERENCES accounts(id) ON DELETE CASCADE,
		enabled INTEGER NOT NULL DEFAULT 0,
		subject TEXT NOT NULL DEFAULT '',
		body TEXT NOT NULL DEFAULT '',
		from_date TEXT,
		until_date TEXT
	);

	CREATE TABLE vacation_replies (
		account_id INTEGER NOT NULL,
		sender TEXT NOT NULL,
		sent_at INTEGER NOT NULL,
		PRIMARY KEY(account_id, sender)
	);

	CREATE TABLE greylist (
		key TEXT PRIMARY KEY,
		first_seen INTEGER NOT NULL,
		last_seen INTEGER NOT NULL,
		passed INTEGER NOT NULL DEFAULT 0
	);

	CREATE TABLE bayes_tokens (
		token TEXT PRIMARY KEY,
		spam INTEGER NOT NULL DEFAULT 0,
		ham INTEGER NOT NULL DEFAULT 0
	);

	CREATE TABLE stats (
		hour INTEGER NOT NULL,
		metric TEXT NOT NULL,
		value INTEGER NOT NULL,
		PRIMARY KEY(hour, metric)
	);`,
	`CREATE TABLE migrations (
		id INTEGER PRIMARY KEY,
		name TEXT NOT NULL,
		source_json TEXT NOT NULL,
		options_json TEXT NOT NULL,
		status TEXT NOT NULL,
		accounts_json TEXT NOT NULL,
		log_json TEXT NOT NULL DEFAULT '[]',
		created_at INTEGER NOT NULL,
		started_at INTEGER,
		finished_at INTEGER
	);`,
}

func migrate(db *sql.DB) error {
	if _, err := db.Exec(`CREATE TABLE IF NOT EXISTS schema_version (version INTEGER NOT NULL)`); err != nil {
		return err
	}
	var v int
	if err := db.QueryRow(`SELECT COALESCE(MAX(version), 0) FROM schema_version`).Scan(&v); err != nil {
		return err
	}
	for i := v; i < len(migrations); i++ {
		tx, err := db.Begin()
		if err != nil {
			return err
		}
		if _, err := tx.Exec(migrations[i]); err != nil {
			tx.Rollback()
			return fmt.Errorf("migration %d: %w", i+1, err)
		}
		if _, err := tx.Exec(`INSERT INTO schema_version(version) VALUES (?)`, i+1); err != nil {
			tx.Rollback()
			return err
		}
		if err := tx.Commit(); err != nil {
			return err
		}
	}
	return nil
}

func now() int64 { return time.Now().Unix() }

func ts(unix int64) time.Time { return time.Unix(unix, 0).UTC() }

func nullTime(v sql.NullInt64) *time.Time {
	if !v.Valid {
		return nil
	}
	t := ts(v.Int64)
	return &t
}

func nullStr(v sql.NullString) *string {
	if !v.Valid {
		return nil
	}
	s := v.String
	return &s
}

func toNull(s *string) any {
	if s == nil || *s == "" {
		return nil
	}
	return *s
}
