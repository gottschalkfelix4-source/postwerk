package store

import (
	"context"
	"database/sql"
	"encoding/binary"
	"encoding/json"
	"errors"
	"math"
	"sort"
	"strings"
	"time"
)

type MailboxDef struct{ Name, Role string }

var DefaultMailboxes = []MailboxDef{
	{"Posteingang", "inbox"}, {"Entwürfe", "drafts"}, {"Gesendet", "sent"},
	{"Archiv", "archive"}, {"Spam", "junk"}, {"Papierkorb", "trash"},
}

type Mailbox struct {
	ID     int64   `json:"id"`
	Name   string  `json:"name"`
	Role   *string `json:"role"`
	Total  int     `json:"total"`
	Unread int     `json:"unread"`
}

type Address struct {
	Name  string `json:"name"`
	Email string `json:"email"`
}

type AuthResults struct {
	SPF   string `json:"spf"`
	DKIM  string `json:"dkim"`
	DMARC string `json:"dmarc"`
}

type ExtractedEvent struct {
	Title       string  `json:"title"`
	Start       string  `json:"start"`
	End         *string `json:"end"`
	Location    *string `json:"location"`
	Description *string `json:"description"`
}

type ExtractedTask struct {
	Title string  `json:"title"`
	Due   *string `json:"due"`
}

type Message struct {
	ID             int64     `json:"id"`
	AccountID      int64     `json:"-"`
	MailboxID      int64     `json:"mailboxId"`
	ThreadID       int64     `json:"threadId"`
	UID            int64     `json:"-"`
	MessageID      string    `json:"-"`
	InReplyTo      string    `json:"-"`
	From           Address   `json:"from"`
	To             []Address `json:"to"`
	Cc             []Address `json:"-"`
	ReplyTo        []Address `json:"-"`
	Subject        string    `json:"subject"`
	Preview        string    `json:"preview"`
	ReceivedAt     time.Time `json:"receivedAt"`
	Size           int64     `json:"size"`
	Seen           bool      `json:"seen"`
	Flagged        bool      `json:"flagged"`
	Answered       bool      `json:"-"`
	Draft          bool      `json:"-"`
	HasAttachments bool      `json:"hasAttachments"`
	Tags           []string  `json:"tags"`
	Category       *string   `json:"category"`
	Priority       string    `json:"priority"`
	SpamScore      float64   `json:"spamScore"`
	Summary        *string   `json:"summary"`

	Auth       AuthResults      `json:"-"`
	Virus      *string          `json:"-"`
	AIReasons  []string         `json:"-"`
	AIEvents   []ExtractedEvent `json:"-"`
	AITasks    []ExtractedTask  `json:"-"`
	AIDone     bool             `json:"-"`
	References []string         `json:"-"`
	SearchText string           `json:"-"`
	BlobHash   string           `json:"-"`
	// AISuspicious marks messages the AI analysis suspects of prompt injection.
	AISuspicious bool `json:"aiSuspicious"`
}

// ---- mailboxes ----

