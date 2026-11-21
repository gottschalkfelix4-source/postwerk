package store

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

// ---- change log (JMAP state, later IMAP CONDSTORE) ----
//
// Every mutation of a mailbox, message or thread bumps a per-account modseq
// and stores it with the changed object (one row per object, tombstones for
// destroyed objects). JMAP derives its per-type state strings and /changes
// responses from this log.

// Object types tracked in the change log.
const (
	TypeMailbox         = "Mailbox"
	TypeEmail           = "Email"
	TypeThread          = "Thread"
	TypeEmailDelivery   = "EmailDelivery"
	TypeEmailSubmission = "EmailSubmission"
)

// ErrCannotCalculateChanges is returned when the requested state is unknown
// or older than the pruned part of the change log.
var ErrCannotCalculateChanges = errors.New("cannot calculate changes")

type objChange struct {
	typ  string
	id   int64
	kind byte // 'c' created, 'u' updated, 'd' destroyed
}

type msgRef struct{ id, mailbox, thread int64 }

// logChanges assigns consecutive modseqs to the changes and records them.
// Change logging is best effort: a failure never fails the mutation itself.
func (s *Store) logChanges(ctx context.Context, accountID int64, changes []objChange) {
	if len(changes) == 0 {
		return
	}
	// Merge duplicates: destroyed wins over created, created over updated.
	type key struct {
		typ string
		id  int64
	}
	idx := map[key]int{}
	var list []objChange
	for _, c := range changes {
		k := key{c.typ, c.id}
		if i, ok := idx[k]; ok {
			if c.kind == 'd' || (c.kind == 'c' && list[i].kind == 'u') {
				list[i].kind = c.kind
			}
			continue
		}
		idx[k] = len(list)
		list = append(list, c)
	}
	// The mutation already happened; record it even if the caller gives up.
	ctx = context.WithoutCancel(ctx)
	_ = s.tx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `INSERT INTO jmap_state(account_id, modseq) VALUES (?, 0) ON CONFLICT(account_id) DO NOTHING`, accountID); err != nil {
			return err
		}
		var top int64
		if err := tx.QueryRowContext(ctx, `UPDATE jmap_state SET modseq = modseq + ? WHERE account_id = ? RETURNING modseq`, len(list), accountID).Scan(&top); err != nil {
			return err
		}
		seq := top - int64(len(list))
		t := now()
		for _, c := range list {
			seq++
			created := int64(0)
			if c.kind == 'c' {
				created = seq
			}
			if _, err := tx.ExecContext(ctx, `INSERT INTO jmap_changes(account_id, type, obj_id, modseq, created_modseq, destroyed, changed_at)
				VALUES (?,?,?,?,?,?,?)
				ON CONFLICT(account_id, type, obj_id) DO UPDATE SET modseq = excluded.modseq, destroyed = excluded.destroyed,
					changed_at = excluded.changed_at,
					created_modseq = CASE WHEN excluded.created_modseq > 0 THEN excluded.created_modseq ELSE jmap_changes.created_modseq END`,
				accountID, c.typ, c.id, seq, created, c.kind == 'd', t); err != nil {
				return err
			}
		}
		return nil
	})
}

// logMessageInsert records a new message, its thread and mailbox.
func (s *Store) logMessageInsert(ctx context.Context, m *Message) {
	thread := byte('u')
	if m.ThreadID == m.ID {
		thread = 'c'
	}
	ch := []objChange{{TypeEmail, m.ID, 'c'}, {TypeThread, m.ThreadID, thread}, {TypeMailbox, m.MailboxID, 'u'}}
	var role sql.NullString
	if s.DB.QueryRowContext(ctx, `SELECT role FROM mailboxes WHERE id = ?`, m.MailboxID).Scan(&role) == nil &&
		role.String != "drafts" && role.String != "sent" {
		ch = append(ch, objChange{TypeEmailDelivery, 0, 'u'})
	}
	s.logChanges(ctx, m.AccountID, ch)
}

