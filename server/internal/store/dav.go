package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"postwerk/internal/pim"
)

// Calendars, task lists and address books ("collections") hold iCalendar
// and vCard objects. Every change increments the collection's sync sequence:
// it is the CTag and, together with per-object modseqs and tombstones, the
// basis of WebDAV sync tokens (RFC 6578).

const (
	KindCalendar    = "calendar"
	KindAddressBook = "addressbook"
)

// ErrPrecondition is returned when If-Match/If-None-Match do not hold.
var ErrPrecondition = errors.New("precondition failed")

// ErrUIDConflict is returned when another object in the collection already
// uses the UID.
var ErrUIDConflict = errors.New("uid conflict")

// DefaultCalendarColor is used for new calendars without a colour.
const DefaultCalendarColor = "#4f46e5"

type Collection struct {
	ID          int64     `json:"id"`
	AccountID   int64     `json:"-"`
	Kind        string    `json:"-"`
	Slug        string    `json:"slug"`
	Name        string    `json:"name"`
	Description string    `json:"description"`
	Color       string    `json:"color"`
	Components  []string  `json:"components"`
	SyncSeq     int64     `json:"-"`
	CreatedAt   time.Time `json:"-"`
}

// Supports reports whether a calendar accepts the component type.
func (c *Collection) Supports(comp string) bool {
	if c.Kind != KindCalendar {
		return false
	}
	for _, x := range c.Components {
		if strings.EqualFold(x, comp) {
			return true
		}
	}
	return false
}

// DavObject is a stored calendar object or vCard.
type DavObject struct {
	ID              int64
	CollectionID    int64
	AccountID       int64
	Name            string // resource name, e.g. "<uid>.ics"
	UID             string
	Component       string // VEVENT, VTODO, VJOURNAL or VCARD
	Data            []byte
	ETag            string
	ModSeq          int64
	SourceMessageID *int64
	CreatedAt       time.Time
	UpdatedAt       time.Time
}

func objTable(kind string) string {
	if kind == KindAddressBook {
		return "address_objects"
	}
	return "calendar_objects"
}

// ETagOf returns the entity tag of an object body.
func ETagOf(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:12])
}

var slugRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._@+-]{0,127}$`)

// ValidSlug reports whether s can be used as collection or resource name.
func ValidSlug(s string) bool { return slugRe.MatchString(s) && s != "." && s != ".." }

// ---- collections ----

const collCols = `id, account_id, kind, slug, name, description, color, components, sync_seq, created_at`

func scanColl(sc interface{ Scan(...any) error }) (*Collection, error) {
	var c Collection
	var comps string
	var created int64
	if err := sc.Scan(&c.ID, &c.AccountID, &c.Kind, &c.Slug, &c.Name, &c.Description, &c.Color, &comps, &c.SyncSeq, &created); err != nil {
		return nil, err
	}
	c.Components = []string{}
	for _, x := range strings.Split(comps, ",") {
		if x = strings.TrimSpace(x); x != "" {
			c.Components = append(c.Components, x)
		}
	}
	c.CreatedAt = ts(created)
	return &c, nil
}

// EnsureDefaultCollections creates the default calendar "Kalender" (events
// and tasks) and address book "Kontakte" for accounts without any.
func (s *Store) EnsureDefaultCollections(ctx context.Context, accountID int64) error {
	return s.tx(ctx, func(tx *sql.Tx) error { return ensureDefaults(ctx, tx, accountID) })
}

func ensureDefaults(ctx context.Context, tx *sql.Tx, accountID int64) error {
	for _, d := range []struct{ kind, name, color, comps string }{
		{KindCalendar, "Kalender", DefaultCalendarColor, "VEVENT,VTODO"},
		{KindAddressBook, "Kontakte", "", ""},
	} {
		var n int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM dav_collections WHERE account_id = ? AND kind = ?`, accountID, d.kind).Scan(&n); err != nil {
			return err
		}
		if n > 0 {
			continue
		}
		if _, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO dav_collections(account_id, kind, slug, name, color, components, sync_seq, created_at)
			VALUES (?,?,?,?,?,?,1,?)`, accountID, d.kind, "default", d.name, d.color, d.comps, now()); err != nil {
			return err
		}
	}
	return nil
}

// ListCollections returns the account's collections of one kind, creating
// the defaults on first use.
func (s *Store) ListCollections(ctx context.Context, accountID int64, kind string) ([]*Collection, error) {
	if err := s.EnsureDefaultCollections(ctx, accountID); err != nil {
		return nil, err
	}
	rows, err := s.DB.QueryContext(ctx, `SELECT `+collCols+` FROM dav_collections WHERE account_id = ? AND kind = ? ORDER BY id`, accountID, kind)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*Collection{}
	for rows.Next() {
		c, err := scanColl(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

func (s *Store) GetCollection(ctx context.Context, accountID, id int64) (*Collection, error) {
	c, err := scanColl(s.DB.QueryRowContext(ctx, `SELECT `+collCols+` FROM dav_collections WHERE id = ? AND account_id = ?`, id, accountID))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return c, err
}

func (s *Store) GetCollectionBySlug(ctx context.Context, accountID int64, kind, slug string) (*Collection, error) {
	c, err := scanColl(s.DB.QueryRowContext(ctx, `SELECT `+collCols+` FROM dav_collections WHERE account_id = ? AND kind = ? AND slug = ?`, accountID, kind, slug))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return c, err
}

// CreateCollection inserts a collection. An empty slug is generated.
func (s *Store) CreateCollection(ctx context.Context, c *Collection) error {
	if c.Slug == "" {
		c.Slug = strings.ReplaceAll(pim.NewUID(), "-", "")[:16]
	}
	if !ValidSlug(c.Slug) {
		return fmt.Errorf("invalid collection name %q", c.Slug)
	}
	if c.Kind == KindCalendar && len(c.Components) == 0 {
		c.Components = []string{"VEVENT", "VTODO"}
	}
	if c.Kind == KindCalendar && c.Color == "" {
		c.Color = DefaultCalendarColor
	}
	res, err := s.DB.ExecContext(ctx, `INSERT INTO dav_collections(account_id, kind, slug, name, description, color, components, sync_seq, created_at)
		VALUES (?,?,?,?,?,?,?,1,?)`, c.AccountID, c.Kind, c.Slug, c.Name, c.Description, c.Color, strings.Join(c.Components, ","), now())
	if err != nil {
		if strings.Contains(err.Error(), "UNIQUE") {
			return ErrConflict
		}
		return err
	}
	c.ID, _ = res.LastInsertId()
	c.SyncSeq = 1
	c.CreatedAt = time.Now().UTC()
	return nil
}

// UpdateCollection stores name, description and colour.
func (s *Store) UpdateCollection(ctx context.Context, c *Collection) error {
	_, err := s.DB.ExecContext(ctx, `UPDATE dav_collections SET name = ?, description = ?, color = ?, sync_seq = sync_seq + 1 WHERE id = ? AND account_id = ?`,
		c.Name, c.Description, c.Color, c.ID, c.AccountID)
	if err == nil {
		c.SyncSeq++
	}
	return err
}

// DeleteCollection removes a collection with all its objects.
func (s *Store) DeleteCollection(ctx context.Context, accountID, id int64) error {
	res, err := s.DB.ExecContext(ctx, `DELETE FROM dav_collections WHERE id = ? AND account_id = ?`, id, accountID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// CountObjects returns the number of objects in a collection.
func (s *Store) CountObjects(ctx context.Context, c *Collection) int {
	var n int
	_ = s.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM `+objTable(c.Kind)+` WHERE collection_id = ?`, c.ID).Scan(&n)
	return n
}

// ---- objects ----

func objCols(kind string) string {
	if kind == KindAddressBook {
		return `id, collection_id, account_id, name, uid, 'VCARD', data, etag, modseq, NULL, created_at, updated_at`
	}
	return `id, collection_id, account_id, name, uid, component, data, etag, modseq, source_message_id, created_at, updated_at`
}

func scanObj(sc interface{ Scan(...any) error }) (*DavObject, error) {
	var o DavObject
	var src sql.NullInt64
	var created, updated int64
	if err := sc.Scan(&o.ID, &o.CollectionID, &o.AccountID, &o.Name, &o.UID, &o.Component, &o.Data, &o.ETag, &o.ModSeq, &src, &created, &updated); err != nil {
		return nil, err
	}
	if src.Valid {
		o.SourceMessageID = &src.Int64
	}
	o.CreatedAt, o.UpdatedAt = ts(created), ts(updated)
	return &o, nil
}

