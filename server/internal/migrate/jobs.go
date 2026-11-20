package migrate

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/emersion/go-imap/client"

	"postwerk/internal/auth"
	"postwerk/internal/delivery"
	"postwerk/internal/logx"
	"postwerk/internal/mailauth"
	"postwerk/internal/store"
)

type Options struct {
	IncludeTrash   bool     `json:"includeTrash"`
	IncludeJunk    bool     `json:"includeJunk"`
	ExcludeFolders []string `json:"excludeFolders"`
	Since          *string  `json:"since"`
	AnalyzeWithAI  bool     `json:"analyzeWithAI"`
}

type CreateAccount struct {
	Name     string `json:"name"`
	Password string `json:"password"`
}

type AccountInput struct {
	TargetEmail   string         `json:"targetEmail"`
	Username      string         `json:"username"`
	Password      string         `json:"password"`
	CreateAccount *CreateAccount `json:"createAccount"`
}

type AccountProgress struct {
	TargetEmail   string  `json:"targetEmail"`
	Username      string  `json:"username"`
	Status        string  `json:"status"`
	FoldersTotal  int     `json:"foldersTotal"`
	FoldersDone   int     `json:"foldersDone"`
	CurrentFolder *string `json:"currentFolder"`
	MessagesTotal int     `json:"messagesTotal"`
	MessagesDone  int     `json:"messagesDone"`
	Imported      int     `json:"imported"`
	Skipped       int     `json:"skipped"`
	Failed        int     `json:"failed"`
	Error         *string `json:"error"`
}

type Job struct {
	ID         int64             `json:"id"`
	Name       string            `json:"name"`
	Source     Source            `json:"source"`
	Status     string            `json:"status"`
	Options    Options           `json:"options"`
	CreatedAt  time.Time         `json:"createdAt"`
	StartedAt  *time.Time        `json:"startedAt"`
	FinishedAt *time.Time        `json:"finishedAt"`
	Accounts   []AccountProgress `json:"accounts"`
	Log        []logx.Entry      `json:"log"`
}

type running struct {
	mu     sync.Mutex
	job    *Job
	cancel context.CancelFunc
	dirty  bool
}

type Manager struct {
	Store    *store.Store
	Delivery *delivery.Service
	Log      *slog.Logger

	mu   sync.Mutex
	jobs map[int64]*running
}

func NewManager(st *store.Store, svc *delivery.Service, log *slog.Logger) *Manager {
	_ = st.MarkMigrationsInterrupted(context.Background())
	return &Manager{Store: st, Delivery: svc, Log: log.With("component", "migration"), jobs: map[int64]*running{}}
}

var ErrRunning = errors.New("job läuft noch")

func fromRow(r *store.MigrationRow) *Job {
	j := &Job{ID: r.ID, Name: r.Name, Status: r.Status, CreatedAt: r.CreatedAt, StartedAt: r.StartedAt, FinishedAt: r.FinishedAt}
	_ = json.Unmarshal([]byte(r.SourceJSON), &j.Source)
	_ = json.Unmarshal([]byte(r.OptionsJSON), &j.Options)
	_ = json.Unmarshal([]byte(r.AccountsJSON), &j.Accounts)
	_ = json.Unmarshal([]byte(r.LogJSON), &j.Log)
	if j.Accounts == nil {
		j.Accounts = []AccountProgress{}
	}
	if j.Log == nil {
		j.Log = []logx.Entry{}
	}
	if j.Options.ExcludeFolders == nil {
		j.Options.ExcludeFolders = []string{}
	}
	return j
}

func copyJob(j *Job) *Job {
	b, _ := json.Marshal(j)
	var out Job
	_ = json.Unmarshal(b, &out)
	return &out
}