// messageMailbox returns the mailbox of a message (0 if unknown).
func (s *Store) messageMailbox(ctx context.Context, accountID, id int64) int64 {
	var mb int64
	_ = s.DB.QueryRowContext(ctx, `SELECT mailbox_id FROM messages WHERE id = ? AND account_id = ?`, id, accountID).Scan(&mb)
	return mb
}

// logMessageUpdate records a changed message; oldMailbox is set when the
// message was moved.
func (s *Store) logMessageUpdate(ctx context.Context, accountID, id, oldMailbox int64) {
	ch := []objChange{{TypeEmail, id, 'u'}}
	if mb := s.messageMailbox(ctx, accountID, id); mb != 0 {
		ch = append(ch, objChange{TypeMailbox, mb, 'u'})
	}
	if oldMailbox != 0 {
		ch = append(ch, objChange{TypeMailbox, oldMailbox, 'u'})
	}
	s.logChanges(ctx, accountID, ch)
}

// logMessagesDeleted records removed messages; threads without remaining
// messages are destroyed as well.
func (s *Store) logMessagesDeleted(ctx context.Context, accountID int64, refs []msgRef, extra ...objChange) {
	ch := append([]objChange{}, extra...)
	threads := map[int64]bool{}
	for _, r := range refs {
		ch = append(ch, objChange{TypeEmail, r.id, 'd'}, objChange{TypeMailbox, r.mailbox, 'u'})
		threads[r.thread] = true
	}
	for t := range threads {
		var n int
		_ = s.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM messages WHERE account_id = ? AND thread_id = ?`, accountID, t).Scan(&n)
		kind := byte('u')
		if n == 0 {
			kind = 'd'
		}
		ch = append(ch, objChange{TypeThread, t, kind})
	}
	s.logChanges(ctx, accountID, ch)
}

// mailboxMessageRefs lists the messages of a mailbox (before it is deleted).
func (s *Store) mailboxMessageRefs(ctx context.Context, mailboxID int64) []msgRef {
	rows, err := s.DB.QueryContext(ctx, `SELECT id, thread_id FROM messages WHERE mailbox_id = ?`, mailboxID)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []msgRef
	for rows.Next() {
		r := msgRef{mailbox: mailboxID}
		if rows.Scan(&r.id, &r.thread) == nil {
			out = append(out, r)
		}
	}
	return out
}

// logMailbox records a mailbox change and wakes up change subscribers.
func (s *Store) logMailbox(ctx context.Context, accountID, id int64, kind byte) {
	s.logChanges(ctx, accountID, []objChange{{TypeMailbox, id, kind}})
	s.notify(accountID)
}

// LogChange records a change of an object type not covered by the store
// mutations (e.g. email submissions) and notifies subscribers.
func (s *Store) LogChange(ctx context.Context, accountID int64, typ string, id int64, kind byte) {
	s.logChanges(ctx, accountID, []objChange{{typ, id, kind}})
	s.notify(accountID)
}

// ChangeState returns the current state (highest modseq) of an object type.
func (s *Store) ChangeState(ctx context.Context, accountID int64, typ string) (int64, error) {
	var v int64
	err := s.DB.QueryRowContext(ctx, `SELECT COALESCE(MAX(modseq), 0) FROM jmap_changes WHERE account_id = ? AND type = ?`, accountID, typ).Scan(&v)
	return v, err
}

// ChangeStates returns the current state of every object type with changes.
func (s *Store) ChangeStates(ctx context.Context, accountID int64) (map[string]int64, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT type, MAX(modseq) FROM jmap_changes WHERE account_id = ? GROUP BY type`, accountID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]int64{}
	for rows.Next() {
		var t string
		var v int64
		if err := rows.Scan(&t, &v); err != nil {
			return nil, err
		}
		out[t] = v
	}
	return out, rows.Err()
}

// ChangeSet is the result of Changes.
type ChangeSet struct {
	Created, Updated, Destroyed []int64
	NewState                    int64
	HasMore                     bool
}

