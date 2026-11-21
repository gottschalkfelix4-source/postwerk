package backup

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"postwerk/internal/store"
	"postwerk/internal/vault"
)

var ErrBusy = errors.New("es läuft bereits eine Sicherung oder Wiederherstellung")

// Job is the operation currently running.
type Job struct {
	Kind      string    `json:"kind"` // backup | restore
	Trigger   string    `json:"trigger"`
	Archive   string    `json:"archive"`
	Phase     string    `json:"phase"`
	Done      int64     `json:"done"`
	Total     int64     `json:"total"`
	StartedAt time.Time `json:"startedAt"`
}

// Result is the outcome of the last finished operation.
type Result struct {
	Kind       string    `json:"kind"`
	Archive    string    `json:"archive"`
	OK         bool      `json:"ok"`
	Error      string    `json:"error,omitempty"`
	FinishedAt time.Time `json:"finishedAt"`
}

type Manager struct {
	Store    *store.Store
	Settings *store.SettingsCache
	DataDir  string
	Version  string
	Log      *slog.Logger
	// Restart is called once a restore has been prepared; the restore is
	// applied by ApplyPending on the next start.
	Restart func()
	// OnFailure is called after a backup run failed (optional).
	OnFailure func()

	mu   sync.Mutex
	job  *Job
	last *Result
	wake chan struct{}
}

func (m *Manager) target() (Target, store.BackupSettings, error) {
	cfg := m.Settings.Get().Backup
	t, err := NewTarget(cfg.Target, m.DataDir)
	return t, cfg, err
}

// Status is what the admin page shows.
type Status struct {
	Job     *Job       `json:"job"`
	Last    *Result    `json:"last"`
	Target  string     `json:"target"`
	NextRun *time.Time `json:"nextRun"`
	// MasterKey tells the admin what a restore needs besides the archive.
	MasterKey *MasterKeyInfo `json:"masterKey,omitempty"`
}

// MasterKeyInfo describes where the master key lives and whether archives
// carry it. A key in the data directory (master.key) is always archived,
// because a restore without it would lose every encrypted secret; a key from
// POSTWERK_MASTER_KEY or a file outside the data directory is never archived
// and must be provided again for a restore.
type MasterKeyInfo struct {
	Source      string `json:"source"` // env | env-file | data-file
	InArchive   bool   `json:"inArchive"`
	Fingerprint string `json:"fingerprint"`
}

func (m *Manager) masterKeyInfo() *MasterKeyInfo {
	if m.Store == nil || m.Store.Vault == nil {
		return nil
	}
	k := m.Store.Vault
	return &MasterKeyInfo{Source: k.Source, InArchive: k.InDataDir, Fingerprint: k.Fingerprint()}
}

// archiveExcludes lists data-dir paths that never go into an archive: the
// local backup folder and a stale master.key while the key comes from
// elsewhere.
func (m *Manager) archiveExcludes(t Target) []string {
	return append([]string{LocalDir(t)}, KeyExcludes(m.Store, m.DataDir)...)
}

// KeyExcludes returns the stale data-dir master.key while the active key
// comes from POSTWERK_MASTER_KEY or a file elsewhere: such archives must
// not carry a key at all.
func KeyExcludes(st *store.Store, dataDir string) []string {
	if st != nil && st.Vault != nil && st.Vault.Source != vault.SourceDataFile {
		return []string{filepath.Join(dataDir, vault.KeyFileName)}
	}
	return nil
}

func (m *Manager) Status() Status {
	m.mu.Lock()
	defer m.mu.Unlock()
	st := Status{MasterKey: m.masterKeyInfo()}
	if m.job != nil {
		j := *m.job
		st.Job = &j
	}
	if m.last != nil {
		l := *m.last
		st.Last = &l
	}
	t, cfg, err := m.target()
	if err == nil {
		st.Target = t.Describe()
	}
	if cfg.Schedule.Enabled {
		if n := NextSlot(cfg.Schedule, time.Now()); !n.IsZero() {
			st.NextRun = &n
		}
	}
	return st
}

func (m *Manager) begin(kind, trigger, archive string) (*Job, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.job != nil {
		return nil, ErrBusy
	}
	m.job = &Job{Kind: kind, Trigger: trigger, Archive: archive, Phase: "Vorbereiten", StartedAt: time.Now().UTC()}
	return m.job, nil
}

func (m *Manager) update(fn func(j *Job)) {
	m.mu.Lock()
	if m.job != nil {
		fn(m.job)
	}
	m.mu.Unlock()
}