func (m *Manager) List(ctx context.Context) ([]*Job, error) {
	rows, err := m.Store.ListMigrations(ctx)
	if err != nil {
		return nil, err
	}
	out := []*Job{}
	for _, r := range rows {
		j := m.live(r.ID)
		if j == nil {
			j = fromRow(r)
		}
		j.Log = []logx.Entry{} // the list stays light; details carry the log
		out = append(out, j)
	}
	return out, nil
}

func (m *Manager) live(id int64) *Job {
	m.mu.Lock()
	r := m.jobs[id]
	m.mu.Unlock()
	if r == nil {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return copyJob(r.job)
}

func (m *Manager) Get(ctx context.Context, id int64) (*Job, error) {
	if j := m.live(id); j != nil {
		return j, nil
	}
	r, err := m.Store.GetMigration(ctx, id)
	if err != nil {
		return nil, err
	}
	return fromRow(r), nil
}

func (m *Manager) Cancel(id int64) error {
	m.mu.Lock()
	r := m.jobs[id]
	m.mu.Unlock()
	if r == nil {
		return store.ErrNotFound
	}
	r.cancel()
	return nil
}

func (m *Manager) Delete(ctx context.Context, id int64) error {
	if m.live(id) != nil {
		return ErrRunning
	}
	return m.Store.DeleteMigration(ctx, id)
}

// Start creates and launches a new job.
func (m *Manager) Start(name string, src Source, accounts []AccountInput, opt Options) (*Job, error) {
	if err := src.validate(); err != nil {
		return nil, err
	}
	if len(accounts) == 0 {
		return nil, errors.New("keine Postfächer angegeben")
	}
	seen := map[string]bool{}
	progress := make([]AccountProgress, len(accounts))
	for i := range accounts {
		a := &accounts[i]
		a.TargetEmail = store.Normalize(a.TargetEmail)
		if !strings.Contains(a.TargetEmail, "@") {
			return nil, fmt.Errorf("ungültige Zieladresse %q", a.TargetEmail)
		}
		if seen[a.TargetEmail] {
			return nil, fmt.Errorf("Zieladresse %s ist doppelt", a.TargetEmail)
		}
		seen[a.TargetEmail] = true
		if a.Username == "" || a.Password == "" {
			return nil, fmt.Errorf("Zugangsdaten für %s fehlen", a.TargetEmail)
		}
		if a.CreateAccount != nil && len(a.CreateAccount.Password) < 10 {
			return nil, fmt.Errorf("neues Passwort für %s muss mindestens 10 Zeichen haben", a.TargetEmail)
		}
		progress[i] = AccountProgress{TargetEmail: a.TargetEmail, Username: a.Username, Status: "pending"}
	}
	if opt.ExcludeFolders == nil {
		opt.ExcludeFolders = []string{}
	}
	if strings.TrimSpace(name) == "" {
		name = "Migration von " + src.Host
	}
	srcJSON, _ := json.Marshal(src)
	optJSON, _ := json.Marshal(opt)
	accJSON, _ := json.Marshal(progress)
	row := &store.MigrationRow{Name: name, SourceJSON: string(srcJSON), OptionsJSON: string(optJSON), Status: "pending", AccountsJSON: string(accJSON), LogJSON: "[]"}
	if err := m.Store.CreateMigration(context.Background(), row); err != nil {
		return nil, err
	}
	job := fromRow(row)
	m.launch(job, accounts)
	return m.live(job.ID), nil
}

// Restart reruns all unfinished accounts of a stopped job with fresh passwords.
func (m *Manager) Restart(ctx context.Context, id int64, passwords map[string]string) (*Job, error) {
	if m.live(id) != nil {
		return nil, ErrRunning
	}
	row, err := m.Store.GetMigration(ctx, id)
	if err != nil {
		return nil, err
	}
	job := fromRow(row)
	var inputs []AccountInput
	for i := range job.Accounts {
		a := &job.Accounts[i]
		if a.Status == "done" {
			continue
		}
		pw := passwords[a.TargetEmail]
		if pw == "" {
			return nil, fmt.Errorf("Passwort für %s fehlt", a.TargetEmail)
		}
		inputs = append(inputs, AccountInput{TargetEmail: a.TargetEmail, Username: a.Username, Password: pw})
		*a = AccountProgress{TargetEmail: a.TargetEmail, Username: a.Username, Status: "pending"}
	}
	if len(inputs) == 0 {
		return nil, errors.New("alle Postfächer sind bereits fertig")
	}
	job.Status = "pending"
	job.FinishedAt = nil
	m.launch(job, inputs)
	return m.live(job.ID), nil
}

func (m *Manager) launch(job *Job, accounts []AccountInput) {
	ctx, cancel := context.WithCancel(context.Background())
	r := &running{job: job, cancel: cancel}
	m.mu.Lock()
	m.jobs[job.ID] = r
	m.mu.Unlock()
	go m.run(ctx, r, accounts)
}

func (r *running) update(fn func(j *Job)) {
	r.mu.Lock()
	fn(r.job)
	r.dirty = true
	r.mu.Unlock()
}

func (m *Manager) logf(r *running, level, format string, args ...any) {
	msg := fmt.Sprintf(format, args...)
	e := logx.Entry{Time: time.Now().UTC(), Level: level, Component: "migration", Message: msg, Fields: map[string]string{}}
	r.update(func(j *Job) {
		j.Log = append([]logx.Entry{e}, j.Log...)
		if len(j.Log) > 100 {
			j.Log = j.Log[:100]
		}
	})
	switch level {
	case "error":
		m.Log.Error(msg, "job", r.job.ID)
	case "warn":
		m.Log.Warn(msg, "job", r.job.ID)
	default:
		m.Log.Info(msg, "job", r.job.ID)
	}
}

func (m *Manager) persist(r *running) {
	r.mu.Lock()
	if !r.dirty {
		r.mu.Unlock()
		return
	}
	j := copyJob(r.job)
	r.dirty = false
	r.mu.Unlock()
	accJSON, _ := json.Marshal(j.Accounts)
	logJSON, _ := json.Marshal(j.Log)
	_ = m.Store.SaveMigration(context.Background(), &store.MigrationRow{ID: j.ID, Status: j.Status, AccountsJSON: string(accJSON), LogJSON: string(logJSON), StartedAt: j.StartedAt, FinishedAt: j.FinishedAt})
}

func (m *Manager) run(ctx context.Context, r *running, accounts []AccountInput) {
	stopSaver := make(chan struct{})
	go func() {
		t := time.NewTicker(2 * time.Second)
		defer t.Stop()
		for {
			select {
			case <-stopSaver:
				return
			case <-t.C:
				m.persist(r)
			}
		}
	}()
	defer func() {
		if p := recover(); p != nil {
			m.logf(r, "error", "Interner Fehler: %v", p)
			r.update(func(j *Job) { j.Status = "failed" })
		}
		close(stopSaver)
		now := time.Now().UTC()
		r.update(func(j *Job) { j.FinishedAt = &now })
		m.persist(r)
		m.mu.Lock()
		delete(m.jobs, r.job.ID)
		m.mu.Unlock()
	}()

	now := time.Now().UTC()
	r.update(func(j *Job) { j.Status = "running"; j.StartedAt = &now })
	m.logf(r, "info", "Migration gestartet: %d Postfach/Postfächer von %s", len(accounts), r.job.Source.Host)

	failed := 0
	for _, in := range accounts {
		if ctx.Err() != nil {
			break
		}
		idx := -1
		for i, a := range r.job.Accounts {
			if a.TargetEmail == in.TargetEmail {
				idx = i
			}
		}
		if idx < 0 {
			continue
		}
		err := m.migrateAccount(ctx, r, idx, in)
		switch {
		case ctx.Err() != nil:
			r.update(func(j *Job) { j.Accounts[idx].Status = "cancelled" })
		case err != nil:
			failed++
			msg := err.Error()
			r.update(func(j *Job) {
				j.Accounts[idx].Status = "failed"
				j.Accounts[idx].Error = &msg
				j.Accounts[idx].CurrentFolder = nil
			})
			m.logf(r, "error", "%s: %s", in.TargetEmail, msg)
		default:
			r.update(func(j *Job) { j.Accounts[idx].Status = "done"; j.Accounts[idx].CurrentFolder = nil })
			a := r.job.Accounts[idx]
			m.logf(r, "info", "%s fertig: %d importiert, %d übersprungen, %d fehlerhaft", in.TargetEmail, a.Imported, a.Skipped, a.Failed)
		}
	}
	r.update(func(j *Job) {
		for i := range j.Accounts {
			if j.Accounts[i].Status == "pending" || j.Accounts[i].Status == "running" {
				j.Accounts[i].Status = "cancelled"
			}
		}
		switch {
		case ctx.Err() != nil:
			j.Status = "cancelled"
		case failed > 0:
			j.Status = "failed"
		default:
			j.Status = "done"
		}
	})
	m.logf(r, "info", "Migration beendet (%s)", r.job.Status)
	if m.Delivery.AI != nil {
		m.Delivery.AI.Notify()
	}
}

func (m *Manager) ensureAccount(ctx context.Context, r *running, in AccountInput) (*store.Account, error) {
	acct, err := m.Store.GetAccountByEmail(ctx, in.TargetEmail)
	if err == nil {
		return acct, nil
	}
	if !errors.Is(err, store.ErrNotFound) {
		return nil, err
	}
	if in.CreateAccount == nil {
		return nil, fmt.Errorf("Zielkonto %s existiert nicht", in.TargetEmail)
	}
	domain := store.DomainOf(in.TargetEmail)
	if ok, _ := m.Store.DomainExists(ctx, domain); !ok {
		key, err := mailauth.GenerateKey()
		if err != nil {
			return nil, err
		}
		if err := m.Store.CreateDomain(ctx, domain, "pw"+time.Now().Format("200601"), key); err != nil && !errors.Is(err, store.ErrConflict) {
			return nil, err
		}
		m.logf(r, "info", "Domain %s angelegt (DNS-Einträge in der Verwaltung prüfen)", domain)
	}
	h, err := auth.HashPassword(in.CreateAccount.Password)
	if err != nil {
		return nil, err
	}
	a := &store.Account{Email: in.TargetEmail, Name: in.CreateAccount.Name, PasswordHash: h, Role: "user"}
	if err := m.Store.CreateAccount(ctx, a); err != nil && !errors.Is(err, store.ErrConflict) {
		return nil, err
	}
	m.logf(r, "info", "Konto %s angelegt", in.TargetEmail)
	return m.Store.GetAccountByEmail(ctx, in.TargetEmail)
}

func (m *Manager) targetMailbox(ctx context.Context, acct *store.Account, target string) (int64, error) {
	switch target {
	case "inbox", "sent", "drafts", "junk", "trash", "archive":
		return m.Store.MailboxByRole(ctx, acct.ID, target)
	}
	return m.Store.EnsureMailbox(ctx, acct.ID, target)
}

func (m *Manager) migrateAccount(ctx context.Context, r *running, idx int, in AccountInput) error {
	r.update(func(j *Job) { j.Accounts[idx].Status = "running" })
	acct, err := m.ensureAccount(ctx, r, in)
	if err != nil {
		return err
	}
	c, err := dial(ctx, r.job.Source, in.Username, in.Password)
	if err != nil {
		return err
	}
	defer c.Logout()
	folders, err := listFolders(c)
	if err != nil {
		return err
	}
	opt := r.job.Options
	excluded := map[string]bool{}
	for _, f := range opt.ExcludeFolders {
		excluded[f] = true
	}
	var selected []Folder
	total := 0
	for _, f := range folders {
		if excluded[f.Name] || (f.Target == "trash" && !opt.IncludeTrash) || (f.Target == "junk" && !opt.IncludeJunk) {
			continue
		}
		selected = append(selected, f)
		total += f.Messages
	}
	r.update(func(j *Job) { j.Accounts[idx].FoldersTotal = len(selected); j.Accounts[idx].MessagesTotal = total })
	m.logf(r, "info", "%s: %d Ordner mit %d Nachrichten", in.TargetEmail, len(selected), total)

	var since time.Time
	if opt.Since != nil && *opt.Since != "" {
		since, _ = time.Parse("2006-01-02", *opt.Since)
	}
	maxSize := int(m.Delivery.Settings.Get().Server.MaxMessageSize) * 4
	for _, f := range selected {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		name := f.Name
		r.update(func(j *Job) { j.Accounts[idx].CurrentFolder = &name })
		if err := m.migrateFolder(ctx, r, idx, c, acct, f, since, maxSize, !opt.AnalyzeWithAI); err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			m.logf(r, "warn", "%s: Ordner %s: %v", in.TargetEmail, f.Name, err)
			r.update(func(j *Job) { j.Accounts[idx].Failed++ })
		}
		r.update(func(j *Job) { j.Accounts[idx].FoldersDone++ })
	}
	return nil
}