func (s *Store) queryObjs(ctx context.Context, kind, where string, args ...any) ([]*DavObject, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT `+objCols(kind)+` FROM `+objTable(kind)+` WHERE `+where, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*DavObject{}
	for rows.Next() {
		o, err := scanObj(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, o)
	}
	return out, rows.Err()
}

// ListObjects returns all objects of a collection.
func (s *Store) ListObjects(ctx context.Context, c *Collection) ([]*DavObject, error) {
	return s.queryObjs(ctx, c.Kind, `collection_id = ? ORDER BY name`, c.ID)
}

// GetObject returns one object by resource name.
func (s *Store) GetObject(ctx context.Context, c *Collection, name string) (*DavObject, error) {
	objs, err := s.queryObjs(ctx, c.Kind, `collection_id = ? AND name = ?`, c.ID, name)
	if err != nil {
		return nil, err
	}
	if len(objs) == 0 {
		return nil, ErrNotFound
	}
	return objs[0], nil
}

// GetObjectByUID returns the object of a collection with the given UID.
func (s *Store) GetObjectByUID(ctx context.Context, c *Collection, uid string) (*DavObject, error) {
	objs, err := s.queryObjs(ctx, c.Kind, `collection_id = ? AND uid = ?`, c.ID, uid)
	if err != nil {
		return nil, err
	}
	if len(objs) == 0 {
		return nil, ErrNotFound
	}
	return objs[0], nil
}

// GetObjectByID returns an object of the account by its numeric ID.
func (s *Store) GetObjectByID(ctx context.Context, accountID int64, kind string, id int64) (*DavObject, error) {
	objs, err := s.queryObjs(ctx, kind, `id = ? AND account_id = ?`, id, accountID)
	if err != nil {
		return nil, err
	}
	if len(objs) == 0 {
		return nil, ErrNotFound
	}
	return objs[0], nil
}

// PutOptions carries the HTTP preconditions of a write.
type PutOptions struct {
	IfMatch     string // ETag the client expects (without quotes); "*" = must exist
	IfNoneMatch bool   // "If-None-Match: *": must not exist
	// SourceMessageID links a new calendar object to a mail.
	SourceMessageID *int64
}

// objectIndex computes the index columns of an object body and validates it
// against the collection.
func objectIndex(c *Collection, data []byte) (uid, comp string, cols []any, err error) {
	if c.Kind == KindAddressBook {
		card, err := pim.ParseCard(data)
		if err != nil {
			return "", "", nil, err
		}
		m := pim.CardInfo(card)
		if m.UID == "" {
			return "", "", nil, fmt.Errorf("%w: UID missing", pim.ErrInvalidCard)
		}
		return m.UID, "VCARD", []any{m.FN, strings.Join(m.Emails, " "), m.Search}, nil
	}
	cal, err := pim.ParseCalendar(data)
	if err != nil {
		return "", "", nil, err
	}
	m, err := pim.Validate(cal)
	if err != nil {
		return "", "", nil, err
	}
	if !c.Supports(m.Component) {
		return "", "", nil, fmt.Errorf("%w: component %s not supported by this calendar", pim.ErrInvalid, m.Component)
	}
	return m.UID, m.Component, []any{m.Summary, m.Start, m.End}, nil
}

// PutObject creates or replaces an object and reports whether it was new.
func (s *Store) PutObject(ctx context.Context, c *Collection, name string, data []byte, opts PutOptions) (*DavObject, bool, error) {
	uid, comp, cols, err := objectIndex(c, data)
	if err != nil {
		return nil, false, err
	}
	etag := ETagOf(data)
	var obj *DavObject
	created := false
	err = s.tx(ctx, func(tx *sql.Tx) error {
		var id int64
		var oldETag string
		err := tx.QueryRowContext(ctx, `SELECT id, etag FROM `+objTable(c.Kind)+` WHERE collection_id = ? AND name = ?`, c.ID, name).Scan(&id, &oldETag)
		exists := err == nil
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		switch {
		case opts.IfNoneMatch && exists:
			return ErrPrecondition
		case opts.IfMatch == "*" && !exists:
			return ErrPrecondition
		case opts.IfMatch != "" && opts.IfMatch != "*" && (!exists || opts.IfMatch != oldETag):
			return ErrPrecondition
		}
		var other string
		err = tx.QueryRowContext(ctx, `SELECT name FROM `+objTable(c.Kind)+` WHERE collection_id = ? AND uid = ? AND name <> ?`, c.ID, uid, name).Scan(&other)
		if err == nil {
			return ErrUIDConflict
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		seq, err := bumpSeq(ctx, tx, c.ID)
		if err != nil {
			return err
		}
		t := now()
		if exists {
			if c.Kind == KindAddressBook {
				_, err = tx.ExecContext(ctx, `UPDATE address_objects SET uid = ?, data = ?, etag = ?, modseq = ?, fn = ?, emails = ?, search = ?, updated_at = ? WHERE id = ?`,
					append(append([]any{uid, data, etag, seq}, cols...), t, id)...)
			} else {
				_, err = tx.ExecContext(ctx, `UPDATE calendar_objects SET uid = ?, component = ?, data = ?, etag = ?, modseq = ?, summary = ?, start_ts = ?, end_ts = ?, updated_at = ? WHERE id = ?`,
					append(append([]any{uid, comp, data, etag, seq}, cols...), t, id)...)
			}
			if err != nil {
				return err
			}
		} else {
			var res sql.Result
			if c.Kind == KindAddressBook {
				res, err = tx.ExecContext(ctx, `INSERT INTO address_objects(collection_id, account_id, name, uid, data, etag, modseq, fn, emails, search, created_at, updated_at)
					VALUES (?,?,?,?,?,?,?,?,?,?,?,?)`, append(append([]any{c.ID, c.AccountID, name, uid, data, etag, seq}, cols...), t, t)...)
			} else {
				res, err = tx.ExecContext(ctx, `INSERT INTO calendar_objects(collection_id, account_id, name, uid, component, data, etag, modseq, summary, start_ts, end_ts, source_message_id, created_at, updated_at)
					VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, append(append([]any{c.ID, c.AccountID, name, uid, comp, data, etag, seq}, cols...), opts.SourceMessageID, t, t)...)
			}
			if err != nil {
				return err
			}
			id, _ = res.LastInsertId()
			created = true
			if _, err := tx.ExecContext(ctx, `DELETE FROM dav_tombstones WHERE collection_id = ? AND name = ?`, c.ID, name); err != nil {
				return err
			}
		}
		row := tx.QueryRowContext(ctx, `SELECT `+objCols(c.Kind)+` FROM `+objTable(c.Kind)+` WHERE id = ?`, id)
		obj, err = scanObj(row)
		return err
	})
	if err != nil {
		return nil, false, err
	}
	c.SyncSeq = obj.ModSeq
	return obj, created, nil
}