func (m *Manager) finish(kind, archive string, err error) {
	m.mu.Lock()
	m.job = nil
	r := &Result{Kind: kind, Archive: archive, OK: err == nil, FinishedAt: time.Now().UTC()}
	if err != nil {
		r.Error = err.Error()
	}
	m.last = r
	m.mu.Unlock()
}

// Start begins a backup in the background.
func (m *Manager) Start(trigger string) error {
	if _, err := m.begin("backup", trigger, ""); err != nil {
		return err
	}
	go m.runBackup(trigger)
	return nil
}

type countingReader struct {
	r io.Reader
	n int64
}

func (c *countingReader) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	c.n += int64(n)
	return n, err
}

func (m *Manager) runBackup(trigger string) {
	ctx, cancel := context.WithTimeout(context.Background(), 24*time.Hour)
	defer cancel()
	started := time.Now()
	t, cfg, err := m.target()
	desc := ""
	if t != nil {
		desc = t.Describe()
	}
	id, dbErr := m.Store.StartBackupRun(ctx, trigger, desc)
	name := NewName(started, cfg.Encrypt)
	m.update(func(j *Job) { j.Archive = name })
	var size int64
	var files int
	if err == nil {
		size, files, err = m.writeArchive(ctx, t, cfg, name)
	}
	if dbErr == nil {
		_ = m.Store.FinishBackupRun(ctx, id, name, size, files, err)
	}
	if err != nil {
		m.Log.Error("backup failed", "component", "backup", "target", desc, "err", err)
		m.finish("backup", name, err)
		if m.OnFailure != nil {
			m.OnFailure()
		}
		return
	}
	m.Log.Info("backup created", "component", "backup", "archive", name, "target", desc, "size", size, "files", files,
		"seconds", int(time.Since(started).Seconds()), "trigger", trigger)
	if cfg.Keep > 0 {
		m.update(func(j *Job) { j.Phase = "Alte Sicherungen aufräumen" })
		m.prune(ctx, t, cfg.Keep)
	}
	m.finish("backup", name, nil)
}

func (m *Manager) writeArchive(ctx context.Context, t Target, cfg store.BackupSettings, name string) (int64, int, error) {
	password := ""
	if cfg.Encrypt {
		if cfg.Password == "" {
			return 0, 0, errors.New("Verschlüsselung ist aktiv, aber kein Passwort gesetzt")
		}
		password = cfg.Password
	}
	set := m.Settings.Get()
	man := Manifest{Version: m.Version, Hostname: set.Server.Hostname, CreatedAt: time.Now().UTC()}
	man.Domains, man.Accounts, man.Messages, _ = m.Store.BackupCounts(ctx)
	src := Source{
		DataDir: m.DataDir, DB: m.Store.DB, Manifest: man, Exclude: m.archiveExcludes(t),
		Progress: func(done, total int64) {
			m.update(func(j *Job) { j.Done, j.Total = done, total })
		},
	}
	m.update(func(j *Job) { j.Phase = "Archiv schreiben" })
	if t.Streaming() {
		pr, pw := io.Pipe()
		res := make(chan struct {
			n   int
			err error
		}, 1)
		go func() {
			n, err := Create(ctx, pw, src, password)
			pw.CloseWithError(err)
			res <- struct {
				n   int
				err error
			}{n, err}
		}()
		cr := &countingReader{r: pr}
		putErr := t.Put(ctx, name, cr, -1)
		if putErr != nil {
			pr.CloseWithError(putErr)
		}
		r := <-res
		if r.err != nil {
			return 0, 0, r.err
		}
		return cr.n, r.n, putErr
	}
	// Remote targets that need the size up front: archive to a temp file.
	f, err := os.CreateTemp(m.DataDir, ".backup-upload-*.tmp")
	if err != nil {
		return 0, 0, err
	}
	defer os.Remove(f.Name())
	defer f.Close()
	n, err := Create(ctx, f, src, password)
	if err != nil {
		return 0, 0, err
	}
	size, err := f.Seek(0, io.SeekCurrent)
	if err != nil {
		return 0, 0, err
	}
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return 0, 0, err
	}
	m.update(func(j *Job) { j.Phase = "Hochladen"; j.Done, j.Total = 0, size })
	pr := &progressReader{r: f, fn: func(done int64) { m.update(func(j *Job) { j.Done = done }) }}
	if err := t.Put(ctx, name, pr, size); err != nil {
		return 0, 0, err
	}
	return size, n, nil
}

type progressReader struct {
	r    io.Reader
	n    int64
	fn   func(int64)
	last time.Time
}

