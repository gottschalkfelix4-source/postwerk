package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"sort"
	"strings"
	"sync"
	"time"
)

// ---- change notifications (IMAP IDLE) ----

type changeHub struct {
	mu   sync.Mutex
	subs map[int64]map[chan struct{}]struct{}
}

// SubscribeChanges returns a channel that receives a signal whenever messages
// of the account change (new, moved, flags, deleted). Signals are coalesced.
func (s *Store) SubscribeChanges(accountID int64) (<-chan struct{}, func()) {
	ch := make(chan struct{}, 1)
	h := &s.changes
	h.mu.Lock()
	if h.subs == nil {
		h.subs = map[int64]map[chan struct{}]struct{}{}
	}
	if h.subs[accountID] == nil {
		h.subs[accountID] = map[chan struct{}]struct{}{}
	}
	h.subs[accountID][ch] = struct{}{}
	h.mu.Unlock()
	return ch, func() {
		h.mu.Lock()
		delete(h.subs[accountID], ch)
		if len(h.subs[accountID]) == 0 {
			delete(h.subs, accountID)
		}
		h.mu.Unlock()
	}
}

func (s *Store) notify(accountID int64) {
	h := &s.changes
	h.mu.Lock()
	defer h.mu.Unlock()
	for ch := range h.subs[accountID] {
		select {
		case ch <- struct{}{}:
		default:
		}
	}
}

// ---- IMAP mailbox state ----

type IMAPMailbox struct {
	ID          int64
	Name        string
	Role        string
	UIDNext     uint32
	UIDValidity uint32
	Subscribed  bool
}

const imapMailboxCols = `id, name, COALESCE(role, ''), uid_next, uid_validity, subscribed`

func scanIMAPMailbox(sc interface{ Scan(...any) error }) (*IMAPMailbox, error) {
	var mb IMAPMailbox
	var uidNext, uidValidity int64
	if err := sc.Scan(&mb.ID, &mb.Name, &mb.Role, &uidNext, &uidValidity, &mb.Subscribed); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	mb.UIDNext = uint32(uidNext)
	mb.UIDValidity = uint32(uidValidity)
	return &mb, nil
}

// IMAPMailboxes lists all mailboxes of an account with their IMAP state.
func (s *Store) IMAPMailboxes(ctx context.Context, accountID int64) ([]*IMAPMailbox, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT `+imapMailboxCols+` FROM mailboxes WHERE account_id = ? ORDER BY id`, accountID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*IMAPMailbox
	for rows.Next() {
		mb, err := scanIMAPMailbox(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, mb)
	}
	return out, rows.Err()
}

func (s *Store) IMAPMailboxByID(ctx context.Context, accountID, id int64) (*IMAPMailbox, error) {
	return scanIMAPMailbox(s.DB.QueryRowContext(ctx, `SELECT `+imapMailboxCols+` FROM mailboxes WHERE id = ? AND account_id = ?`, id, accountID))
}

func (s *Store) SetMailboxSubscribed(ctx context.Context, accountID, id int64, on bool) error {
	_, err := s.DB.ExecContext(ctx, `UPDATE mailboxes SET subscribed = ? WHERE id = ? AND account_id = ?`, on, id, accountID)
	return err
}

// MailboxCounts returns total, unseen, \Deleted counts and the size of a mailbox.
func (s *Store) MailboxCounts(ctx context.Context, mailboxID int64) (total, unseen, deleted int, size int64, err error) {
	err = s.DB.QueryRowContext(ctx, `SELECT COUNT(*), COALESCE(SUM(seen = 0), 0), COALESCE(SUM(deleted), 0), COALESCE(SUM(size), 0) FROM messages WHERE mailbox_id = ?`, mailboxID).
		Scan(&total, &unseen, &deleted, &size)
	return
}

// ---- IMAP messages ----

// IMAPFlags are the flags of one message as IMAP sees them.
type IMAPFlags struct {
	Seen, Flagged, Answered, Draft, Deleted bool
	Keywords                                []string // custom keywords like $Junk, $Forwarded
}

type IMAPMessage struct {
	ID         int64
	UID        uint32
	Size       int64
	ReceivedAt time.Time
	BlobHash   string
	Flags      IMAPFlags
}

// IMAPMessages returns the messages of a mailbox ordered by UID.
func (s *Store) IMAPMessages(ctx context.Context, mailboxID int64) ([]IMAPMessage, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT id, uid, size, received_at, blob_hash, seen, flagged, answered, draft, deleted, keywords_json
		FROM messages WHERE mailbox_id = ? ORDER BY uid`, mailboxID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []IMAPMessage
	for rows.Next() {
		var m IMAPMessage
		var uid, received int64
		var kw string
		if err := rows.Scan(&m.ID, &uid, &m.Size, &received, &m.BlobHash, &m.Flags.Seen, &m.Flags.Flagged, &m.Flags.Answered,
			&m.Flags.Draft, &m.Flags.Deleted, &kw); err != nil {
			return nil, err
		}
		m.UID = uint32(uid)
		m.ReceivedAt = time.Unix(received, 0)
		_ = json.Unmarshal([]byte(kw), &m.Flags.Keywords)
		out = append(out, m)
	}
	return out, rows.Err()
}