func (s *Store) ListMailboxes(ctx context.Context, accountID int64) ([]Mailbox, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT b.id, b.name, b.role,
		(SELECT COUNT(*) FROM messages m WHERE m.mailbox_id = b.id),
		(SELECT COUNT(*) FROM messages m WHERE m.mailbox_id = b.id AND m.seen = 0)
		FROM mailboxes b WHERE b.account_id = ? ORDER BY b.id`, accountID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Mailbox{}
	for rows.Next() {
		var m Mailbox
		var role sql.NullString
		if err := rows.Scan(&m.ID, &m.Name, &role, &m.Total, &m.Unread); err != nil {
			return nil, err
		}
		m.Role = nullStr(role)
		out = append(out, m)
	}
	return out, rows.Err()
}

func (s *Store) MailboxByRole(ctx context.Context, accountID int64, role string) (int64, error) {
	var id int64
	err := s.DB.QueryRowContext(ctx, `SELECT id FROM mailboxes WHERE account_id = ? AND role = ?`, accountID, role).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, ErrNotFound
	}
	return id, err
}

func (s *Store) MailboxOwned(ctx context.Context, accountID, mailboxID int64) (*Mailbox, error) {
	var m Mailbox
	var role sql.NullString
	err := s.DB.QueryRowContext(ctx, `SELECT id, name, role FROM mailboxes WHERE id = ? AND account_id = ?`, mailboxID, accountID).Scan(&m.ID, &m.Name, &role)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	m.Role = nullStr(role)
	return &m, err
}

func (s *Store) CreateMailbox(ctx context.Context, accountID int64, name string) (*Mailbox, error) {
	res, err := s.DB.ExecContext(ctx, `INSERT INTO mailboxes(account_id, name, uid_validity, created_at) VALUES (?,?,?,?)`, accountID, name, now(), now())
	if err != nil {
		if strings.Contains(err.Error(), "UNIQUE") {
			return nil, ErrConflict
		}
		return nil, err
	}
	id, _ := res.LastInsertId()
	s.logMailbox(ctx, accountID, id, 'c')
	return &Mailbox{ID: id, Name: name}, nil
}

func (s *Store) RenameMailbox(ctx context.Context, accountID, id int64, name string) error {
	_, err := s.DB.ExecContext(ctx, `UPDATE mailboxes SET name = ? WHERE id = ? AND account_id = ?`, name, id, accountID)
	if err != nil && strings.Contains(err.Error(), "UNIQUE") {
		return ErrConflict
	}
	if err == nil {
		s.logMailbox(ctx, accountID, id, 'u')
	}
	return err
}

func (s *Store) DeleteMailbox(ctx context.Context, accountID, id int64) error {
	mb, err := s.MailboxOwned(ctx, accountID, id)
	if err != nil {
		return err
	}
	if mb.Role != nil {
		return ErrConflict
	}
	hashes := s.blobHashes(ctx, `SELECT blob_hash FROM messages WHERE mailbox_id = ?`, id)
	refs := s.mailboxMessageRefs(ctx, id)
	if _, err := s.DB.ExecContext(ctx, `DELETE FROM mailboxes WHERE id = ?`, id); err != nil {
		return err
	}
	s.logMessagesDeleted(ctx, accountID, refs, objChange{TypeMailbox, id, 'd'})
	s.notify(accountID)
	for _, h := range hashes {
		s.releaseBlob(ctx, h)
	}
	return nil
}

// ---- messages ----

const msgCols = `id, account_id, mailbox_id, thread_id, uid, message_id, in_reply_to, subject, from_name, from_email,
	to_json, cc_json, reply_to_json, preview, received_at, size, seen, flagged, answered, draft, has_attachments,
	tags_json, category, priority, spam_score, auth_json, virus, ai_summary, ai_reasons_json, ai_events_json, ai_tasks_json, ai_done, blob_hash, ai_suspicious`

func scanMessage(sc interface{ Scan(...any) error }) (*Message, error) {
	var m Message
	var to, cc, rt, tags, auth, reasons, events, tasks string
	var cat, virus, summary sql.NullString
	var received int64
	err := sc.Scan(&m.ID, &m.AccountID, &m.MailboxID, &m.ThreadID, &m.UID, &m.MessageID, &m.InReplyTo, &m.Subject, &m.From.Name, &m.From.Email,
		&to, &cc, &rt, &m.Preview, &received, &m.Size, &m.Seen, &m.Flagged, &m.Answered, &m.Draft, &m.HasAttachments,
		&tags, &cat, &m.Priority, &m.SpamScore, &auth, &virus, &summary, &reasons, &events, &tasks, &m.AIDone, &m.BlobHash, &m.AISuspicious)
	if err != nil {
		return nil, err
	}
	m.ReceivedAt = ts(received)
	_ = json.Unmarshal([]byte(to), &m.To)
	_ = json.Unmarshal([]byte(cc), &m.Cc)
	_ = json.Unmarshal([]byte(rt), &m.ReplyTo)
	_ = json.Unmarshal([]byte(tags), &m.Tags)
	_ = json.Unmarshal([]byte(auth), &m.Auth)
	_ = json.Unmarshal([]byte(reasons), &m.AIReasons)
	_ = json.Unmarshal([]byte(events), &m.AIEvents)
	_ = json.Unmarshal([]byte(tasks), &m.AITasks)
	for _, p := range []*[]Address{&m.To, &m.Cc, &m.ReplyTo} {
		if *p == nil {
			*p = []Address{}
		}
	}
	if m.Tags == nil {
		m.Tags = []string{}
	}
	if m.AIReasons == nil {
		m.AIReasons = []string{}
	}
	if m.AIEvents == nil {
		m.AIEvents = []ExtractedEvent{}
	}
	if m.AITasks == nil {
		m.AITasks = []ExtractedTask{}
	}
	m.Category = nullStr(cat)
	m.Virus = nullStr(virus)
	m.Summary = nullStr(summary)
	return &m, nil
}

func js(v any) string { b, _ := json.Marshal(v); return string(b) }

// InsertMessage stores message metadata, assigning UID and thread.
func (s *Store) InsertMessage(ctx context.Context, m *Message) error {
	if m.Tags == nil {
		m.Tags = []string{}
	}
	if m.Priority == "" {
		m.Priority = "normal"
	}
	if err := s.tx(ctx, func(tx *sql.Tx) error {
		if err := tx.QueryRowContext(ctx, `UPDATE mailboxes SET uid_next = uid_next + 1 WHERE id = ? RETURNING uid_next - 1`, m.MailboxID).Scan(&m.UID); err != nil {
			return err
		}
		// Thread detection via In-Reply-To / References.
		refs := append([]string{}, m.References...)
		if m.InReplyTo != "" {
			refs = append(refs, m.InReplyTo)
		}
		for _, r := range refs {
			if r == "" {
				continue
			}
			var tid int64
			if err := tx.QueryRowContext(ctx, `SELECT thread_id FROM messages WHERE account_id = ? AND message_id = ? LIMIT 1`, m.AccountID, r).Scan(&tid); err == nil && tid != 0 {
				m.ThreadID = tid
				break
			}
		}
		res, err := tx.ExecContext(ctx, `INSERT INTO messages(account_id, mailbox_id, thread_id, uid, message_id, in_reply_to, subject, from_name, from_email,
			to_json, cc_json, reply_to_json, preview, received_at, size, seen, flagged, answered, draft, has_attachments, tags_json, category, priority,
			spam_score, auth_json, virus, search_text, blob_hash, ai_done) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
			m.AccountID, m.MailboxID, m.ThreadID, m.UID, m.MessageID, m.InReplyTo, m.Subject, m.From.Name, m.From.Email,
			js(m.To), js(m.Cc), js(m.ReplyTo), m.Preview, m.ReceivedAt.Unix(), m.Size, m.Seen, m.Flagged, m.Answered, m.Draft, m.HasAttachments,
			js(m.Tags), toNull(m.Category), m.Priority, m.SpamScore, js(m.Auth), toNull(m.Virus), strings.ToLower(m.SearchText), m.BlobHash, m.AIDone)
		if err != nil {
			return err
		}
		m.ID, _ = res.LastInsertId()
		if m.ThreadID == 0 {
			m.ThreadID = m.ID
			_, err = tx.ExecContext(ctx, `UPDATE messages SET thread_id = ? WHERE id = ?`, m.ID, m.ID)
		}
		return err
	}); err != nil {
		return err
	}
	s.logMessageInsert(ctx, m)
	s.notify(m.AccountID)
	return nil
}