func (p *progressReader) Read(b []byte) (int, error) {
	n, err := p.r.Read(b)
	p.n += int64(n)
	if time.Since(p.last) > 300*time.Millisecond {
		p.last = time.Now()
		p.fn(p.n)
	}
	return n, err
}

// prune deletes the oldest archives beyond keep.
func (m *Manager) prune(ctx context.Context, t Target, keep int) {
	list, err := t.List(ctx)
	if err != nil {
		m.Log.Warn("backup retention: list failed", "component", "backup", "err", err)
		return
	}
	for i := keep; i < len(list); i++ {
		if err := t.Delete(ctx, list[i].Name); err != nil {
			m.Log.Warn("backup retention: delete failed", "component", "backup", "archive", list[i].Name, "err", err)
			continue
		}
		m.Log.Info("old backup removed", "component", "backup", "archive", list[i].Name)
	}
}

func (m *Manager) List(ctx context.Context) ([]Entry, error) {
	t, _, err := m.target()
	if err != nil {
		return nil, err
	}
	return t.List(ctx)
}

func (m *Manager) Open(ctx context.Context, name string) (io.ReadCloser, int64, error) {
	if !ValidName(name) {
		return nil, 0, ErrNotFound
	}
	t, _, err := m.target()
	if err != nil {
		return nil, 0, err
	}
	return t.Open(ctx, name)
}

func (m *Manager) Delete(ctx context.Context, name string) error {
	if !ValidName(name) {
		return ErrNotFound
	}
	t, _, err := m.target()
	if err != nil {
		return err
	}
	if err := t.Delete(ctx, name); err != nil {
		return err
	}
	m.Log.Info("backup deleted", "component", "backup", "archive", name)
	return nil
}

// Upload stores an archive file (for example from another server) on the
// target. The name is kept when it is a valid archive name.
func (m *Manager) Upload(ctx context.Context, filename string, f *os.File) (string, error) {
	format, _, err := Sniff(f)
	if err != nil {
		return "", err
	}
	if format == FormatUnknown {
		return "", errors.New("keine Postwerk-Sicherung (erwartet .tar.gz oder .tar.gz.enc)")
	}
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return "", err
	}
	info, err := f.Stat()
	if err != nil {
		return "", err
	}
	name := filepath.Base(filename)
	if !ValidName(name) || (format == FormatEncrypted) != strings.HasSuffix(name, ".enc") {
		name = "postwerk-" + time.Now().Format("20060102-150405") + "-upload.tar.gz"
		if format == FormatEncrypted {
			name += ".enc"
		}
	}
	t, _, err := m.target()
	if err != nil {
		return "", err
	}
	if err := t.Put(ctx, name, f, info.Size()); err != nil {
		return "", err
	}
	m.Log.Info("backup uploaded", "component", "backup", "archive", name, "size", info.Size())
	return name, nil
}

// StartRestore checks the archive and password, then unpacks it in the
// background. When everything is in place Restart is called.
func (m *Manager) StartRestore(ctx context.Context, name, password string) error {
	if !ValidName(name) {
		return ErrNotFound
	}
	t, _, err := m.target()
	if err != nil {
		return err
	}
	// Quick check so a wrong password fails right away.
	rc, _, err := t.Open(ctx, name)
	if err != nil {
		return err
	}
	plain, _, err := Open(rc, password)
	if err == nil {
		_, err = io.ReadFull(plain, make([]byte, 512))
		if errors.Is(err, io.ErrUnexpectedEOF) {
			err = nil
		}
	}
	rc.Close()
	if err != nil {
		return err
	}
	if _, err := m.begin("restore", "manual", name); err != nil {
		return err
	}
	go m.runRestore(t, name, password)
	return nil
}

func (m *Manager) runRestore(t Target, name, password string) {
	ctx, cancel := context.WithTimeout(context.Background(), 24*time.Hour)
	defer cancel()
	err := m.stage(ctx, t, name, password)
	if err != nil {
		m.Log.Error("restore failed", "component", "backup", "archive", name, "err", err)
		_ = os.RemoveAll(StagingDir(m.DataDir))
		m.finish("restore", name, err)
		return
	}
	m.Log.Warn("restore prepared, restarting", "component", "backup", "archive", name)
	m.update(func(j *Job) { j.Phase = "Neustart" })
	if m.Restart != nil {
		time.Sleep(time.Second) // let the UI see the final state
		m.Restart()
	}
}