// Changes lists the objects of a type changed after since, at most max of
// them. It returns ErrCannotCalculateChanges for unknown or pruned states.
func (s *Store) Changes(ctx context.Context, accountID int64, typ string, since int64, max int) (*ChangeSet, error) {
	cur, err := s.ChangeState(ctx, accountID, typ)
	if err != nil {
		return nil, err
	}
	var floor int64
	_ = s.DB.QueryRowContext(ctx, `SELECT floor FROM jmap_floors WHERE account_id = ? AND type = ?`, accountID, typ).Scan(&floor)
	if since < 0 || since < floor || since > cur {
		return nil, ErrCannotCalculateChanges
	}
	if max <= 0 {
		max = 1000
	}
	rows, err := s.DB.QueryContext(ctx, `SELECT obj_id, modseq, created_modseq, destroyed FROM jmap_changes
		WHERE account_id = ? AND type = ? AND modseq > ? ORDER BY modseq LIMIT ?`, accountID, typ, since, max+1)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	cs := &ChangeSet{Created: []int64{}, Updated: []int64{}, Destroyed: []int64{}, NewState: cur}
	n := 0
	for rows.Next() {
		var id, seq, created int64
		var destroyed bool
		if err := rows.Scan(&id, &seq, &created, &destroyed); err != nil {
			return nil, err
		}
		if n == max {
			cs.HasMore = true
			break
		}
		n++
		cs.NewState = seq
		switch {
		case destroyed && created > since:
			// created and destroyed within the window: nothing to report
		case destroyed:
			cs.Destroyed = append(cs.Destroyed, id)
		case created > since:
			cs.Created = append(cs.Created, id)
		default:
			cs.Updated = append(cs.Updated, id)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if !cs.HasMore {
		cs.NewState = cur
	}
	return cs, nil
}

// PruneChanges drops tombstones older than maxAge and keeps at most
// maxTombstones per account; states older than the pruned rows can no
// longer calculate changes. It also forgets JMAP uploads older than
// uploadAge (their blobs are released) and submissions older than maxAge.
func (s *Store) PruneChanges(ctx context.Context, maxAge time.Duration, maxTombstones int, uploadAge time.Duration) error {
	cutoff := time.Now().Add(-maxAge).Unix()
	type bound struct {
		account int64
		typ     string
		seq     int64
	}
	var bounds []bound
	collect := func(q string, args ...any) error {
		rows, err := s.DB.QueryContext(ctx, q, args...)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var b bound
			if err := rows.Scan(&b.account, &b.typ, &b.seq); err != nil {
				return err
			}
			bounds = append(bounds, b)
		}
		return rows.Err()
	}
	if err := collect(`SELECT account_id, type, MAX(modseq) FROM jmap_changes WHERE destroyed = 1 AND changed_at < ? GROUP BY account_id, type`, cutoff); err != nil {
		return err
	}
	if maxTombstones > 0 {
		rows, err := s.DB.QueryContext(ctx, `SELECT account_id, type, COUNT(*) FROM jmap_changes WHERE destroyed = 1 GROUP BY account_id, type HAVING COUNT(*) > ?`, maxTombstones)
		if err != nil {
			return err
		}
		var over []bound
		for rows.Next() {
			var b bound
			if rows.Scan(&b.account, &b.typ, &b.seq) == nil {
				over = append(over, b)
			}
		}
		rows.Close()
		for _, b := range over {
			if err := collect(`SELECT account_id, type, modseq FROM jmap_changes WHERE account_id = ? AND type = ? AND destroyed = 1
				ORDER BY modseq LIMIT 1 OFFSET ?`, b.account, b.typ, b.seq-int64(maxTombstones)-1); err != nil {
				return err
			}
		}
	}
	for _, b := range bounds {
		// The newest row of a type is never removed, so the state of a
		// type never goes backwards.
		res, err := s.DB.ExecContext(ctx, `DELETE FROM jmap_changes WHERE account_id = ? AND type = ? AND destroyed = 1 AND modseq <= ?
			AND modseq < (SELECT MAX(modseq) FROM jmap_changes c WHERE c.account_id = ? AND c.type = ?)`, b.account, b.typ, b.seq, b.account, b.typ)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			continue
		}
		if _, err := s.DB.ExecContext(ctx, `INSERT INTO jmap_floors(account_id, type, floor) VALUES (?,?,?)
			ON CONFLICT(account_id, type) DO UPDATE SET floor = MAX(floor, excluded.floor)`, b.account, b.typ, b.seq); err != nil {
			return err
		}
	}
	if _, err := s.DB.ExecContext(ctx, `DELETE FROM jmap_submissions WHERE send_at < ?`, cutoff); err != nil {
		return err
	}
	hashes := s.blobHashes(ctx, `DELETE FROM jmap_uploads WHERE created_at < ? RETURNING blob_hash`, time.Now().Add(-uploadAge).Unix())
	for _, h := range hashes {
		s.releaseBlob(ctx, h)
	}
	return nil
}