func (s *Store) GetMessage(ctx context.Context, accountID, id int64) (*Message, error) {
	m, err := scanMessage(s.DB.QueryRowContext(ctx, `SELECT `+msgCols+` FROM messages WHERE id = ? AND account_id = ?`, id, accountID))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return m, err
}

// GetMessageAny loads a message without ownership check (internal workers only).
func (s *Store) GetMessageAny(ctx context.Context, id int64) (*Message, error) {
	m, err := scanMessage(s.DB.QueryRowContext(ctx, `SELECT `+msgCols+` FROM messages WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return m, err
}

type MessageQuery struct {
	AccountID int64
	MailboxID int64
	Query     string
	Tag       string
	Category  string
	Unread    bool
	Limit     int
	Offset    int
}

func (s *Store) ListMessages(ctx context.Context, q MessageQuery) ([]*Message, int, error) {
	where := []string{"account_id = ?"}
	args := []any{q.AccountID}
	if q.MailboxID != 0 {
		where = append(where, "mailbox_id = ?")
		args = append(args, q.MailboxID)
	} else {
		// Search across all folders except trash and junk.
		where = append(where, "mailbox_id NOT IN (SELECT id FROM mailboxes WHERE account_id = ? AND role IN ('trash','junk'))")
		args = append(args, q.AccountID)
	}
	if q.Query != "" {
		for _, term := range strings.Fields(strings.ToLower(q.Query)) {
			where = append(where, "search_text LIKE ?")
			args = append(args, "%"+term+"%")
		}
	}
	if q.Tag != "" {
		where = append(where, "EXISTS (SELECT 1 FROM json_each(tags_json) WHERE value = ?)")
		args = append(args, q.Tag)
	}
	if q.Category != "" {
		where = append(where, "category = ?")
		args = append(args, q.Category)
	}
	if q.Unread {
		where = append(where, "seen = 0")
	}
	w := strings.Join(where, " AND ")
	var total int
	if err := s.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM messages WHERE `+w, args...).Scan(&total); err != nil {
		return nil, 0, err
	}
	rows, err := s.DB.QueryContext(ctx, `SELECT `+msgCols+` FROM messages WHERE `+w+` ORDER BY received_at DESC, id DESC LIMIT ? OFFSET ?`, append(args, q.Limit, q.Offset)...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	out := []*Message{}
	for rows.Next() {
		m, err := scanMessage(rows)
		if err != nil {
			return nil, 0, err
		}
		out = append(out, m)
	}
	return out, total, rows.Err()
}

func (s *Store) ListMailboxMessages(ctx context.Context, mailboxID int64) ([]*Message, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT `+msgCols+` FROM messages WHERE mailbox_id = ? ORDER BY uid`, mailboxID)
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

func (s *Store) ThreadMessages(ctx context.Context, accountID, threadID int64) ([]*Message, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT `+msgCols+` FROM messages WHERE account_id = ? AND thread_id = ? ORDER BY received_at`, accountID, threadID)
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

type MessagePatch struct {
	Seen      *bool
	Flagged   *bool
	Answered  *bool
	MailboxID *int64
	Tags      *[]string
}

func (s *Store) UpdateMessage(ctx context.Context, accountID, id int64, p MessagePatch) error {
	sets := []string{}
	args := []any{}
	if p.Seen != nil {
		sets = append(sets, "seen = ?")
		args = append(args, *p.Seen)
	}
	if p.Flagged != nil {
		sets = append(sets, "flagged = ?")
		args = append(args, *p.Flagged)
	}
	if p.Answered != nil {
		sets = append(sets, "answered = ?")
		args = append(args, *p.Answered)
	}
	if p.Tags != nil {
		sets = append(sets, "tags_json = ?")
		args = append(args, js(*p.Tags))
	}
	var oldMailbox int64
	if p.MailboxID != nil {
		if _, err := s.MailboxOwned(ctx, accountID, *p.MailboxID); err != nil {
			return err
		}
		oldMailbox = s.messageMailbox(ctx, accountID, id)
		sets = append(sets, "mailbox_id = ?", "uid = (SELECT uid_next FROM mailboxes WHERE id = ?)")
		args = append(args, *p.MailboxID, *p.MailboxID)
	}
	if len(sets) == 0 {
		return nil
	}
	err := s.tx(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `UPDATE messages SET `+strings.Join(sets, ", ")+` WHERE id = ? AND account_id = ?`, append(args, id, accountID)...)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return ErrNotFound
		}
		if p.MailboxID != nil {
			_, err = tx.ExecContext(ctx, `UPDATE mailboxes SET uid_next = uid_next + 1 WHERE id = ?`, *p.MailboxID)
		}
		return err
	})
	if err == nil {
		s.logMessageUpdate(ctx, accountID, id, oldMailbox)
		s.notify(accountID)
	}
	return err
}

