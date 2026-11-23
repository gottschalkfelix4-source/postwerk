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

	"postwerk/internal/store"
	"postwerk/internal/vault"
)

// A restore is prepared while the server runs (archive unpacked into
// restore-staging and checked: format, database, schema version, master
// key) and applied on the next start, before the database is opened. The
// previous data is kept in pre-restore-<time>. When the restored data cannot
// be opened on that start, Revert puts the previous data back.
const (
	pendingFile = "restore-pending"
	failedFile  = "restore-failed.json"
	// stagedManifest sits next to staging/data and remembers the archive
	// while a restore waits for the master key.
	stagedManifest = "manifest.json"
)

var (
	// ErrKeyMissing: the archive carries no master.key, the server has no
	// POSTWERK_MASTER_KEY and the archived secrets are encrypted.
	ErrKeyMissing = errors.New("master-schlüssel fehlt")
	// ErrKeyMismatch: the key the restored data would be opened with does
	// not decrypt the archived secrets.
	ErrKeyMismatch = errors.New("master-schlüssel passt nicht")
	// ErrNothingStaged: ResumeRestore without a prepared restore.
	ErrNothingStaged = errors.New("es wartet keine vorbereitete Wiederherstellung – bitte das Archiv erneut einspielen")
)

// IsKeyError reports errors that a retry with the right master key fixes;
// the unpacked archive is kept for ResumeRestore.
func IsKeyError(err error) bool {
	return errors.Is(err, ErrKeyMissing) || errors.Is(err, ErrKeyMismatch)
}

// RestoreOptions control PrepareRestore.
type RestoreOptions struct {
	// Name is the archive name shown in logs and the backup history.
	Name string
	// Password decrypts an encrypted archive.
	Password string
	// MasterKey (base64) is the key of the old server. It is only needed
	// when the archive carries no master.key (the old server used
	// POSTWERK_MASTER_KEY); it is then stored as master.key in the restored
	// data directory.
	MasterKey string
}

type pending struct {
	Archive   string    `json:"archive"`
	CreatedAt time.Time `json:"createdAt"`
}

func StagingDir(dataDir string) string { return filepath.Join(dataDir, "restore-staging") }

func markPending(dataDir, archive string) error {
	b, _ := json.Marshal(pending{Archive: archive, CreatedAt: time.Now().UTC()})
	_ = os.Remove(filepath.Join(dataDir, failedFile))
	return os.WriteFile(filepath.Join(dataDir, pendingFile), b, 0o640)
}

// Failure describes a restore that was rolled back on start.
type Failure struct {
	Archive string    `json:"archive"`
	Error   string    `json:"error"`
	At      time.Time `json:"at"`
}

func writeFailure(dataDir, archive string, cause error) {
	b, _ := json.Marshal(Failure{Archive: archive, Error: cause.Error(), At: time.Now().UTC()})
	_ = os.WriteFile(filepath.Join(dataDir, failedFile), b, 0o640)
}

// LastFailure returns the restore that failed on the last start, if any.
func LastFailure(dataDir string) *Failure {
	b, err := os.ReadFile(filepath.Join(dataDir, failedFile))
	if err != nil {
		return nil
	}
	var f Failure
	if json.Unmarshal(b, &f) != nil || f.Error == "" {
		return nil
	}
	return &f
}

// keepOnRestore lists top-level entries that stay in place when a restore
// swaps the data directory.
func keepOnRestore(name string) bool {
	return name == "backups" || name == "restore-staging" || name == pendingFile || name == failedFile ||
		strings.HasPrefix(name, "pre-restore-") || strings.HasPrefix(name, "failed-restore-")
}

// Applied describes a restore swapped in by ApplyPending.
type Applied struct {
	Archive string
	// PreDir holds the data that was replaced.
	PreDir string
}

