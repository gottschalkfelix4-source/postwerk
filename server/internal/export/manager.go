package export

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"postwerk/internal/store"
)

// Defaults for Manager.
const (
	DefaultTTL       = 24 * time.Hour
	DefaultRateLimit = time.Hour
)

// ErrBusy is returned while an export of the account is being written.
var ErrBusy = errors.New("für dieses Konto wird bereits ein Export erstellt")

// RateLimitError is returned when a user requests exports too often.
type RateLimitError struct{ RetryAfter time.Duration }

func (e *RateLimitError) Error() string {
	min := int(e.RetryAfter.Round(time.Minute) / time.Minute)
	if min < 1 {
		min = 1
	}
	return fmt.Sprintf("Ein Export ist nur einmal pro Stunde möglich – bitte in %d Minuten erneut versuchen", min)
}

// Manager runs exports in the background and removes expired files.
type Manager struct {
	Store   *store.Store
	DataDir string
	Log     *slog.Logger
	// TTL is how long a finished export can be downloaded (default 24h).
	TTL time.Duration
	// RateLimit is the minimum time between two self-service exports of an
	// account (default 1h). Admin exports are not limited.
	RateLimit time.Duration

	mu      sync.Mutex
	running map[int64]bool
	wg      sync.WaitGroup
}

// Dir is the folder holding the export files.
func (m *Manager) Dir() string { return filepath.Join(m.DataDir, "exports") }

func (m *Manager) ttl() time.Duration {
	if m.TTL > 0 {
		return m.TTL
	}
	return DefaultTTL
}

func (m *Manager) rateLimit() time.Duration {
	if m.RateLimit > 0 {
		return m.RateLimit
	}
	return DefaultRateLimit
}

func (m *Manager) log() *slog.Logger {
	if m.Log == nil {
		return slog.Default()
	}
	return m.Log.With("component", "export")
}

// Start creates an export of accountID requested by requestedBy. Self-service
// requests (requestedBy == accountID and !admin) are rate limited.
func (m *Manager) Start(ctx context.Context, accountID, requestedBy int64, admin bool) (*store.DataExport, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.running == nil {
		m.running = map[int64]bool{}
	}
	if m.running[accountID] {
		return nil, ErrBusy
	}
	if !admin {
		last, err := m.Store.LastDataExport(ctx, accountID, requestedBy)
		if err == nil {
			if wait := m.rateLimit() - time.Since(last.CreatedAt); wait > 0 && last.Status != "failed" {
				return nil, &RateLimitError{RetryAfter: wait}
			}
		} else if !errors.Is(err, store.ErrNotFound) {
			return nil, err
		}
	}
	e, err := m.Store.CreateDataExport(ctx, accountID, requestedBy)
	if err != nil {
		return nil, err
	}
	m.running[accountID] = true
	m.wg.Add(1)
	go func() {
		defer m.wg.Done()
		m.run(e)
		m.mu.Lock()
		delete(m.running, accountID)
		m.mu.Unlock()
	}()
	return e, nil
}

// Wait blocks until all running exports are finished (tests, shutdown).
func (m *Manager) Wait() { m.wg.Wait() }

func (m *Manager) run(e *store.DataExport) {
	ctx := context.Background()
	start := time.Now()
	fail := func(err error) {
		now := time.Now().UTC()
		e.Status, e.Error, e.FinishedAt = "failed", err.Error(), &now
		_ = m.Store.SaveDataExport(ctx, e)
		m.log().Error("data export failed", "export", e.ID, "account", e.AccountID, "err", err)
	}
	if err := os.MkdirAll(m.Dir(), 0o750); err != nil {
		fail(err)
		return
	}
	token := make([]byte, 16)
	if _, err := rand.Read(token); err != nil {
		fail(err)
		return
	}
	e.File = hex.EncodeToString(token) + ".zip"
	e.Status = "running"
	_ = m.Store.SaveDataExport(ctx, e)
	final := filepath.Join(m.Dir(), e.File)
	tmp := final + ".part"
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		fail(err)
		return
	}
	var lastSave time.Time
	err = Build(ctx, m.Store, e.AccountID, f, func(phase string, done, total int64) {
		e.Phase, e.Done, e.Total = phase, done, total
		if time.Since(lastSave) > time.Second {
			lastSave = time.Now()
			_ = m.Store.SaveDataExport(ctx, e)
		}
	})
	if err == nil {
		err = f.Sync()
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Rename(tmp, final)
	}
	if err != nil {
		_ = os.Remove(tmp)
		fail(err)
		return
	}
	info, _ := os.Stat(final)
	now := time.Now().UTC()
	exp := now.Add(m.ttl())
	e.Status, e.Phase, e.FinishedAt, e.ExpiresAt = "done", "Fertig", &now, &exp
	if info != nil {
		e.Size = info.Size()
	}
	if err := m.Store.SaveDataExport(ctx, e); err != nil {
		fail(err)
		return
	}
	m.log().Info("data export ready", "export", e.ID, "account", e.AccountID, "size", e.Size, "duration", time.Since(start).Round(time.Millisecond))
}

// Path returns the file of a downloadable export.
func (m *Manager) Path(e *store.DataExport) (string, error) {
	if e.Status != "done" || e.File == "" || strings.ContainsAny(e.File, `/\`) {
		return "", store.ErrNotFound
	}
	if e.ExpiresAt != nil && time.Now().After(*e.ExpiresAt) {
		return "", store.ErrNotFound
	}
	p := filepath.Join(m.Dir(), e.File)
	if _, err := os.Stat(p); err != nil {
		return "", store.ErrNotFound
	}
	return p, nil
}

// Cleanup expires old exports and removes files no export refers to.
func (m *Manager) Cleanup(ctx context.Context) {
	expired, err := m.Store.ExpiredDataExports(ctx, time.Now())
	if err != nil {
		m.log().Error("listing expired exports failed", "err", err)
	}
	for _, e := range expired {
		_ = os.Remove(filepath.Join(m.Dir(), e.File))
		e.Status, e.File = "expired", ""
		_ = m.Store.SaveDataExport(ctx, e)
	}
	keep, err := m.Store.ExportFiles(ctx)
	if err != nil {
		return
	}
	entries, err := os.ReadDir(m.Dir())
	if err != nil {
		return
	}
	m.mu.Lock()
	busy := len(m.running) > 0
	m.mu.Unlock()
	for _, de := range entries {
		name := de.Name()
		if keep[name] || (busy && strings.HasSuffix(name, ".part")) {
			continue
		}
		_ = os.RemoveAll(filepath.Join(m.Dir(), name))
	}
}

// Run marks interrupted exports as failed and cleans up every 30 minutes.
func (m *Manager) Run(ctx context.Context) {
	if err := m.Store.FailInterruptedExports(ctx); err != nil {
		m.log().Error("marking interrupted exports failed", "err", err)
	}
	t := time.NewTicker(30 * time.Minute)
	defer t.Stop()
	for {
		m.Cleanup(ctx)
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}