func (m *Manager) stage(ctx context.Context, t Target, name, password string) error {
	m.update(func(j *Job) { j.Phase = "Archiv laden und entpacken" })
	rc, size, err := t.Open(ctx, name)
	if err != nil {
		return err
	}
	defer rc.Close()
	m.update(func(j *Job) { j.Total = size })
	pr := &progressReader{r: rc, fn: func(done int64) { m.update(func(j *Job) { j.Done = done }) }}
	man, err := PrepareRestore(ctx, pr, password, m.DataDir, name)
	if err != nil {
		return err
	}
	m.Log.Info("restore staged", "component", "backup", "archive", name, "created", man.CreatedAt, "accounts", man.Accounts, "messages", man.Messages)
	return nil
}

// TestTarget writes, lists and deletes a small file on the given target.
func (m *Manager) TestTarget(ctx context.Context, cfg store.BackupTarget) (string, int, error) {
	t, err := NewTarget(cfg, m.DataDir)
	if err != nil {
		return "", 0, err
	}
	probe := "postwerk-schreibtest-" + randHex(4) + ".txt"
	body := "Postwerk Schreibtest – diese Datei kann gelöscht werden.\n"
	if err := t.Put(ctx, probe, strings.NewReader(body), int64(len(body))); err != nil {
		return t.Describe(), 0, fmt.Errorf("schreiben: %w", err)
	}
	list, listErr := t.List(ctx)
	if err := t.Delete(ctx, probe); err != nil {
		return t.Describe(), 0, fmt.Errorf("löschen: %w", err)
	}
	if listErr != nil {
		return t.Describe(), 0, fmt.Errorf("auflisten: %w", listErr)
	}
	return t.Describe(), len(list), nil
}

// Notify re-evaluates the schedule (after settings changes).
func (m *Manager) Notify() {
	select {
	case m.wake <- struct{}{}:
	default:
	}
}

// Run drives scheduled backups until ctx ends.
func (m *Manager) Run(ctx context.Context) {
	m.mu.Lock()
	if m.wake == nil {
		m.wake = make(chan struct{}, 1)
	}
	m.mu.Unlock()
	_ = m.Store.FailStaleBackupRuns(ctx)
	started := time.Now()
	var changedAt time.Time
	prev := m.Settings.Get().Backup.Schedule
	tick := time.NewTicker(time.Minute)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		case <-m.wake:
		}
		s := m.Settings.Get().Backup.Schedule
		if s != prev {
			// A changed schedule counts from now, so enabling it does not
			// immediately run the slot that just passed.
			prev, changedAt = s, time.Now()
		}
		if !s.Enabled {
			continue
		}
		slot := LastSlot(s, time.Now())
		if slot.IsZero() {
			continue
		}
		last, err := m.Store.LastScheduledBackup(ctx)
		if err != nil {
			continue
		}
		// A slot missed while the server was down is caught up; without any
		// previous run, counting starts at process start.
		since := last
		if since.IsZero() {
			since = started
		}
		if changedAt.After(since) {
			since = changedAt
		}
		if slot.After(since) {
			if err := m.Start("schedule"); err != nil && !errors.Is(err, ErrBusy) {
				m.Log.Warn("scheduled backup not started", "component", "backup", "err", err)
			}
		}
	}
}

func parseClock(s string) (int, int, bool) {
	t, err := time.Parse("15:04", s)
	if err != nil {
		return 0, 0, false
	}
	return t.Hour(), t.Minute(), true
}

// LastSlot returns the most recent scheduled time at or before now.
func LastSlot(s store.BackupSchedule, now time.Time) time.Time {
	switch s.Frequency {
	case "hourly":
		return time.Date(now.Year(), now.Month(), now.Day(), now.Hour(), 0, 0, 0, now.Location())
	case "daily", "weekly":
		h, mi, ok := parseClock(s.Time)
		if !ok {
			return time.Time{}
		}
		t := time.Date(now.Year(), now.Month(), now.Day(), h, mi, 0, 0, now.Location())
		if t.After(now) {
			t = t.AddDate(0, 0, -1)
		}
		if s.Frequency == "weekly" {
			for int(t.Weekday()) != s.Weekday%7 {
				t = t.AddDate(0, 0, -1)
			}
		}
		return t
	}
	return time.Time{}
}

// NextSlot returns the next scheduled time after now.
func NextSlot(s store.BackupSchedule, now time.Time) time.Time {
	switch s.Frequency {
	case "hourly":
		return LastSlot(s, now).Add(time.Hour)
	case "daily":
		last := LastSlot(s, now)
		if last.IsZero() {
			return last
		}
		return last.AddDate(0, 0, 1)
	case "weekly":
		last := LastSlot(s, now)
		if last.IsZero() {
			return last
		}
		return last.AddDate(0, 0, 7)
	}
	return time.Time{}
}
