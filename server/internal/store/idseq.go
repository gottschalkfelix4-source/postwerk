package store

import (
	"context"
	"database/sql"
	"fmt"
)

// Object ids that clients cache (JMAP ids of mailboxes, emails, threads and
// submissions, account ids, DAV collection ids in sync tokens) must never be
// handed out twice (RFC 8620 §1.2). SQLite reuses the rowid of the most
// recently deleted row when a table has no AUTOINCREMENT, so these tables get
// explicit ids from a per-table high-water mark in id_seq instead.

// seqTables lists the tables whose ids are assigned via nextID.
var seqTables = map[string]bool{
	"messages":         true,
	"mailboxes":        true,
	"accounts":         true,
	"jmap_submissions": true,
	"dav_collections":  true,
}

// nextID reserves a fresh id for table inside tx. The counter never goes
// below the current maximum id, so rows inserted without nextID (or before
// the counter existed) are never collided with.
func nextID(ctx context.Context, tx *sql.Tx, table string) (int64, error) {
	if !seqTables[table] {
		return 0, fmt.Errorf("nextID: unknown table %q", table)
	}
	var id int64
	err := tx.QueryRowContext(ctx, `INSERT INTO id_seq(name, last) VALUES (?, (SELECT COALESCE(MAX(id), 0) FROM `+table+`) + 1)
		ON CONFLICT(name) DO UPDATE SET last = MAX(id_seq.last, (SELECT COALESCE(MAX(id), 0) FROM `+table+`)) + 1
		RETURNING last`, table).Scan(&id)
	return id, err
}

// idSeqMigration creates the high-water marks, seeded from the highest id
// still present and, for JMAP objects, the highest id the change log has
// ever reported (rows deleted before the upgrade).
const idSeqMigration = `CREATE TABLE id_seq (name TEXT PRIMARY KEY, last INTEGER NOT NULL);
	INSERT INTO id_seq(name, last) SELECT 'messages', MAX(
		(SELECT COALESCE(MAX(id), 0) FROM messages),
		(SELECT COALESCE(MAX(obj_id), 0) FROM jmap_changes WHERE type IN ('Email', 'Thread')),
		(SELECT COALESCE(MAX(thread_id), 0) FROM messages));
	INSERT INTO id_seq(name, last) SELECT 'mailboxes', MAX(
		(SELECT COALESCE(MAX(id), 0) FROM mailboxes),
		(SELECT COALESCE(MAX(obj_id), 0) FROM jmap_changes WHERE type = 'Mailbox'));
	INSERT INTO id_seq(name, last) SELECT 'jmap_submissions', MAX(
		(SELECT COALESCE(MAX(id), 0) FROM jmap_submissions),
		(SELECT COALESCE(MAX(obj_id), 0) FROM jmap_changes WHERE type = 'EmailSubmission'));
	INSERT INTO id_seq(name, last) SELECT 'accounts', COALESCE(MAX(id), 0) FROM accounts;
	INSERT INTO id_seq(name, last) SELECT 'dav_collections', COALESCE(MAX(id), 0) FROM dav_collections;`