type AIResult struct {
	Category  *string
	Priority  string
	Tags      []string
	Summary   *string
	Reasons   []string
	Events    []ExtractedEvent
	Tasks     []ExtractedTask
	SpamScore *float64
	// Suspicious flags a suspected prompt injection.
	Suspicious bool
}

func (s *Store) SetAIResult(ctx context.Context, id int64, r AIResult) error {
	if r.Priority == "" {
		r.Priority = "normal"
	}
	q := `UPDATE messages SET category = ?, priority = ?, tags_json = ?, ai_summary = ?, ai_reasons_json = ?, ai_events_json = ?, ai_tasks_json = ?, ai_done = 1, ai_suspicious = ?`
	args := []any{toNull(r.Category), r.Priority, js(nonNil(r.Tags)), toNull(r.Summary), js(nonNil(r.Reasons)), js(r.Events), js(r.Tasks), r.Suspicious}
	if r.SpamScore != nil {
		q += `, spam_score = ?`
		args = append(args, *r.SpamScore)
	}
	_, err := s.DB.ExecContext(ctx, q+` WHERE id = ?`, append(args, id)...)
	return err
}

func (s *Store) SetAIEvents(ctx context.Context, id int64, ev []ExtractedEvent, tasks []ExtractedTask) error {
	_, err := s.DB.ExecContext(ctx, `UPDATE messages SET ai_events_json = ?, ai_tasks_json = ? WHERE id = ?`, js(ev), js(tasks), id)
	return err
}