// SetIMAPFlags replaces all flags of a message.
func (s *Store) SetIMAPFlags(ctx context.Context, accountID, id int64, f IMAPFlags) error {
	kw := normalizeKeywords(f.Keywords)
	res, err := s.DB.ExecContext(ctx, `UPDATE messages SET seen = ?, flagged = ?, answered = ?, draft = ?, deleted = ?, keywords_json = ? WHERE id = ? AND account_id = ?`,
		f.Seen, f.Flagged, f.Answered, f.Draft, f.Deleted, js(kw), id, accountID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	s.notify(accountID)
	return nil
}

func normalizeKeywords(in []string) []string {
	seen := map[string]bool{}
	out := []string{}
	for _, k := range in {
		k = strings.TrimSpace(k)
		if k == "" || strings.HasPrefix(k, "\\") || seen[strings.ToLower(k)] {
			continue
		}
		seen[strings.ToLower(k)] = true
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// CopyMessage duplicates a message into another mailbox of the same account
// (the blob is shared) and returns the new message.
func (s *Store) CopyMessage(ctx context.Context, accountID, id, destMailboxID int64) (*Message, error) {
	if _, err := s.MailboxOwned(ctx, accountID, destMailboxID); err != nil {
		return nil, err
	}
	m, err := s.GetMessage(ctx, accountID, id)
	if err != nil {
		return nil, err
	}
	var deleted bool
	var kw, searchText string
	if err := s.DB.QueryRowContext(ctx, `SELECT deleted, keywords_json, search_text FROM messages WHERE id = ?`, id).Scan(&deleted, &kw, &searchText); err != nil {
		return nil, err
	}
	cp := *m
	cp.ID, cp.UID, cp.MailboxID, cp.ThreadID = 0, 0, destMailboxID, m.ThreadID
	cp.SearchText = searchText
	cp.AIDone = true
	if err := s.InsertMessage(ctx, &cp); err != nil {
		return nil, err
	}
	if kw != "[]" {
		_, _ = s.DB.ExecContext(ctx, `UPDATE messages SET keywords_json = ? WHERE id = ?`, kw, cp.ID)
	}
	return &cp, nil
}

// UIDByMessageID finds a message with the same Message-ID and size in a
// mailbox (duplicate APPEND of a sent message).
func (s *Store) UIDByMessageID(ctx context.Context, mailboxID int64, messageID string, size int64) (uint32, bool) {
	var uid int64
	err := s.DB.QueryRowContext(ctx, `SELECT uid FROM messages WHERE mailbox_id = ? AND message_id = ? AND size = ? LIMIT 1`, mailboxID, messageID, size).Scan(&uid)
	return uint32(uid), err == nil
}

// MessageUID returns the current UID of a message.
func (s *Store) MessageUID(ctx context.Context, id int64) (uint32, error) {
	var uid int64
	err := s.DB.QueryRowContext(ctx, `SELECT uid FROM messages WHERE id = ?`, id).Scan(&uid)
	return uint32(uid), err
}