func bumpSeq(ctx context.Context, tx *sql.Tx, collID int64) (int64, error) {
	var seq int64
	err := tx.QueryRowContext(ctx, `UPDATE dav_collections SET sync_seq = sync_seq + 1 WHERE id = ? RETURNING sync_seq`, collID).Scan(&seq)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, ErrNotFound
	}
	return seq, err
}

// DeleteObject removes an object and leaves a tombstone for sync clients.
func (s *Store) DeleteObject(ctx context.Context, c *Collection, name, ifMatch string) error {
	return s.tx(ctx, func(tx *sql.Tx) error {
		var etag string
		err := tx.QueryRowContext(ctx, `SELECT etag FROM `+objTable(c.Kind)+` WHERE collection_id = ? AND name = ?`, c.ID, name).Scan(&etag)
		if errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		if ifMatch != "" && ifMatch != "*" && ifMatch != etag {
			return ErrPrecondition
		}
		seq, err := bumpSeq(ctx, tx, c.ID)
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM `+objTable(c.Kind)+` WHERE collection_id = ? AND name = ?`, c.ID, name); err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, `INSERT INTO dav_tombstones(collection_id, name, modseq) VALUES (?,?,?)
			ON CONFLICT(collection_id, name) DO UPDATE SET modseq = excluded.modseq`, c.ID, name, seq)
		c.SyncSeq = seq
		return err
	})
}

// Changes returns the objects changed and the resource names deleted after
// sequence number since (0: everything, no deletions).
func (s *Store) Changes(ctx context.Context, c *Collection, since int64) ([]*DavObject, []string, error) {
	changed, err := s.queryObjs(ctx, c.Kind, `collection_id = ? AND modseq > ? ORDER BY modseq`, c.ID, since)
	if err != nil {
		return nil, nil, err
	}
	deleted := []string{}
	if since > 0 {
		rows, err := s.DB.QueryContext(ctx, `SELECT name FROM dav_tombstones WHERE collection_id = ? AND modseq > ? ORDER BY modseq`, c.ID, since)
		if err != nil {
			return nil, nil, err
		}
		defer rows.Close()
		for rows.Next() {
			var n string
			if err := rows.Scan(&n); err != nil {
				return nil, nil, err
			}
			deleted = append(deleted, n)
		}
		if err := rows.Err(); err != nil {
			return nil, nil, err
		}
	}
	return changed, deleted, nil
}

// ListCalendarObjects returns the account's objects of one component type
// whose time range overlaps [from, to) (nil bounds are open). Objects
// without times are included only when both bounds are nil. collectionID 0
// means all calendars.
func (s *Store) ListCalendarObjects(ctx context.Context, accountID int64, component string, collectionID int64, from, to *int64) ([]*DavObject, error) {
	q := `account_id = ? AND component = ?`
	args := []any{accountID, component}
	if collectionID > 0 {
		q += ` AND collection_id = ?`
		args = append(args, collectionID)
	}
	if to != nil {
		q += ` AND start_ts IS NOT NULL AND start_ts < ?`
		args = append(args, *to)
	}
	if from != nil {
		q += ` AND start_ts IS NOT NULL AND (end_ts IS NULL OR end_ts >= ?)`
		args = append(args, *from)
	}
	return s.queryObjs(ctx, KindCalendar, q+` ORDER BY start_ts, id`, args...)
}

// ListAddressObjects returns the account's contacts, optionally limited to
// one address book and a search term.
func (s *Store) ListAddressObjects(ctx context.Context, accountID, collectionID int64, query string, limit int) ([]*DavObject, error) {
	q := `account_id = ?`
	args := []any{accountID}
	if collectionID > 0 {
		q += ` AND collection_id = ?`
		args = append(args, collectionID)
	}
	for _, term := range strings.Fields(strings.ToLower(query)) {
		q += ` AND instr(search, ?) > 0`
		args = append(args, term)
	}
	q += ` ORDER BY fn COLLATE NOCASE, id`
	if limit > 0 {
		q += fmt.Sprintf(` LIMIT %d`, limit)
	}
	return s.queryObjs(ctx, KindAddressBook, q, args...)
}

// SetObjectSource links a calendar object to the mail it was created from.
func (s *Store) SetObjectSource(ctx context.Context, id int64, msgID *int64) error {
	_, err := s.DB.ExecContext(ctx, `UPDATE calendar_objects SET source_message_id = ? WHERE id = ?`, msgID, id)
	return err
}

// MoveObject moves an object to another collection of the same kind (WebDAV
// MOVE). The target name must be free unless overwrite is set.
func (s *Store) MoveObject(ctx context.Context, from *Collection, name string, to *Collection, newName string, overwrite bool) (bool, error) {
	obj, err := s.GetObject(ctx, from, name)
	if err != nil {
		return false, err
	}
	if from.ID == to.ID && name == newName {
		return false, nil
	}
	_, err = s.GetObject(ctx, to, newName)
	existed := err == nil
	if existed && !overwrite {
		return false, ErrPrecondition
	}
	opts := PutOptions{SourceMessageID: obj.SourceMessageID}
	if _, _, err := s.PutObject(ctx, to, newName, obj.Data, opts); err != nil {
		return false, err
	}
	if err := s.DeleteObject(ctx, from, name, ""); err != nil {
		return false, err
	}
	return !existed, nil
}

// RecentRecipients returns addresses the account recently sent mail to,
// most recent first.
func (s *Store) RecentRecipients(ctx context.Context, accountID int64, limit int) ([]Address, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT m.to_json, m.cc_json FROM messages m JOIN mailboxes b ON b.id = m.mailbox_id
		WHERE m.account_id = ? AND b.role = 'sent' ORDER BY m.received_at DESC LIMIT 500`, accountID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	seen := map[string]bool{}
	out := []Address{}
	for rows.Next() {
		var to, cc string
		if err := rows.Scan(&to, &cc); err != nil {
			return nil, err
		}
		for _, raw := range []string{to, cc} {
			for _, a := range parseAddrs(raw) {
				e := Normalize(a.Email)
				if e == "" || seen[e] {
					continue
				}
				seen[e] = true
				out = append(out, Address{Name: a.Name, Email: e})
				if len(out) >= limit {
					return out, nil
				}
			}
		}
	}
	return out, rows.Err()
}

func parseAddrs(raw string) []Address {
	var out []Address
	_ = json.Unmarshal([]byte(raw), &out)
	return out
}

// ---- migration of the former events/tasks tables ----

// migrateLegacyCalendar converts rows of the old events and tasks tables into
// iCalendar objects in each account's default calendar. It runs once; the
// old tables are left untouched.
func migrateLegacyCalendar(ctx context.Context, db *sql.DB) error {
	var done string
	_ = db.QueryRowContext(ctx, `SELECT value FROM settings WHERE key = 'pim_legacy_migrated'`).Scan(&done)
	if done == "1" {
		return nil
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	type legacy struct {
		account          int64
		comp             string
		cal              []byte
		uid              string
		src              sql.NullInt64
		created, updated int64
	}
	var items []legacy
	rows, err := tx.QueryContext(ctx, `SELECT id, account_id, title, start, end_time, location, description, source_message_id FROM events`)
	if err != nil {
		return err
	}
	for rows.Next() {
		var id, acct int64
		var title, start string
		var end, loc, desc sql.NullString
		var src sql.NullInt64
		if err := rows.Scan(&id, &acct, &title, &start, &end, &loc, &desc, &src); err != nil {
			rows.Close()
			return err
		}
		uid := fmt.Sprintf("postwerk-event-%d@legacy", id)
		in := pim.EventInput{Title: title, Start: start, End: nullStr(end), Location: nullStr(loc), Description: nullStr(desc)}
		if in.End != nil {
			if _, err := pim.ParseWhen(*in.End, false); err != nil {
				in.End = nil
			}
		}
		cal, err := pim.NewEvent(uid, in)
		if err != nil {
			continue // unusable row (e.g. unparseable start); keep it in the old table
		}
		data, err := pim.EncodeCalendar(cal)
		if err != nil {
			continue
		}
		items = append(items, legacy{account: acct, comp: "VEVENT", cal: data, uid: uid, src: src, created: now(), updated: now()})
	}
	rows.Close()
	rows, err = tx.QueryContext(ctx, `SELECT id, account_id, title, due, done, source_message_id, created_at FROM tasks`)
	if err != nil {
		return err
	}
	for rows.Next() {
		var id, acct, created int64
		var title string
		var due sql.NullString
		var isDone bool
		var src sql.NullInt64
		if err := rows.Scan(&id, &acct, &title, &due, &isDone, &src, &created); err != nil {
			rows.Close()
			return err
		}
		uid := fmt.Sprintf("postwerk-task-%d@legacy", id)
		in := pim.TaskInput{Title: &title, Done: &isDone}
		if d := nullStr(due); d != nil {
			if _, err := pim.ParseWhen(*d, false); err == nil {
				in.Due = d
			}
		}
		cal, err := pim.NewTask(uid, in)
		if err != nil {
			continue
		}
		data, err := pim.EncodeCalendar(cal)
		if err != nil {
			continue
		}
		items = append(items, legacy{account: acct, comp: "VTODO", cal: data, uid: uid, src: src, created: created, updated: now()})
	}
	rows.Close()
	for _, it := range items {
		if err := ensureDefaults(ctx, tx, it.account); err != nil {
			return err
		}
		var collID int64
		if err := tx.QueryRowContext(ctx, `SELECT id FROM dav_collections WHERE account_id = ? AND kind = 'calendar' ORDER BY id LIMIT 1`, it.account).Scan(&collID); err != nil {
			return err
		}
		coll := &Collection{ID: collID, Kind: KindCalendar, Components: []string{"VEVENT", "VTODO"}}
		_, _, cols, err := objectIndex(coll, it.cal)
		if err != nil {
			continue
		}
		seq, err := bumpSeq(ctx, tx, collID)
		if err != nil {
			return err
		}
		var src any
		if it.src.Valid {
			src = it.src.Int64
		}
		if _, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO calendar_objects(collection_id, account_id, name, uid, component, data, etag, modseq, summary, start_ts, end_ts, source_message_id, created_at, updated_at)
			VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, append(append([]any{collID, it.account, it.uid + ".ics", it.uid, it.comp, it.cal, ETagOf(it.cal), seq}, cols...), src, it.created, it.updated)...); err != nil {
			return err
		}
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO settings(key, value) VALUES ('pim_legacy_migrated', '1') ON CONFLICT(key) DO UPDATE SET value = '1'`); err != nil {
		return err
	}
	return tx.Commit()
}
