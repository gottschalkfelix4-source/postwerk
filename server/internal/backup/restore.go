package backup

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// A restore is prepared while the server runs (archive unpacked into
// restore-staging and checked) and applied on the next start, before the
// database is opened. The previous data is kept in pre-restore-<time>.
const pendingFile = "restore-pending"

type pending struct {
	Archive   string    `json:"archive"`
	CreatedAt time.Time `json:"createdAt"`
}

func StagingDir(dataDir string) string { return filepath.Join(dataDir, "restore-staging") }

func markPending(dataDir, archive string) error {
	b, _ := json.Marshal(pending{Archive: archive, CreatedAt: time.Now().UTC()})
	return os.WriteFile(filepath.Join(dataDir, pendingFile), b, 0o640)
}

// keepOnRestore lists top-level entries that stay in place when a restore
// swaps the data directory.
func keepOnRestore(name string) bool {
	return name == "backups" || name == "restore-staging" || name == pendingFile || strings.HasPrefix(name, "pre-restore-")
}

// ApplyPending swaps in a prepared restore. It returns the archive name when
// a restore was applied. On failure the previous data is moved back.
func ApplyPending(dataDir string) (string, error) {
	raw, err := os.ReadFile(filepath.Join(dataDir, pendingFile))
	if errors.Is(err, fs.ErrNotExist) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	// Never loop on a broken restore: the marker goes first.
	_ = os.Remove(filepath.Join(dataDir, pendingFile))
	var p pending
	_ = json.Unmarshal(raw, &p)
	src := filepath.Join(StagingDir(dataDir), "data")
	if _, err := os.Stat(filepath.Join(src, dbName)); err != nil {
		return "", fmt.Errorf("vorbereitete Wiederherstellung unvollständig: %w", err)
	}
	pre := filepath.Join(dataDir, "pre-restore-"+time.Now().Format("20060102-150405"))
	if err := os.MkdirAll(pre, 0o750); err != nil {
		return "", err
	}
	current, err := os.ReadDir(dataDir)
	if err != nil {
		return "", err
	}
	var moved []string
	rollback := func() {
		entries, _ := os.ReadDir(dataDir)
		for _, e := range entries {
			if !keepOnRestore(e.Name()) {
				_ = os.RemoveAll(filepath.Join(dataDir, e.Name()))
			}
		}
		for _, name := range moved {
			_ = os.Rename(filepath.Join(pre, name), filepath.Join(dataDir, name))
		}
		_ = os.Remove(pre)
	}
	for _, e := range current {
		if keepOnRestore(e.Name()) {
			continue
		}
		if err := os.Rename(filepath.Join(dataDir, e.Name()), filepath.Join(pre, e.Name())); err != nil {
			rollback()
			return "", fmt.Errorf("alte Daten sichern: %w", err)
		}
		moved = append(moved, e.Name())
	}
	restored, err := os.ReadDir(src)
	if err != nil {
		rollback()
		return "", err
	}
	for _, e := range restored {
		if keepOnRestore(e.Name()) {
			continue
		}
		if err := os.Rename(filepath.Join(src, e.Name()), filepath.Join(dataDir, e.Name())); err != nil {
			rollback()
			return "", fmt.Errorf("daten einspielen: %w", err)
		}
	}
	_ = os.RemoveAll(StagingDir(dataDir))
	pruneOldPreRestore(dataDir, filepath.Base(pre))
	name := p.Archive
	if name == "" {
		name = "(unbekannt)"
	}
	carryHistory(filepath.Join(dataDir, dbName), filepath.Join(pre, dbName), name, p.CreatedAt)
	return name, nil
}

// carryHistory keeps the backup history of the replaced database (the
// archive only knows runs up to its own creation) and records the restore.
// Failures are not fatal: the history is informational.
func carryHistory(restored, previous, archive string, started time.Time) {
	db, err := sql.Open("sqlite", "file:"+filepath.ToSlash(restored))
	if err != nil {
		return
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	if _, err := os.Stat(previous); err == nil {
		if _, err := db.Exec(`ATTACH DATABASE ? AS prev`, "file:"+filepath.ToSlash(previous)+"?mode=ro"); err == nil {
			_, _ = db.Exec(`DELETE FROM backup_runs`)
			_, _ = db.Exec(`INSERT INTO backup_runs SELECT * FROM prev.backup_runs`)
			_, _ = db.Exec(`DETACH DATABASE prev`)
		}
	}
	// A run that was in progress when the archive was written is finished.
	_, _ = db.Exec(`UPDATE backup_runs SET status = 'success', finished_at = started_at WHERE status = 'running' AND archive = ''`)
	if started.IsZero() {
		started = time.Now()
	}
	_, _ = db.Exec(`INSERT INTO backup_runs(trigger, status, archive, started_at, finished_at) VALUES ('restore', 'success', ?, ?, ?)`,
		archive, started.Unix(), time.Now().Unix())
}

// pruneOldPreRestore keeps only the newest pre-restore folder.
func pruneOldPreRestore(dataDir, keep string) {
	entries, _ := os.ReadDir(dataDir)
	var old []string
	for _, e := range entries {
		if e.IsDir() && strings.HasPrefix(e.Name(), "pre-restore-") && e.Name() != keep {
			old = append(old, e.Name())
		}
	}
	sort.Strings(old)
	for _, name := range old {
		_ = os.RemoveAll(filepath.Join(dataDir, name))
	}
}

// PrepareRestore unpacks an archive into the staging folder and marks it to
// be applied on the next start.
func PrepareRestore(ctx context.Context, r io.Reader, password, dataDir, name string) (*Manifest, error) {
	staging := StagingDir(dataDir)
	_ = os.RemoveAll(staging)
	if err := os.MkdirAll(staging, 0o750); err != nil {
		return nil, err
	}
	man, err := Extract(ctx, r, password, staging)
	if err != nil {
		_ = os.RemoveAll(staging)
		return nil, err
	}
	return man, markPending(dataDir, name)
}