// ApplyPending swaps in a prepared restore. It returns nil when no restore
// was pending. On failure the previous data is moved back. A staging folder
// without a pending marker (a restore that waited for the master key) is
// removed.
func ApplyPending(dataDir string) (*Applied, error) {
	raw, err := os.ReadFile(filepath.Join(dataDir, pendingFile))
	if errors.Is(err, fs.ErrNotExist) {
		_ = os.RemoveAll(StagingDir(dataDir))
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	// Never loop on a broken restore: the marker goes first.
	_ = os.Remove(filepath.Join(dataDir, pendingFile))
	var p pending
	_ = json.Unmarshal(raw, &p)
	name := p.Archive
	if name == "" {
		name = "(unbekannt)"
	}
	a, err := swapIn(dataDir, name)
	if err != nil {
		writeFailure(dataDir, name, err)
		_ = os.RemoveAll(StagingDir(dataDir))
		return nil, err
	}
	carryHistory(filepath.Join(dataDir, dbName), filepath.Join(a.PreDir, dbName), name, p.CreatedAt)
	return a, nil
}

func swapIn(dataDir, name string) (*Applied, error) {
	src := filepath.Join(StagingDir(dataDir), "data")
	if _, err := os.Stat(filepath.Join(src, dbName)); err != nil {
		return nil, fmt.Errorf("vorbereitete Wiederherstellung unvollständig: %w", err)
	}
	pre := filepath.Join(dataDir, "pre-restore-"+time.Now().Format("20060102-150405"))
	if err := os.MkdirAll(pre, 0o750); err != nil {
		return nil, err
	}
	current, err := os.ReadDir(dataDir)
	if err != nil {
		return nil, err
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
			return nil, fmt.Errorf("alte Daten sichern: %w", err)
		}
		moved = append(moved, e.Name())
	}
	restored, err := os.ReadDir(src)
	if err != nil {
		rollback()
		return nil, err
	}
	for _, e := range restored {
		if keepOnRestore(e.Name()) {
			continue
		}
		if err := os.Rename(filepath.Join(src, e.Name()), filepath.Join(dataDir, e.Name())); err != nil {
			rollback()
			return nil, fmt.Errorf("daten einspielen: %w", err)
		}
	}
	_ = os.RemoveAll(StagingDir(dataDir))
	pruneOld(dataDir, "pre-restore-", filepath.Base(pre))
	_ = os.Remove(filepath.Join(dataDir, failedFile))
	return &Applied{Archive: name, PreDir: pre}, nil
}

