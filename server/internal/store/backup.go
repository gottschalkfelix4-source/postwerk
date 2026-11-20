package store

import (
	"context"
	"database/sql"
	"time"
)

// BackupRun is one entry of the backup history.
type BackupRun struct {
	ID         int64      `json:"id"`
	Trigger    string     `json:"trigger"` // manual | schedule | restore
	Status     string     `json:"status"`  // running | success | failed
	Archive    string     `json:"archive"`
	Target     string     `json:"target"`
	Size       int64      `json:"size"`
	Files      int        `json:"files"`
	Error      string     `json:"error"`
	StartedAt  time.Time  `json:"startedAt"`
	FinishedAt *time.Time `json:"finishedAt"`
}

func (s *Store) StartBackupRun(ctx context.Context, trigger, target string) (int64, error) {
	res, err := s.DB.ExecContext(ctx, `INSERT INTO backup_runs(trigger, status, target, started_at) VALUES (?, 'running', ?, ?)`, trigger, target, now())
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

func (s *Store) FinishBackupRun(ctx context.Context, id int64, archive string, size int64, files int, runErr error) error {
	status, msg := "success", ""
	if runErr != nil {
		status, msg = "failed", runErr.Error()
	}
	_, err := s.DB.ExecContext(ctx, `UPDATE backup_runs SET status = ?, archive = ?, size = ?, files = ?, error = ?, finished_at = ? WHERE id = ?`,
		status, archive, size, files, msg, now(), id)
	return err
}

// FailStaleBackupRuns marks runs that were interrupted by a restart.
func (s *Store) FailStaleBackupRuns(ctx context.Context) error {
	_, err := s.DB.ExecContext(ctx, `UPDATE backup_runs SET status = 'failed', error = 'Durch Neustart unterbrochen', finished_at = ? WHERE status = 'running'`, now())
	return err
}

func (s *Store) BackupRuns(ctx context.Context, limit int) ([]BackupRun, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT id, trigger, status, archive, target, size, files, error, started_at, finished_at
		FROM backup_runs ORDER BY id DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []BackupRun{}
	for rows.Next() {
		var r BackupRun
		var started int64
		var finished sql.NullInt64
		if err := rows.Scan(&r.ID, &r.Trigger, &r.Status, &r.Archive, &r.Target, &r.Size, &r.Files, &r.Error, &started, &finished); err != nil {
			return nil, err
		}
		r.StartedAt = ts(started)
		if finished.Valid {
			t := ts(finished.Int64)
			r.FinishedAt = &t
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// LastScheduledBackup returns the start time of the latest scheduled run.
func (s *Store) LastScheduledBackup(ctx context.Context) (time.Time, error) {
	var t sql.NullInt64
	err := s.DB.QueryRowContext(ctx, `SELECT MAX(started_at) FROM backup_runs WHERE trigger = 'schedule'`).Scan(&t)
	if err != nil || !t.Valid {
		return time.Time{}, err
	}
	return ts(t.Int64), nil
}

// BackupCounts returns figures for the archive manifest.
func (s *Store) BackupCounts(ctx context.Context) (domains, accounts, messages int, err error) {
	err = s.DB.QueryRowContext(ctx, `SELECT (SELECT COUNT(*) FROM domains), (SELECT COUNT(*) FROM accounts), (SELECT COUNT(*) FROM messages)`).
		Scan(&domains, &accounts, &messages)
	return
}
