package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"
)

// ---- outbound queue ----

type QueueItem struct {
	ID          int64     `json:"id"`
	From        string    `json:"from"`
	To          []string  `json:"to"`
	BlobHash    string    `json:"-"`
	Size        int64     `json:"size"`
	Attempts    int       `json:"attempts"`
	NextAttempt time.Time `json:"nextAttempt"`
	LastError   *string   `json:"lastError"`
	CreatedAt   time.Time `json:"createdAt"`
}

func scanQueue(sc interface{ Scan(...any) error }) (*QueueItem, error) {
	var q QueueItem
	var rcpts string
	var next, created int64
	var lastErr sql.NullString
	if err := sc.Scan(&q.ID, &q.From, &rcpts, &q.BlobHash, &q.Size, &q.Attempts, &next, &lastErr, &created); err != nil {
		return nil, err
	}
	_ = json.Unmarshal([]byte(rcpts), &q.To)
	q.NextAttempt = ts(next)
	q.CreatedAt = ts(created)
	q.LastError = nullStr(lastErr)
	return &q, nil
}

const queueCols = `id, from_addr, rcpts_json, blob_hash, size, attempts, next_attempt, last_error, created_at`

func (s *Store) Enqueue(ctx context.Context, from string, rcpts []string, blobHash string, size int64) (int64, error) {
	res, err := s.DB.ExecContext(ctx, `INSERT INTO queue(from_addr, rcpts_json, blob_hash, size, next_attempt, created_at) VALUES (?,?,?,?,?,?)`,
		from, js(rcpts), blobHash, size, now(), now())
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

func (s *Store) ListQueue(ctx context.Context) ([]*QueueItem, error) {
	return s.queryQueue(ctx, `SELECT `+queueCols+` FROM queue ORDER BY next_attempt`)
}

func (s *Store) DueQueue(ctx context.Context) ([]*QueueItem, error) {
	return s.queryQueue(ctx, `SELECT `+queueCols+` FROM queue WHERE next_attempt <= ? ORDER BY next_attempt LIMIT 50`, now())
}

func (s *Store) queryQueue(ctx context.Context, q string, args ...any) ([]*QueueItem, error) {
	rows, err := s.DB.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*QueueItem{}
	for rows.Next() {
		it, err := scanQueue(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, it)
	}
	return out, rows.Err()
}

func (s *Store) GetQueueItem(ctx context.Context, id int64) (*QueueItem, error) {
	it, err := scanQueue(s.DB.QueryRowContext(ctx, `SELECT `+queueCols+` FROM queue WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return it, err
}

func (s *Store) UpdateQueueItem(ctx context.Context, id int64, rcpts []string, attempts int, next time.Time, lastErr string) error {
	_, err := s.DB.ExecContext(ctx, `UPDATE queue SET rcpts_json = ?, attempts = ?, next_attempt = ?, last_error = ? WHERE id = ?`,
		js(rcpts), attempts, next.Unix(), lastErr, id)
	return err
}

func (s *Store) RetryQueueItem(ctx context.Context, id int64) error {
	res, err := s.DB.ExecContext(ctx, `UPDATE queue SET next_attempt = ? WHERE id = ?`, now(), id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *Store) DeleteQueueItem(ctx context.Context, id int64) error {
	it, err := s.GetQueueItem(ctx, id)
	if err != nil {
		return err
	}
	if _, err := s.DB.ExecContext(ctx, `DELETE FROM queue WHERE id = ?`, id); err != nil {
		return err
	}
	s.releaseBlob(ctx, it.BlobHash)
	return nil
}

func (s *Store) QueueSize(ctx context.Context) int {
	var n int
	_ = s.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM queue`).Scan(&n)
	return n
}

// ---- calendar & tasks ----

type CalendarEvent struct {
	ID              int64   `json:"id"`
	Title           string  `json:"title"`
	Start           string  `json:"start"`
	End             *string `json:"end"`
	Location        *string `json:"location"`
	Description     *string `json:"description"`
	SourceMessageID *int64  `json:"sourceMessageId"`
}

func (s *Store) ListEvents(ctx context.Context, accountID int64, from, to string) ([]CalendarEvent, error) {
	q := `SELECT id, title, start, end_time, location, description, source_message_id FROM events WHERE account_id = ?`
	args := []any{accountID}
	if from != "" {
		q += ` AND start >= ?`
		args = append(args, from)
	}
	if to != "" {
		q += ` AND start < ?`
		args = append(args, to)
	}
	rows, err := s.DB.QueryContext(ctx, q+` ORDER BY start`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []CalendarEvent{}
	for rows.Next() {
		var e CalendarEvent
		var end, loc, desc sql.NullString
		var src sql.NullInt64
		if err := rows.Scan(&e.ID, &e.Title, &e.Start, &end, &loc, &desc, &src); err != nil {
			return nil, err
		}
		e.End, e.Location, e.Description = nullStr(end), nullStr(loc), nullStr(desc)
		if src.Valid {
			e.SourceMessageID = &src.Int64
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

func (s *Store) CreateEvent(ctx context.Context, accountID int64, e *CalendarEvent) error {
	res, err := s.DB.ExecContext(ctx, `INSERT INTO events(account_id, title, start, end_time, location, description, source_message_id) VALUES (?,?,?,?,?,?,?)`,
		accountID, e.Title, e.Start, toNull(e.End), toNull(e.Location), toNull(e.Description), e.SourceMessageID)
	if err != nil {
		return err
	}
	e.ID, _ = res.LastInsertId()
	return nil
}

func (s *Store) DeleteEvent(ctx context.Context, accountID, id int64) error {
	res, err := s.DB.ExecContext(ctx, `DELETE FROM events WHERE id = ? AND account_id = ?`, id, accountID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

type Task struct {
	ID              int64     `json:"id"`
	Title           string    `json:"title"`
	Due             *string   `json:"due"`
	Done            bool      `json:"done"`
	SourceMessageID *int64    `json:"sourceMessageId"`
	CreatedAt       time.Time `json:"createdAt"`
}

func (s *Store) ListTasks(ctx context.Context, accountID int64) ([]Task, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT id, title, due, done, source_message_id, created_at FROM tasks WHERE account_id = ? ORDER BY done, COALESCE(due, '9999'), created_at DESC`, accountID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Task{}
	for rows.Next() {
		var t Task
		var due sql.NullString
		var src sql.NullInt64
		var created int64
		if err := rows.Scan(&t.ID, &t.Title, &due, &t.Done, &src, &created); err != nil {
			return nil, err
		}
		t.Due = nullStr(due)
		if src.Valid {
			t.SourceMessageID = &src.Int64
		}
		t.CreatedAt = ts(created)
		out = append(out, t)
	}
	return out, rows.Err()
}

func (s *Store) GetTask(ctx context.Context, accountID, id int64) (*Task, error) {
	all, err := s.ListTasks(ctx, accountID)
	if err != nil {
		return nil, err
	}
	for i := range all {
		if all[i].ID == id {
			return &all[i], nil
		}
	}
	return nil, ErrNotFound
}

func (s *Store) CreateTask(ctx context.Context, accountID int64, t *Task) error {
	res, err := s.DB.ExecContext(ctx, `INSERT INTO tasks(account_id, title, due, source_message_id, created_at) VALUES (?,?,?,?,?)`,
		accountID, t.Title, toNull(t.Due), t.SourceMessageID, now())
	if err != nil {
		return err
	}
	t.ID, _ = res.LastInsertId()
	t.CreatedAt = time.Now().UTC()
	return nil
}

func (s *Store) UpdateTask(ctx context.Context, accountID int64, t *Task) error {
	_, err := s.DB.ExecContext(ctx, `UPDATE tasks SET title = ?, due = ?, done = ? WHERE id = ? AND account_id = ?`, t.Title, toNull(t.Due), t.Done, t.ID, accountID)
	return err
}

func (s *Store) DeleteTask(ctx context.Context, accountID, id int64) error {
	res, err := s.DB.ExecContext(ctx, `DELETE FROM tasks WHERE id = ? AND account_id = ?`, id, accountID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// ---- vacation ----

type Vacation struct {
	Enabled bool    `json:"enabled"`
	Subject string  `json:"subject"`
	Body    string  `json:"body"`
	From    *string `json:"from"`
	Until   *string `json:"until"`
}

func (s *Store) GetVacation(ctx context.Context, accountID int64) (*Vacation, error) {
	var v Vacation
	var from, until sql.NullString
	err := s.DB.QueryRowContext(ctx, `SELECT enabled, subject, body, from_date, until_date FROM vacation WHERE account_id = ?`, accountID).Scan(&v.Enabled, &v.Subject, &v.Body, &from, &until)
	if errors.Is(err, sql.ErrNoRows) {
		return &Vacation{}, nil
	}
	if err != nil {
		return nil, err
	}
	v.From, v.Until = nullStr(from), nullStr(until)
	return &v, nil
}

func (s *Store) SetVacation(ctx context.Context, accountID int64, v *Vacation) error {
	_, err := s.DB.ExecContext(ctx, `INSERT INTO vacation(account_id, enabled, subject, body, from_date, until_date) VALUES (?,?,?,?,?,?)
		ON CONFLICT(account_id) DO UPDATE SET enabled = excluded.enabled, subject = excluded.subject, body = excluded.body,
		from_date = excluded.from_date, until_date = excluded.until_date`, accountID, v.Enabled, v.Subject, v.Body, toNull(v.From), toNull(v.Until))
	if err == nil {
		_, _ = s.DB.ExecContext(ctx, `DELETE FROM vacation_replies WHERE account_id = ?`, accountID)
	}
	return err
}

// VacationShouldReply records a reply to sender and reports whether one is due
// (at most one reply per sender every 7 days).
func (s *Store) VacationShouldReply(ctx context.Context, accountID int64, sender string) bool {
	var sent int64
	err := s.DB.QueryRowContext(ctx, `SELECT sent_at FROM vacation_replies WHERE account_id = ? AND sender = ?`, accountID, sender).Scan(&sent)
	if err == nil && time.Since(ts(sent)) < 7*24*time.Hour {
		return false
	}
	_, _ = s.DB.ExecContext(ctx, `INSERT INTO vacation_replies(account_id, sender, sent_at) VALUES (?,?,?)
		ON CONFLICT(account_id, sender) DO UPDATE SET sent_at = excluded.sent_at`, accountID, sender, now())
	return true
}

// ---- greylisting ----

// Greylist returns true if the triplet may pass.
func (s *Store) Greylist(ctx context.Context, key string, delay time.Duration) (bool, error) {
	var first int64
	var passed bool
	err := s.DB.QueryRowContext(ctx, `SELECT first_seen, passed FROM greylist WHERE key = ?`, key).Scan(&first, &passed)
	if errors.Is(err, sql.ErrNoRows) {
		_, err = s.DB.ExecContext(ctx, `INSERT INTO greylist(key, first_seen, last_seen) VALUES (?,?,?)`, key, now(), now())
		return false, err
	}
	if err != nil {
		return true, err
	}
	if passed || time.Since(ts(first)) >= delay {
		_, err = s.DB.ExecContext(ctx, `UPDATE greylist SET passed = 1, last_seen = ? WHERE key = ?`, now(), key)
		return true, err
	}
	return false, nil
}

// ---- bayes ----

func (s *Store) BayesCounts(ctx context.Context) (spamMsgs, hamMsgs int64) {
	_ = s.DB.QueryRowContext(ctx, `SELECT COALESCE((SELECT value FROM settings WHERE key='bayes_spam'),'0'), COALESCE((SELECT value FROM settings WHERE key='bayes_ham'),'0')`).Scan(&spamMsgs, &hamMsgs)
	return
}

func (s *Store) BayesTokens(ctx context.Context, tokens []string) (map[string][2]int64, error) {
	out := map[string][2]int64{}
	for _, t := range tokens {
		var sp, hm int64
		err := s.DB.QueryRowContext(ctx, `SELECT spam, ham FROM bayes_tokens WHERE token = ?`, t).Scan(&sp, &hm)
		if errors.Is(err, sql.ErrNoRows) {
			continue
		}
		if err != nil {
			return nil, err
		}
		out[t] = [2]int64{sp, hm}
	}
	return out, nil
}

func (s *Store) BayesTrain(ctx context.Context, tokens []string, spam bool, delta int) error {
	col, key := "ham", "bayes_ham"
	if spam {
		col, key = "spam", "bayes_spam"
	}
	return s.tx(ctx, func(tx *sql.Tx) error {
		for _, t := range tokens {
			if _, err := tx.ExecContext(ctx, `INSERT INTO bayes_tokens(token, `+col+`) VALUES (?, MAX(?,0))
				ON CONFLICT(token) DO UPDATE SET `+col+` = MAX(`+col+` + ?, 0)`, t, delta, delta); err != nil {
				return err
			}
		}
		_, err := tx.ExecContext(ctx, `INSERT INTO settings(key, value) VALUES (?, MAX(?,0))
			ON CONFLICT(key) DO UPDATE SET value = MAX(CAST(value AS INTEGER) + ?, 0)`, key, delta, delta)
		return err
	})
}

// ---- stats ----

func (s *Store) IncStat(ctx context.Context, metric string, n int) {
	hour := time.Now().Unix() / 3600
	_, _ = s.DB.ExecContext(ctx, `INSERT INTO stats(hour, metric, value) VALUES (?,?,?)
		ON CONFLICT(hour, metric) DO UPDATE SET value = value + excluded.value`, hour, metric, n)
}

// StatSeries returns hourly values for the last n hours, oldest first.
func (s *Store) StatSeries(ctx context.Context, metric string, hours int) ([]int, []time.Time) {
	cur := time.Now().Unix() / 3600
	vals := make([]int, hours)
	times := make([]time.Time, hours)
	for i := range times {
		times[i] = time.Unix((cur-int64(hours-1-i))*3600, 0).UTC()
	}
	rows, err := s.DB.QueryContext(ctx, `SELECT hour, value FROM stats WHERE metric = ? AND hour > ?`, metric, cur-int64(hours))
	if err != nil {
		return vals, times
	}
	defer rows.Close()
	for rows.Next() {
		var h int64
		var v int
		rows.Scan(&h, &v)
		idx := hours - 1 - int(cur-h)
		if idx >= 0 && idx < hours {
			vals[idx] = v
		}
	}
	return vals, times
}

func Sum(v []int) int {
	t := 0
	for _, x := range v {
		t += x
	}
	return t
}
