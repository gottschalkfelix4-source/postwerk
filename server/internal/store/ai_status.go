package store

import (
	"context"
	"database/sql"
	"time"
)

// aiQueueWhere selects incoming mail the analysis still has to process; it
// matches PendingAIMessages without the retry-time condition.
const aiQueueWhere = `m.ai_done = 0 AND m.draft = 0 AND (b.role IS NULL OR b.role IN ('inbox','archive'))`

// AIQueueStats summarizes the analysis queue for the admin status.
type AIQueueStats struct {
	// Due messages are waiting for their first or next attempt now.
	Due int `json:"due"`
	// Waiting messages failed and wait for their scheduled retry.
	Waiting int `json:"waiting"`
	// Analyzed counts messages with a stored analysis.
	Analyzed int `json:"analyzed"`
	// Analyzed24h counts analyzed messages received in the last 24 hours.
	Analyzed24h int `json:"analyzed24h"`
	// Received24h counts incoming messages received in the last 24 hours.
	Received24h int `json:"received24h"`
}

// AIFailure is a message whose analysis failed at least once.
type AIFailure struct {
	ID            int64      `json:"id"`
	Account       string     `json:"account"`
	Subject       string     `json:"subject"`
	From          string     `json:"from"`
	ReceivedAt    time.Time  `json:"receivedAt"`
	Attempts      int        `json:"attempts"`
	LastError     string     `json:"lastError"`
	NextAttemptAt *time.Time `json:"nextAttemptAt"`
}

// AIQueueStats counts pending, failed and analyzed incoming messages.
func (s *Store) AIQueueStats(ctx context.Context) (AIQueueStats, error) {
	var st AIQueueStats
	now := time.Now()
	err := s.DB.QueryRowContext(ctx, `SELECT
		COALESCE(SUM(CASE WHEN `+aiQueueWhere+` AND (m.ai_next_attempt_at IS NULL OR m.ai_next_attempt_at <= ?) THEN 1 ELSE 0 END), 0),
		COALESCE(SUM(CASE WHEN `+aiQueueWhere+` AND m.ai_next_attempt_at > ? THEN 1 ELSE 0 END), 0),
		COALESCE(SUM(CASE WHEN m.ai_done = 1 THEN 1 ELSE 0 END), 0),
		COALESCE(SUM(CASE WHEN m.ai_done = 1 AND m.received_at >= ? THEN 1 ELSE 0 END), 0),
		COALESCE(SUM(CASE WHEN m.draft = 0 AND (b.role IS NULL OR b.role IN ('inbox','archive','junk')) AND m.received_at >= ? THEN 1 ELSE 0 END), 0)
		FROM messages m JOIN mailboxes b ON b.id = m.mailbox_id`,
		now.Unix(), now.Unix(), now.Add(-24*time.Hour).Unix(), now.Add(-24*time.Hour).Unix()).
		Scan(&st.Due, &st.Waiting, &st.Analyzed, &st.Analyzed24h, &st.Received24h)
	return st, err
}

// AIFailures lists unanalyzed messages with failed attempts, latest first.
func (s *Store) AIFailures(ctx context.Context, limit int) ([]AIFailure, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT m.id, a.email, m.subject, m.from_email, m.received_at,
		m.ai_attempts, m.ai_last_error, m.ai_next_attempt_at
		FROM messages m JOIN mailboxes b ON b.id = m.mailbox_id JOIN accounts a ON a.id = m.account_id
		WHERE `+aiQueueWhere+` AND m.ai_attempts > 0
		ORDER BY m.id DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []AIFailure{}
	for rows.Next() {
		var f AIFailure
		var received int64
		var next sql.NullInt64
		if err := rows.Scan(&f.ID, &f.Account, &f.Subject, &f.From, &received, &f.Attempts, &f.LastError, &next); err != nil {
			return nil, err
		}
		f.ReceivedAt = ts(received)
		f.NextAttemptAt = nullTime(next)
		out = append(out, f)
	}
	return out, rows.Err()
}

// ResetAIRetries makes all failed, unanalyzed messages due again and returns
// how many were reset.
func (s *Store) ResetAIRetries(ctx context.Context) (int64, error) {
	res, err := s.DB.ExecContext(ctx, `UPDATE messages SET ai_attempts = 0, ai_next_attempt_at = NULL, ai_last_error = ''
		WHERE ai_done = 0 AND (ai_attempts > 0 OR ai_next_attempt_at IS NOT NULL)`)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}