func (s *Store) PendingAIMessages(ctx context.Context, limit int) ([]int64, error) {
	// Only incoming mail is analyzed; sent, drafts, junk and trash are skipped.
	rows, err := s.DB.QueryContext(ctx, `SELECT m.id FROM messages m JOIN mailboxes b ON b.id = m.mailbox_id
		WHERE m.ai_done = 0 AND m.draft = 0 AND (b.role IS NULL OR b.role IN ('inbox','archive'))
		ORDER BY m.id DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []int64
	for rows.Next() {
		var id int64
		rows.Scan(&id)
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

func nonNil(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

func (s *Store) DeleteMessage(ctx context.Context, accountID, id int64) error {
	m, err := s.GetMessage(ctx, accountID, id)
	if err != nil {
		return err
	}
	if _, err := s.DB.ExecContext(ctx, `DELETE FROM messages WHERE id = ?`, id); err != nil {
		return err
	}
	s.releaseBlob(ctx, m.BlobHash)
	s.logMessagesDeleted(ctx, accountID, []msgRef{{m.ID, m.MailboxID, m.ThreadID}})
	s.notify(accountID)
	return nil
}

func (s *Store) blobHashes(ctx context.Context, q string, args ...any) []string {
	rows, err := s.DB.QueryContext(ctx, q, args...)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var h string
		rows.Scan(&h)
		out = append(out, h)
	}
	return out
}

// releaseBlob deletes a blob once neither messages, the queue nor JMAP
// uploads reference it.
func (s *Store) releaseBlob(ctx context.Context, hash string) {
	var n int
	if err := s.DB.QueryRowContext(ctx, `SELECT (SELECT COUNT(*) FROM messages WHERE blob_hash = ?) + (SELECT COUNT(*) FROM queue WHERE blob_hash = ?)
		+ (SELECT COUNT(*) FROM jmap_uploads WHERE blob_hash = ?)`, hash, hash, hash).Scan(&n); err == nil && n == 0 {
		_ = s.Blobs.Delete(hash)
	}
}

type TagCount struct {
	Name  string `json:"name"`
	Count int    `json:"count"`
}

func (s *Store) TagCounts(ctx context.Context, accountID int64) ([]TagCount, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT j.value, COUNT(*) FROM messages m, json_each(m.tags_json) j WHERE m.account_id = ? GROUP BY j.value ORDER BY COUNT(*) DESC`, accountID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []TagCount{}
	for rows.Next() {
		var t TagCount
		rows.Scan(&t.Name, &t.Count)
		out = append(out, t)
	}
	return out, rows.Err()
}

func (s *Store) CategoryCounts(ctx context.Context, since time.Time) ([]TagCount, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT category, COUNT(*) FROM messages WHERE category IS NOT NULL AND received_at >= ? GROUP BY category ORDER BY COUNT(*) DESC`, since.Unix())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []TagCount{}
	for rows.Next() {
		var t TagCount
		rows.Scan(&t.Name, &t.Count)
		out = append(out, t)
	}
	return out, rows.Err()
}

// ErrQuota is returned when storing a message would exceed the account quota.
var ErrQuota = errors.New("quota exceeded")

// CheckQuota returns ErrQuota when adding size bytes would exceed the quota
// of the account. Accounts without a quota (0) always pass.
func (s *Store) CheckQuota(ctx context.Context, accountID, size int64) error {
	var quota int64
	if err := s.DB.QueryRowContext(ctx, `SELECT quota_bytes FROM accounts WHERE id = ?`, accountID).Scan(&quota); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		}
		return err
	}
	if quota > 0 && s.UsedBytes(ctx, accountID)+size > quota {
		return ErrQuota
	}
	return nil
}

func (s *Store) UsedBytes(ctx context.Context, accountID int64) int64 {
	var n int64
	_ = s.DB.QueryRowContext(ctx, `SELECT COALESCE(SUM(size),0) FROM messages WHERE account_id = ?`, accountID).Scan(&n)
	return n
}

func (s *Store) TotalMessages(ctx context.Context) (count int, bytes int64) {
	_ = s.DB.QueryRowContext(ctx, `SELECT COUNT(*), COALESCE(SUM(size),0) FROM messages`).Scan(&count, &bytes)
	return
}

// ---- embeddings ----

func encodeVec(v []float32) []byte {
	b := make([]byte, 4*len(v))
	for i, f := range v {
		binary.LittleEndian.PutUint32(b[i*4:], math.Float32bits(f))
	}
	return b
}

func decodeVec(b []byte) []float32 {
	v := make([]float32, len(b)/4)
	for i := range v {
		v[i] = math.Float32frombits(binary.LittleEndian.Uint32(b[i*4:]))
	}
	return v
}

func (s *Store) SaveEmbedding(ctx context.Context, messageID, accountID int64, v []float32) error {
	_, err := s.DB.ExecContext(ctx, `INSERT INTO embeddings(message_id, account_id, vector) VALUES (?,?,?)
		ON CONFLICT(message_id) DO UPDATE SET vector = excluded.vector`, messageID, accountID, encodeVec(normalizeVec(v)))
	return err
}

func normalizeVec(v []float32) []float32 {
	var sum float64
	for _, f := range v {
		sum += float64(f) * float64(f)
	}
	n := float32(math.Sqrt(sum))
	if n == 0 {
		return v
	}
	out := make([]float32, len(v))
	for i, f := range v {
		out[i] = f / n
	}
	return out
}

type Scored struct {
	MessageID int64
	Score     float64
}

// SimilarMessages ranks the account's messages by cosine similarity.
func (s *Store) SimilarMessages(ctx context.Context, accountID int64, query []float32, limit int) ([]Scored, error) {
	q := normalizeVec(query)
	rows, err := s.DB.QueryContext(ctx, `SELECT message_id, vector FROM embeddings WHERE account_id = ?`, accountID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Scored
	for rows.Next() {
		var id int64
		var b []byte
		if err := rows.Scan(&id, &b); err != nil {
			return nil, err
		}
		v := decodeVec(b)
		if len(v) != len(q) {
			continue
		}
		var dot float64
		for i := range v {
			dot += float64(v[i]) * float64(q[i])
		}
		out = append(out, Scored{id, dot})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Score > out[j].Score })
	if len(out) > limit {
		out = out[:limit]
	}
	return out, rows.Err()
}

func (s *Store) MessagesWithoutEmbedding(ctx context.Context, limit int) ([]int64, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT m.id FROM messages m LEFT JOIN embeddings e ON e.message_id = m.id
		WHERE e.message_id IS NULL AND m.draft = 0 ORDER BY m.id DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []int64
	for rows.Next() {
		var id int64
		rows.Scan(&id)
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// MessageKeys returns the Message-IDs and blob hashes stored in a mailbox
// (used to skip duplicates when importing).
func (s *Store) MessageKeys(ctx context.Context, mailboxID int64) (map[string]bool, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT message_id, blob_hash FROM messages WHERE mailbox_id = ?`, mailboxID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]bool{}
	for rows.Next() {
		var id, hash string
		if err := rows.Scan(&id, &hash); err != nil {
			return nil, err
		}
		if id != "" {
			out["id:"+id] = true
		}
		out["h:"+hash] = true
	}
	return out, rows.Err()
}

// MailboxByName finds a mailbox by name, creating it if needed.
func (s *Store) EnsureMailbox(ctx context.Context, accountID int64, name string) (int64, error) {
	var id int64
	err := s.DB.QueryRowContext(ctx, `SELECT id FROM mailboxes WHERE account_id = ? AND name = ?`, accountID, name).Scan(&id)
	if err == nil {
		return id, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return 0, err
	}
	mb, err := s.CreateMailbox(ctx, accountID, name)
	if err != nil {
		return 0, err
	}
	return mb.ID, nil
}