const batchSize = 50

func (m *Manager) migrateFolder(ctx context.Context, r *running, idx int, c *client.Client, acct *store.Account, f Folder, since time.Time, maxSize int, skipAI bool) error {
	mbID, err := m.targetMailbox(ctx, acct, f.Target)
	if err != nil {
		return fmt.Errorf("Zielordner %s: %w", f.Target, err)
	}
	keys, err := m.Store.MessageKeys(ctx, mbID)
	if err != nil {
		return err
	}
	if _, err := c.Select(f.Name, true); err != nil {
		return err
	}
	uids, err := folderUIDs(c, since)
	if err != nil {
		return err
	}
	// The folder count from STATUS may differ (date filter, changes); correct the total.
	if diff := len(uids) - f.Messages; diff != 0 {
		r.update(func(j *Job) { j.Accounts[idx].MessagesTotal += diff })
	}
	for start := 0; start < len(uids); start += batchSize {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		end := start + batchSize
		if end > len(uids) {
			end = len(uids)
		}
		msgs, err := fetchBatch(c, uids[start:end])
		if err != nil {
			return err
		}
		for _, msg := range msgs {
			imported, skipped, failed := 0, 0, 0
			switch {
			case msg.raw == nil:
				failed = 1
			case msg.deleted:
				skipped = 1
			case len(msg.raw) > maxSize:
				failed = 1
				m.logf(r, "warn", "%s: Nachricht UID %d in %s ist zu groß (%d MB)", acct.Email, msg.uid, f.Name, len(msg.raw)>>20)
			default:
				idKey, hashKey := delivery.ImportKey(msg.raw)
				if (idKey != "" && keys[idKey]) || keys[hashKey] {
					skipped = 1
					break
				}
				_, err := m.Delivery.ImportMessage(ctx, acct, mbID, msg.raw, delivery.ImportOptions{
					ReceivedAt: msg.date, Seen: msg.seen, Flagged: msg.flagged, Answered: msg.answered, SkipAI: skipAI,
				})
				if err != nil {
					failed = 1
					m.logf(r, "warn", "%s: UID %d in %s: %v", acct.Email, msg.uid, f.Name, err)
					if strings.Contains(err.Error(), "Quota") {
						return err
					}
					break
				}
				keys[hashKey] = true
				if idKey != "" {
					keys[idKey] = true
				}
				imported = 1
			}
			r.update(func(j *Job) {
				a := &j.Accounts[idx]
				a.MessagesDone++
				a.Imported += imported
				a.Skipped += skipped
				a.Failed += failed
			})
		}
	}
	return nil
}
