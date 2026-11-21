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

	"postwerk/internal/vault"
)

var ErrNotFound = errors.New("not found")
var ErrConflict = errors.New("conflict")

type Store struct {
	DB    *sql.DB
	Blobs *BlobStore
	// Vault is the master key protecting secrets at rest.
	Vault *vault.Key

	changes changeHub
	// settings is set by LoadSettings (postmaster routing reads it).
	settings *SettingsCache
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
	if err := migrateLegacyCalendar(context.Background(), db); err != nil {
		return nil, fmt.Errorf("migrate calendar: %w", err)
	}
	blobs, err := NewBlobStore(filepath.Join(dataDir, "blobs"))
	if err != nil {
		return nil, err
	}
	key, err := vault.Load(dataDir)
	if err != nil {
		db.Close()
		return nil, fmt.Errorf("master key: %w", err)
	}
	s := &Store{DB: db, Blobs: blobs, Vault: key}
	if err := s.initSecrets(context.Background()); err != nil {
		db.Close()
		return nil, err
	}
	return s, nil
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
	// IMAP: \Deleted flag, custom keywords, subscriptions, UID lookups.
	`ALTER TABLE messages ADD COLUMN deleted INTEGER NOT NULL DEFAULT 0;
	ALTER TABLE messages ADD COLUMN keywords_json TEXT NOT NULL DEFAULT '[]';
	ALTER TABLE mailboxes ADD COLUMN subscribed INTEGER NOT NULL DEFAULT 1;
	CREATE INDEX messages_mailbox_uid ON messages(mailbox_id, uid);`,
	`CREATE TABLE backup_runs (
		id INTEGER PRIMARY KEY,
		trigger TEXT NOT NULL,
		status TEXT NOT NULL,
		archive TEXT NOT NULL DEFAULT '',
		target TEXT NOT NULL DEFAULT '',
		size INTEGER NOT NULL DEFAULT 0,
		files INTEGER NOT NULL DEFAULT 0,
		error TEXT NOT NULL DEFAULT '',
		started_at INTEGER NOT NULL,
		finished_at INTEGER
	);`,
	// Login security: session metadata and idle timeout, forced password
	// change, TOTP replay protection, recovery codes and persisted bans.
	`ALTER TABLE sessions ADD COLUMN last_used_at INTEGER;
	ALTER TABLE sessions ADD COLUMN ip TEXT NOT NULL DEFAULT '';
	ALTER TABLE sessions ADD COLUMN user_agent TEXT NOT NULL DEFAULT '';
	CREATE INDEX sessions_account ON sessions(account_id);
	ALTER TABLE accounts ADD COLUMN must_change_password INTEGER NOT NULL DEFAULT 0;
	ALTER TABLE accounts ADD COLUMN totp_last_step INTEGER NOT NULL DEFAULT 0;
	CREATE TABLE recovery_codes (
		id INTEGER PRIMARY KEY,
		account_id INTEGER NOT NULL REFERENCES accounts(id) ON DELETE CASCADE,
		hash TEXT NOT NULL,
		created_at INTEGER NOT NULL
	);
	CREATE INDEX recovery_codes_account ON recovery_codes(account_id);
	CREATE TABLE auth_bans (
		key TEXT PRIMARY KEY,
		reason TEXT NOT NULL,
		until INTEGER NOT NULL,
		failures INTEGER NOT NULL
	);`,
	// JMAP: per-account modseq with a change log for Mailbox/Email/Thread
	// state, uploaded blobs and email submissions.
	`CREATE TABLE jmap_state (
		account_id INTEGER PRIMARY KEY REFERENCES accounts(id) ON DELETE CASCADE,
		modseq INTEGER NOT NULL DEFAULT 0
	);
	CREATE TABLE jmap_changes (
		account_id INTEGER NOT NULL REFERENCES accounts(id) ON DELETE CASCADE,
		type TEXT NOT NULL,
		obj_id INTEGER NOT NULL,
		modseq INTEGER NOT NULL,
		created_modseq INTEGER NOT NULL DEFAULT 0,
		destroyed INTEGER NOT NULL DEFAULT 0,
		changed_at INTEGER NOT NULL,
		PRIMARY KEY (account_id, type, obj_id)
	);
	CREATE INDEX jmap_changes_seq ON jmap_changes(account_id, type, modseq);
	CREATE TABLE jmap_floors (
		account_id INTEGER NOT NULL REFERENCES accounts(id) ON DELETE CASCADE,
		type TEXT NOT NULL,
		floor INTEGER NOT NULL,
		PRIMARY KEY (account_id, type)
	);
	CREATE TABLE jmap_uploads (
		account_id INTEGER NOT NULL REFERENCES accounts(id) ON DELETE CASCADE,
		blob_hash TEXT NOT NULL,
		type TEXT NOT NULL,
		size INTEGER NOT NULL,
		created_at INTEGER NOT NULL,
		PRIMARY KEY (account_id, blob_hash)
	);
	CREATE INDEX jmap_uploads_hash ON jmap_uploads(blob_hash);
	CREATE TABLE jmap_submissions (
		id INTEGER PRIMARY KEY,
		account_id INTEGER NOT NULL REFERENCES accounts(id) ON DELETE CASCADE,
		identity_id TEXT NOT NULL,
		email_id INTEGER NOT NULL,
		thread_id INTEGER NOT NULL,
		envelope_json TEXT NOT NULL,
		send_at INTEGER NOT NULL
	);
	CREATE INDEX jmap_submissions_account ON jmap_submissions(account_id);`,
	`ALTER TABLE accounts ADD COLUMN source TEXT NOT NULL DEFAULT 'local';
	ALTER TABLE sessions ADD COLUMN method TEXT NOT NULL DEFAULT 'password';`,
	// Encryption at rest: per-account public key for stored mail, and
	// GDPR data exports.
	`CREATE TABLE mail_encryption (
		account_id INTEGER PRIMARY KEY REFERENCES accounts(id) ON DELETE CASCADE,
		kind TEXT NOT NULL,
		public_key TEXT NOT NULL,
		fingerprint TEXT NOT NULL DEFAULT '',
		description TEXT NOT NULL DEFAULT '',
		algorithm TEXT NOT NULL DEFAULT '',
		expires_at INTEGER,
		hide_subject INTEGER NOT NULL DEFAULT 0,
		created_at INTEGER NOT NULL,
		updated_at INTEGER NOT NULL
	);
	CREATE TABLE data_exports (
		id INTEGER PRIMARY KEY,
		account_id INTEGER NOT NULL REFERENCES accounts(id) ON DELETE CASCADE,
		requested_by INTEGER NOT NULL,
		status TEXT NOT NULL,
		phase TEXT NOT NULL DEFAULT '',
		done INTEGER NOT NULL DEFAULT 0,
		total INTEGER NOT NULL DEFAULT 0,
		file TEXT NOT NULL DEFAULT '',
		size INTEGER NOT NULL DEFAULT 0,
		error TEXT NOT NULL DEFAULT '',
		created_at INTEGER NOT NULL,
		finished_at INTEGER,
		expires_at INTEGER
	);
	CREATE INDEX data_exports_account ON data_exports(account_id, created_at DESC);`,
	// Mail authentication: several DKIM keys per domain (rotation, RSA and
	// Ed25519) with the existing key migrated as active RSA key, and stored
	// DMARC aggregate and TLS-RPT reports.
	`CREATE TABLE dkim_keys (
		id INTEGER PRIMARY KEY,
		domain TEXT NOT NULL REFERENCES domains(name) ON DELETE CASCADE,
		selector TEXT NOT NULL,
		algorithm TEXT NOT NULL,
		private_key TEXT NOT NULL,
		state TEXT NOT NULL,
		created_at INTEGER NOT NULL,
		activated_at INTEGER,
		retired_at INTEGER,
		UNIQUE(domain, selector)
	);
	INSERT INTO dkim_keys(domain, selector, algorithm, private_key, state, created_at, activated_at)
		SELECT name, dkim_selector, 'rsa', dkim_private_key, 'active', created_at, created_at FROM domains WHERE dkim_private_key <> '';
	CREATE TABLE dmarc_reports (
		id INTEGER PRIMARY KEY,
		domain TEXT NOT NULL,
		org_name TEXT NOT NULL,
		email TEXT NOT NULL DEFAULT '',
		report_id TEXT NOT NULL,
		date_begin INTEGER NOT NULL,
		date_end INTEGER NOT NULL,
		policy_json TEXT NOT NULL DEFAULT '{}',
		messages INTEGER NOT NULL DEFAULT 0,
		failed INTEGER NOT NULL DEFAULT 0,
		received_at INTEGER NOT NULL,
		UNIQUE(org_name, report_id)
	);
	CREATE INDEX dmarc_reports_domain ON dmarc_reports(domain, date_end);
	CREATE TABLE dmarc_records (
		id INTEGER PRIMARY KEY,
		report_id INTEGER NOT NULL REFERENCES dmarc_reports(id) ON DELETE CASCADE,
		source_ip TEXT NOT NULL,
		count INTEGER NOT NULL,
		disposition TEXT NOT NULL DEFAULT '',
		dkim TEXT NOT NULL DEFAULT '',
		spf TEXT NOT NULL DEFAULT '',
		header_from TEXT NOT NULL DEFAULT '',
		envelope_from TEXT NOT NULL DEFAULT '',
		auth_json TEXT NOT NULL DEFAULT '{}'
	);
	CREATE INDEX dmarc_records_report ON dmarc_records(report_id);
	CREATE TABLE tls_reports (
		id INTEGER PRIMARY KEY,
		domain TEXT NOT NULL,
		org_name TEXT NOT NULL,
		email TEXT NOT NULL DEFAULT '',
		report_id TEXT NOT NULL,
		date_begin INTEGER NOT NULL,
		date_end INTEGER NOT NULL,
		policy_type TEXT NOT NULL DEFAULT '',
		policy_json TEXT NOT NULL DEFAULT '{}',
		success_count INTEGER NOT NULL DEFAULT 0,
		failure_count INTEGER NOT NULL DEFAULT 0,
		failures_json TEXT NOT NULL DEFAULT '[]',
		received_at INTEGER NOT NULL,
		UNIQUE(org_name, report_id, domain, policy_type)
	);
	CREATE INDEX tls_reports_domain ON tls_reports(domain, date_end);`,
	// Sieve scripts (RFC 5228); at most one active script per account.
	`CREATE TABLE sieve_scripts (
		id INTEGER PRIMARY KEY,
		account_id INTEGER NOT NULL REFERENCES accounts(id) ON DELETE CASCADE,
		name TEXT NOT NULL,
		content TEXT NOT NULL,
		active INTEGER NOT NULL DEFAULT 0,
		created_at INTEGER NOT NULL,
		updated_at INTEGER NOT NULL,
		UNIQUE(account_id, name)
	);
	CREATE UNIQUE INDEX sieve_scripts_active ON sieve_scripts(account_id) WHERE active = 1;`,
	// CalDAV/CardDAV: calendars and address books with iCalendar/vCard
	// objects, sync sequence numbers and tombstones for sync-collection.
	`CREATE TABLE dav_collections (
		id INTEGER PRIMARY KEY,
		account_id INTEGER NOT NULL REFERENCES accounts(id) ON DELETE CASCADE,
		kind TEXT NOT NULL,
		slug TEXT NOT NULL,
		name TEXT NOT NULL,
		description TEXT NOT NULL DEFAULT '',
		color TEXT NOT NULL DEFAULT '',
		components TEXT NOT NULL DEFAULT '',
		sync_seq INTEGER NOT NULL DEFAULT 1,
		created_at INTEGER NOT NULL,
		UNIQUE(account_id, kind, slug)
	);
	CREATE TABLE calendar_objects (
		id INTEGER PRIMARY KEY,
		collection_id INTEGER NOT NULL REFERENCES dav_collections(id) ON DELETE CASCADE,
		account_id INTEGER NOT NULL REFERENCES accounts(id) ON DELETE CASCADE,
		name TEXT NOT NULL,
		uid TEXT NOT NULL,
		component TEXT NOT NULL,
		data BLOB NOT NULL,
		etag TEXT NOT NULL,
		modseq INTEGER NOT NULL,
		summary TEXT NOT NULL DEFAULT '',
		start_ts INTEGER,
		end_ts INTEGER,
		source_message_id INTEGER,
		created_at INTEGER NOT NULL,
		updated_at INTEGER NOT NULL,
		UNIQUE(collection_id, name),
		UNIQUE(collection_id, uid)
	);
	CREATE INDEX calendar_objects_account ON calendar_objects(account_id, component, start_ts);
	CREATE INDEX calendar_objects_modseq ON calendar_objects(collection_id, modseq);
	CREATE TABLE address_objects (
		id INTEGER PRIMARY KEY,
		collection_id INTEGER NOT NULL REFERENCES dav_collections(id) ON DELETE CASCADE,
		account_id INTEGER NOT NULL REFERENCES accounts(id) ON DELETE CASCADE,
		name TEXT NOT NULL,
		uid TEXT NOT NULL,
		data BLOB NOT NULL,
		etag TEXT NOT NULL,
		modseq INTEGER NOT NULL,
		fn TEXT NOT NULL DEFAULT '',
		emails TEXT NOT NULL DEFAULT '',
		search TEXT NOT NULL DEFAULT '',
		created_at INTEGER NOT NULL,
		updated_at INTEGER NOT NULL,
		UNIQUE(collection_id, name),
		UNIQUE(collection_id, uid)
	);
	CREATE INDEX address_objects_account ON address_objects(account_id, fn);
	CREATE INDEX address_objects_modseq ON address_objects(collection_id, modseq);
	CREATE TABLE dav_tombstones (
		collection_id INTEGER NOT NULL REFERENCES dav_collections(id) ON DELETE CASCADE,
		name TEXT NOT NULL,
		modseq INTEGER NOT NULL,
		PRIMARY KEY(collection_id, name)
	);`,
	// Outbound send limits (sliding windows, per-account overrides, hits)
	// and the state of operational notifications.
	`CREATE TABLE send_log (
		id INTEGER PRIMARY KEY,
		account_id INTEGER NOT NULL,
		at INTEGER NOT NULL,
		rcpts INTEGER NOT NULL
	);
	CREATE INDEX send_log_account ON send_log(account_id, at);
	CREATE TABLE send_limit_hits (
		id INTEGER PRIMARY KEY,
		account_id INTEGER NOT NULL,
		at INTEGER NOT NULL,
		kind TEXT NOT NULL
	);
	CREATE INDEX send_limit_hits_at ON send_limit_hits(at);
	CREATE TABLE account_send_limits (
		account_id INTEGER PRIMARY KEY REFERENCES accounts(id) ON DELETE CASCADE,
		max_rcpts_msg INTEGER,
		max_msgs_hour INTEGER,
		max_rcpts_day INTEGER,
		exempt INTEGER NOT NULL DEFAULT 0
	);
	CREATE TABLE notify_state (
		key TEXT PRIMARY KEY,
		severity TEXT NOT NULL,
		title TEXT NOT NULL,
		first_seen INTEGER NOT NULL,
		last_seen INTEGER NOT NULL,
		last_notified INTEGER NOT NULL
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
