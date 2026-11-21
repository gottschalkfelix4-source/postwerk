package atrest

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"time"

	"postwerk/internal/message"
	"postwerk/internal/store"
)

// Job is the progress of a background conversion.
type Job struct {
	Running    bool       `json:"running"`
	Encrypt    bool       `json:"encrypt"`
	Done       int        `json:"done"`
	Total      int        `json:"total"`
	Converted  int        `json:"converted"`
	Skipped    int        `json:"skipped"`
	Failed     int        `json:"failed"`
	Error      string     `json:"error,omitempty"`
	StartedAt  time.Time  `json:"startedAt"`
	FinishedAt *time.Time `json:"finishedAt"`
}

// ErrBusy is returned when a job of the same kind is already running.
var ErrBusy = errors.New("es läuft bereits eine Umwandlung")

// Manager runs the background jobs: encrypting the existing messages of an
// account and converting all blobs for server-side blob encryption.
type Manager struct {
	Store *store.Store
	Log   *slog.Logger

	mu       sync.Mutex
	accounts map[int64]*Job
	blobs    *Job
}

func (m *Manager) log() *slog.Logger {
	if m.Log == nil {
		return slog.Default()
	}
	return m.Log
}

// AccountJob returns a copy of the account's last job (nil if none).
func (m *Manager) AccountJob(accountID int64) *Job {
	m.mu.Lock()
	defer m.mu.Unlock()
	if j := m.accounts[accountID]; j != nil {
		cp := *j
		return &cp
	}
	return nil
}

// BlobJob returns a copy of the last blob conversion (nil if none).
func (m *Manager) BlobJob() *Job {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.blobs != nil {
		cp := *m.blobs
		return &cp
	}
	return nil
}

func (m *Manager) update(j *Job, fn func(*Job)) {
	m.mu.Lock()
	fn(j)
	m.mu.Unlock()
}

// StartEncryptExisting encrypts all stored messages of the account (except
// drafts and messages that are already encrypted) in the background.
func (m *Manager) StartEncryptExisting(accountID int64) (*Job, error) {
	m.mu.Lock()
	if m.accounts == nil {
		m.accounts = map[int64]*Job{}
	}
	if j := m.accounts[accountID]; j != nil && j.Running {
		m.mu.Unlock()
		return nil, ErrBusy
	}
	j := &Job{Running: true, Encrypt: true, StartedAt: time.Now().UTC()}
	m.accounts[accountID] = j
	cp := *j
	m.mu.Unlock()
	go func() {
		err := m.EncryptExisting(context.Background(), accountID, func(fn func(*Job)) { m.update(j, fn) })
		m.update(j, func(j *Job) {
			j.Running = false
			now := time.Now().UTC()
			j.FinishedAt = &now
			if err != nil {
				j.Error = err.Error()
			}
		})
		if err != nil {
			m.log().Error("encrypting stored messages failed", "component", "atrest", "account", accountID, "err", err)
		} else {
			m.log().Info("stored messages encrypted", "component", "atrest", "account", accountID, "converted", m.AccountJob(accountID).Converted)
		}
	}()
	return &cp, nil
}

// EncryptExisting does the work of StartEncryptExisting synchronously.
// progress (may be nil) receives updates of the job counters.
func (m *Manager) EncryptExisting(ctx context.Context, accountID int64, progress func(func(*Job))) error {
	if progress == nil {
		progress = func(func(*Job)) {}
	}
	enc, err := m.Store.GetMailEncryption(ctx, accountID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return errors.New("Verschlüsselung ist für dieses Konto nicht eingerichtet")
		}
		return err
	}
	opt := Options{Kind: enc.Kind, PublicKey: []byte(enc.PublicKey), HideSubject: enc.HideSubject}
	refs, err := m.Store.EncryptionCandidates(ctx, accountID)
	if err != nil {
		return err
	}
	progress(func(j *Job) { j.Total = len(refs) })
	for _, ref := range refs {
		if err := ctx.Err(); err != nil {
			return err
		}
		res := m.encryptOne(ctx, accountID, ref, opt)
		progress(func(j *Job) {
			j.Done++
			switch res {
			case resConverted:
				j.Converted++
			case resSkipped:
				j.Skipped++
			default:
				j.Failed++
			}
		})
	}
	return nil
}

type result int

const (
	resConverted result = iota
	resSkipped
	resFailed
)

func (m *Manager) encryptOne(ctx context.Context, accountID int64, ref store.MessageRef, opt Options) result {
	raw, err := m.Store.Blobs.Get(ref.BlobHash)
	if err != nil {
		m.log().Warn("encrypt: blob unreadable", "component", "atrest", "message", ref.ID, "err", err)
		return resFailed
	}
	if IsEncrypted(raw) {
		return resSkipped
	}
	out, err := Encrypt(raw, opt)
	if err != nil {
		m.log().Warn("encrypt: failed", "component", "atrest", "message", ref.ID, "err", err)
		return resFailed
	}
	hash, err := m.Store.Blobs.Put(out)
	if err != nil {
		return resFailed
	}
	p, err := message.Parse(out, false)
	if err != nil {
		p = &message.Parsed{}
	}
	search := p.Subject + " " + p.From.Name + " " + p.From.Email
	for _, a := range p.To {
		search += " " + a.Name + " " + a.Email
	}
	if err := m.Store.ReplaceMessageContent(ctx, accountID, ref.ID, store.EncryptedContent{
		BlobHash: hash, Size: int64(len(out)), Subject: p.Subject, SearchText: search,
	}); err != nil {
		if !errors.Is(err, store.ErrNotFound) { // deleted meanwhile
			m.log().Warn("encrypt: update failed", "component", "atrest", "message", ref.ID, "err", err)
		}
		return resFailed
	}
	return resConverted
}

// StartBlobMigration converts every blob on disk into the requested form
// (encrypted with the master key, or plaintext) in the background.
func (m *Manager) StartBlobMigration(encrypt bool) (*Job, error) {
	m.mu.Lock()
	if m.blobs != nil && m.blobs.Running {
		m.mu.Unlock()
		return nil, ErrBusy
	}
	j := &Job{Running: true, Encrypt: encrypt, StartedAt: time.Now().UTC()}
	m.blobs = j
	cp := *j
	m.mu.Unlock()
	go func() {
		err := m.Store.Blobs.MigrateBlobs(context.Background(), encrypt, func(done, total, failed int) {
			m.update(j, func(j *Job) { j.Done, j.Total, j.Failed, j.Converted = done, total, failed, done-failed })
		})
		m.update(j, func(j *Job) {
			j.Running = false
			now := time.Now().UTC()
			j.FinishedAt = &now
			if err != nil {
				j.Error = err.Error()
			}
		})
		if err != nil {
			m.log().Error("blob conversion failed", "component", "atrest", "encrypt", encrypt, "err", err)
		} else {
			m.log().Info("blob conversion finished", "component", "atrest", "encrypt", encrypt, "blobs", m.BlobJob().Total)
		}
	}()
	return &cp, nil
}