// ---- JMAP uploads ----

// AddUpload remembers that an account uploaded a blob.
func (s *Store) AddUpload(ctx context.Context, accountID int64, hash, typ string, size int64) error {
	_, err := s.DB.ExecContext(ctx, `INSERT INTO jmap_uploads(account_id, blob_hash, type, size, created_at) VALUES (?,?,?,?,?)
		ON CONFLICT(account_id, blob_hash) DO UPDATE SET created_at = excluded.created_at, type = excluded.type`, accountID, hash, typ, size, now())
	return err
}

// BlobAccessible reports whether the account uploaded the blob or owns a
// message stored in it.
func (s *Store) BlobAccessible(ctx context.Context, accountID int64, hash string) bool {
	var ok bool
	err := s.DB.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM jmap_uploads WHERE account_id = ? AND blob_hash = ?)
		OR EXISTS(SELECT 1 FROM messages WHERE account_id = ? AND blob_hash = ?)`, accountID, hash, accountID, hash).Scan(&ok)
	return err == nil && ok
}

// ---- JMAP email submissions ----

type Submission struct {
	ID           int64
	IdentityID   string
	EmailID      int64
	ThreadID     int64
	EnvelopeJSON string
	SendAt       time.Time
}

// CreateSubmission stores a completed submission.
func (s *Store) CreateSubmission(ctx context.Context, accountID int64, sub *Submission) error {
	var id int64
	err := s.tx(ctx, func(tx *sql.Tx) error {
		var err error
		if id, err = nextID(ctx, tx, "jmap_submissions"); err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, `INSERT INTO jmap_submissions(id, account_id, identity_id, email_id, thread_id, envelope_json, send_at) VALUES (?,?,?,?,?,?,?)`,
			id, accountID, sub.IdentityID, sub.EmailID, sub.ThreadID, sub.EnvelopeJSON, sub.SendAt.Unix())
		return err
	})
	if err != nil {
		return err
	}
	sub.ID = id
	s.LogChange(ctx, accountID, TypeEmailSubmission, sub.ID, 'c')
	return nil
}

// Submissions lists the submissions of an account, newest first.
func (s *Store) Submissions(ctx context.Context, accountID int64) ([]*Submission, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT id, identity_id, email_id, thread_id, envelope_json, send_at FROM jmap_submissions
		WHERE account_id = ? ORDER BY send_at DESC, id DESC`, accountID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Submission
	for rows.Next() {
		var sub Submission
		var at int64
		if err := rows.Scan(&sub.ID, &sub.IdentityID, &sub.EmailID, &sub.ThreadID, &sub.EnvelopeJSON, &at); err != nil {
			return nil, err
		}
		sub.SendAt = ts(at)
		out = append(out, &sub)
	}
	return out, rows.Err()
}

// DeleteSubmission removes a submission record.
func (s *Store) DeleteSubmission(ctx context.Context, accountID, id int64) error {
	res, err := s.DB.ExecContext(ctx, `DELETE FROM jmap_submissions WHERE id = ? AND account_id = ?`, id, accountID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	s.LogChange(ctx, accountID, TypeEmailSubmission, id, 'd')
	return nil
}
