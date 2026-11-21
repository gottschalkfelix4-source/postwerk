package store

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

// DataExport is a GDPR export of one account (ZIP in data/exports).
type DataExport struct {
	ID          int64      `json:"id"`
	AccountID   int64      `json:"accountId"`
	RequestedBy int64      `json:"requestedBy"`
	Status      string     `json:"status"` // pending | running | done | failed | expired
	Phase       string     `json:"phase"`
	Done        int64      `json:"done"`
	Total       int64      `json:"total"`
	File        string     `json:"-"`
	Size        int64      `json:"size"`
	Error       string     `json:"error,omitempty"`
	CreatedAt   time.Time  `json:"createdAt"`
	FinishedAt  *time.Time `json:"finishedAt"`
	ExpiresAt   *time.Time `json:"expiresAt"`
}

const exportCols = `id, account_id, requested_by, status, phase, done, total, file, size, error, created_at, finished_at, expires_at`

func scanExport(sc interface{ Scan(...any) error }) (*DataExport, error) {
	var e DataExport
	var created int64
	var finished, expires sql.NullInt64
	if err := sc.Scan(&e.ID, &e.AccountID, &e.RequestedBy, &e.Status, &e.Phase, &e.Done, &e.Total, &e.File, &e.Size, &e.Error, &created, &finished, &expires); err != nil {
		return nil, err
	}
	e.CreatedAt = ts(created)
	e.FinishedAt = nullTime(finished)
	e.ExpiresAt = nullTime(expires)
	return &e, nil
}

// CreateDataExport inserts a pending export.
func (s *Store) CreateDataExport(ctx context.Context, accountID, requestedBy int64) (*DataExport, error) {
	res, err := s.DB.ExecContext(ctx, `INSERT INTO data_exports(account_id, requested_by, status, created_at) VALUES (?,?,'pending',?)`, accountID, requestedBy, now())
	if err != nil {
		return nil, err
	}
	id, _ := res.LastInsertId()
	return s.GetDataExport(ctx, id)
}

// GetDataExport loads an export by ID.
func (s *Store) GetDataExport(ctx context.Context, id int64) (*DataExport, error) {
	e, err := scanExport(s.DB.QueryRowContext(ctx, `SELECT `+exportCols+` FROM data_exports WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return e, err
}

// ListDataExports returns the account's exports, newest first. With
// requestedBy > 0 only exports requested by that account are listed.
func (s *Store) ListDataExports(ctx context.Context, accountID, requestedBy int64) ([]*DataExport, error) {
	q := `SELECT ` + exportCols + ` FROM data_exports WHERE account_id = ?`
	args := []any{accountID}
	if requestedBy > 0 {
		q += ` AND requested_by = ?`
		args = append(args, requestedBy)
	}
	rows, err := s.DB.QueryContext(ctx, q+` ORDER BY created_at DESC, id DESC LIMIT 20`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*DataExport{}
	for rows.Next() {
		e, err := scanExport(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// LastDataExport returns the newest export of an account requested by
// requestedBy (ErrNotFound if none).
func (s *Store) LastDataExport(ctx context.Context, accountID, requestedBy int64) (*DataExport, error) {
	e, err := scanExport(s.DB.QueryRowContext(ctx, `SELECT `+exportCols+` FROM data_exports WHERE account_id = ? AND requested_by = ?
		ORDER BY created_at DESC, id DESC LIMIT 1`, accountID, requestedBy))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return e, err
}

// SaveDataExport persists the progress fields of an export.
func (s *Store) SaveDataExport(ctx context.Context, e *DataExport) error {
	var finished, expires any
	if e.FinishedAt != nil {
		finished = e.FinishedAt.Unix()
	}
	if e.ExpiresAt != nil {
		expires = e.ExpiresAt.Unix()
	}
	_, err := s.DB.ExecContext(ctx, `UPDATE data_exports SET status = ?, phase = ?, done = ?, total = ?, file = ?, size = ?, error = ?, finished_at = ?, expires_at = ?
		WHERE id = ?`, e.Status, e.Phase, e.Done, e.Total, e.File, e.Size, e.Error, finished, expires, e.ID)
	return err
}

// ExpiredDataExports lists finished exports whose download expired, and
// exports still marked running (interrupted by a restart).
func (s *Store) ExpiredDataExports(ctx context.Context, at time.Time) ([]*DataExport, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT `+exportCols+` FROM data_exports WHERE status = 'done' AND expires_at IS NOT NULL AND expires_at <= ?`, at.Unix())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*DataExport
	for rows.Next() {
		e, err := scanExport(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// FailInterruptedExports marks exports that were running when the server
// stopped as failed.
func (s *Store) FailInterruptedExports(ctx context.Context) error {
	_, err := s.DB.ExecContext(ctx, `UPDATE data_exports SET status = 'failed', error = 'Abgebrochen durch Neustart des Servers', finished_at = ?
		WHERE status IN ('pending','running')`, now())
	return err
}

// ExportFiles returns the file names referenced by exports that can still
// be downloaded or are being written.
func (s *Store) ExportFiles(ctx context.Context) (map[string]bool, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT file FROM data_exports WHERE file != '' AND status IN ('pending','running','done')`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]bool{}
	for rows.Next() {
		var f string
		if err := rows.Scan(&f); err != nil {
			return nil, err
		}
		out[f] = true
	}
	return out, rows.Err()
}