// Revert undoes an applied restore whose data could not be opened: the
// restored files move to failed-restore-<time> (kept for inspection) and
// the previous data comes back. The cause is recorded for LastFailure.
func Revert(dataDir string, a *Applied, cause error) error {
	if a == nil {
		return errors.New("keine Wiederherstellung zum Zurücknehmen")
	}
	failed := filepath.Join(dataDir, "failed-restore-"+time.Now().Format("20060102-150405"))
	if err := os.MkdirAll(failed, 0o750); err != nil {
		return err
	}
	entries, err := os.ReadDir(dataDir)
	if err != nil {
		return err
	}
	for _, e := range entries {
		if keepOnRestore(e.Name()) {
			continue
		}
		if err := os.Rename(filepath.Join(dataDir, e.Name()), filepath.Join(failed, e.Name())); err != nil {
			return fmt.Errorf("wiederhergestellte Daten beiseitelegen: %w", err)
		}
	}
	prev, err := os.ReadDir(a.PreDir)
	if err != nil {
		return err
	}
	for _, e := range prev {
		if err := os.Rename(filepath.Join(a.PreDir, e.Name()), filepath.Join(dataDir, e.Name())); err != nil {
			return fmt.Errorf("vorherige Daten zurückholen: %w", err)
		}
	}
	_ = os.Remove(a.PreDir)
	pruneOld(dataDir, "failed-restore-", filepath.Base(failed))
	writeFailure(dataDir, a.Archive, cause)
	return nil
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
			// A freshly set up server has no history worth keeping.
			var n int
			if db.QueryRow(`SELECT COUNT(*) FROM prev.backup_runs`).Scan(&n) == nil && n > 0 {
				_, _ = db.Exec(`DELETE FROM backup_runs`)
				_, _ = db.Exec(`INSERT INTO backup_runs SELECT * FROM prev.backup_runs`)
			}
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

// pruneOld keeps only the newest folder with the given prefix.
func pruneOld(dataDir, prefix, keep string) {
	entries, _ := os.ReadDir(dataDir)
	var old []string
	for _, e := range entries {
		if e.IsDir() && strings.HasPrefix(e.Name(), prefix) && e.Name() != keep {
			old = append(old, e.Name())
		}
	}
	sort.Strings(old)
	for _, name := range old {
		_ = os.RemoveAll(filepath.Join(dataDir, name))
	}
}

type stagedInfo struct {
	Name     string   `json:"name"`
	Manifest Manifest `json:"manifest"`
}

// PrepareRestore unpacks an archive into the staging folder, checks that the
// server can run with it and marks it to be applied on the next start.
// On a key error (IsKeyError) the unpacked archive stays staged and the
// manifest is returned, so ResumeRestore can continue with the master key.
// Any other error removes the staging folder.
func PrepareRestore(ctx context.Context, r io.Reader, dataDir string, opts RestoreOptions) (*Manifest, error) {
	staging := StagingDir(dataDir)
	_ = os.RemoveAll(staging)
	if err := os.MkdirAll(staging, 0o750); err != nil {
		return nil, err
	}
	man, err := Extract(ctx, r, opts.Password, staging)
	if err == nil {
		b, _ := json.Marshal(stagedInfo{Name: opts.Name, Manifest: *man})
		err = os.WriteFile(filepath.Join(staging, stagedManifest), b, 0o640)
	}
	if err == nil {
		err = verifyStaged(ctx, dataDir, man, opts.MasterKey)
	}
	if err != nil {
		if IsKeyError(err) {
			return man, err
		}
		_ = os.RemoveAll(staging)
		return nil, err
	}
	return man, markPending(dataDir, opts.Name)
}

// ResumeRestore continues a staged restore that stopped at a key error.
func ResumeRestore(ctx context.Context, dataDir, masterKey string) (string, *Manifest, error) {
	b, err := os.ReadFile(filepath.Join(StagingDir(dataDir), stagedManifest))
	if err != nil {
		return "", nil, ErrNothingStaged
	}
	var info stagedInfo
	if err := json.Unmarshal(b, &info); err != nil {
		return "", nil, ErrNothingStaged
	}
	if err := verifyStaged(ctx, dataDir, &info.Manifest, masterKey); err != nil {
		if !IsKeyError(err) {
			_ = os.RemoveAll(StagingDir(dataDir))
		}
		return info.Name, &info.Manifest, err
	}
	return info.Name, &info.Manifest, markPending(dataDir, info.Name)
}

// verifyStaged checks an unpacked archive before it may replace the live
// data: the schema must not be newer than this build, the master key the
// server will use must decrypt the archived secrets, and the database must
// open (pending migrations run on the staged copy).
func verifyStaged(ctx context.Context, dataDir string, man *Manifest, masterKey string) error {
	data := filepath.Join(StagingDir(dataDir), "data")
	db, err := sql.Open("sqlite", "file:"+filepath.ToSlash(filepath.Join(data, dbName))+"?mode=ro")
	if err != nil {
		return err
	}
	var schema int
	err = db.QueryRowContext(ctx, `SELECT COALESCE(MAX(version), 0) FROM schema_version`).Scan(&schema)
	if err != nil {
		db.Close()
		return fmt.Errorf("datenbank im archiv nicht lesbar: %w", err)
	}
	if schema > store.SchemaVersion() {
		db.Close()
		return errKind(ErrFormat, "Die Datenbank der Sicherung (Schema %d) stammt von einer neueren Postwerk-Version (%s); diese Version kennt Schema %d – bitte zuerst Postwerk aktualisieren",
			schema, man.Version, store.SchemaVersion())
	}

	if masterKey = strings.TrimSpace(masterKey); masterKey != "" {
		given, err := vault.Parse(masterKey)
		if err != nil {
			db.Close()
			return errKind(ErrKeyMismatch, "Der eingegebene Master-Schlüssel ist ungültig (%v)", err)
		}
		if vault.EnvConfigured() {
			active, err := vault.Resolve(data)
			if err != nil {
				db.Close()
				return err
			}
			if !active.Equal(given) {
				db.Close()
				return errKind(ErrKeyMismatch, "Dieser Server nutzt den Schlüssel aus POSTWERK_MASTER_KEY bzw. POSTWERK_MASTER_KEY_FILE (%s); "+
					"den Schlüssel des alten Servers dort eintragen und den Container neu starten", active.Fingerprint())
			}
		} else if err := vault.WriteKeyFile(filepath.Join(data, vault.KeyFileName), given); err != nil {
			db.Close()
			return err
		}
	}

	key, keyErr := vault.Resolve(data)
	if keyErr != nil && !errors.Is(keyErr, fs.ErrNotExist) {
		db.Close()
		return fmt.Errorf("master-schlüssel: %w", keyErr)
	}
	present, ok, err := store.VerifyMasterKey(ctx, db, key)
	db.Close()
	if err != nil {
		return fmt.Errorf("datenbank im archiv nicht lesbar: %w", err)
	}
	want := ""
	if man.KeyFingerprint != "" {
		want = " (Fingerabdruck " + man.KeyFingerprint + ")"
	}
	switch {
	case present && key == nil:
		return errKind(ErrKeyMissing, "Die Sicherung enthält keinen Master-Schlüssel, weil der alte Server ihn aus POSTWERK_MASTER_KEY bzw. einer Datei außerhalb des Datenverzeichnisses las. "+
			"Ohne diesen Schlüssel%s sind Passwörter, DKIM-Schlüssel und verschlüsselte Mails nicht lesbar – bitte den Schlüssel angeben", want)
	case present && !ok:
		return errKind(ErrKeyMismatch, "Der Schlüssel, mit dem dieser Server starten würde (%s), passt nicht zur Sicherung%s – "+
			"POSTWERK_MASTER_KEY bzw. POSTWERK_MASTER_KEY_FILE auf den Schlüssel des alten Servers setzen oder entfernen, wenn die Sicherung ihn enthält", key.Fingerprint(), want)
	}

	// Open the staged data like the server will on the next start.
	st, err := store.Open(data)
	if err != nil {
		return fmt.Errorf("die Datenbank der Sicherung lässt sich nicht öffnen: %w", err)
	}
	return st.Close()
}
