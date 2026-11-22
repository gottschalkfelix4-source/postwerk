package api

import (
	"context"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"time"

	"postwerk/internal/migrate"
	"postwerk/internal/store"
)

func (s *Server) migrationTest(w http.ResponseWriter, r *http.Request, u *store.Account) (any, error) {
	var req struct {
		Source   migrate.Source `json:"source"`
		Username string         `json:"username"`
		Password string         `json:"password"`
	}
	if err := decode(r, &req); err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(r.Context(), 90*time.Second)
	defer cancel()
	folders, err := migrate.ListFolders(ctx, req.Source, req.Username, req.Password)
	if err != nil {
		// Find out where exactly it fails (DNS, network, port, TLS, login).
		dctx, dcancel := context.WithTimeout(r.Context(), 60*time.Second)
		dg := migrate.Diagnose(dctx, req.Source, req.Username, req.Password, err)
		dcancel()
		s.Log.Warn("migration source test failed", "component", "migration", "host", req.Source.Host, "err", err, "hint", dg.Hint)
		return map[string]any{"ok": false, "folders": []any{}, "error": err.Error(), "diagnostics": dg.Steps, "hint": dg.Hint}, nil
	}
	if folders == nil {
		folders = []migrate.Folder{}
	}
	return map[string]any{"ok": true, "folders": folders}, nil
}

func (s *Server) listMigrations(w http.ResponseWriter, r *http.Request, u *store.Account) (any, error) {
	return s.Migrations.List(r.Context())
}

func (s *Server) createMigration(w http.ResponseWriter, r *http.Request, u *store.Account) (any, error) {
	var req struct {
		Name     string                 `json:"name"`
		Source   migrate.Source         `json:"source"`
		Accounts []migrate.AccountInput `json:"accounts"`
		Options  migrate.Options        `json:"options"`
	}
	if err := decode(r, &req); err != nil {
		return nil, err
	}
	for _, a := range req.Accounts {
		if a.CreateAccount != nil {
			if !validEmail(store.Normalize(a.TargetEmail)) {
				return nil, invalid("Ungültige Zieladresse: " + a.TargetEmail)
			}
		}
	}
	job, err := s.Migrations.Start(req.Name, req.Source, req.Accounts, req.Options)
	if err != nil {
		return nil, invalid(err.Error())
	}
	s.Log.Info("migration started", "component", "migration", "job", job.ID, "source", req.Source.Host, "accounts", len(req.Accounts), "by", u.Email)
	return job, nil
}

func (s *Server) getMigration(w http.ResponseWriter, r *http.Request, u *store.Account) (any, error) {
	id, err := pathID(r, "id")
	if err != nil {
		return nil, err
	}
	return s.Migrations.Get(r.Context(), id)
}

func (s *Server) cancelMigration(w http.ResponseWriter, r *http.Request, u *store.Account) (any, error) {
	id, err := pathID(r, "id")
	if err != nil {
		return nil, err
	}
	if err := s.Migrations.Cancel(id); err != nil {
		return nil, errf(409, "conflict", "Der Job läuft nicht")
	}
	return ok(), nil
}

func (s *Server) restartMigration(w http.ResponseWriter, r *http.Request, u *store.Account) (any, error) {
	id, err := pathID(r, "id")
	if err != nil {
		return nil, err
	}
	var req struct {
		Accounts []struct {
			TargetEmail string `json:"targetEmail"`
			Password    string `json:"password"`
		} `json:"accounts"`
	}
	if err := decode(r, &req); err != nil {
		return nil, err
	}
	pw := map[string]string{}
	for _, a := range req.Accounts {
		pw[store.Normalize(a.TargetEmail)] = a.Password
	}
	job, err := s.Migrations.Restart(r.Context(), id, pw)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return nil, err
		}
		return nil, invalid(err.Error())
	}
	return job, nil
}

func (s *Server) deleteMigration(w http.ResponseWriter, r *http.Request, u *store.Account) (any, error) {
	id, err := pathID(r, "id")
	if err != nil {
		return nil, err
	}
	if err := s.Migrations.Delete(r.Context(), id); err != nil {
		if errors.Is(err, migrate.ErrRunning) {
			return nil, errf(409, "conflict", "Der Job läuft noch")
		}
		return nil, err
	}
	return ok(), nil
}

const (
	maxImportSize      = 1 << 30
	maxAdminImportSize = 2 << 30
)

// runningImports holds the IDs of accounts with an import in progress.
var runningImports sync.Map

// importFile streams an uploaded .mbox/.eml/.zip to a temp file and imports it.
// Only one import per account runs at a time.
func (s *Server) importFile(w http.ResponseWriter, r *http.Request, u *store.Account) (any, error) {
	if _, busy := runningImports.LoadOrStore(u.ID, struct{}{}); busy {
		return nil, errf(409, "conflict", "Es läuft bereits ein Import für dieses Konto. Bitte warte, bis er abgeschlossen ist.")
	}
	defer runningImports.Delete(u.ID)
	limit := int64(maxImportSize)
	if u.Role == "admin" {
		limit = maxAdminImportSize
	}
	r.Body = http.MaxBytesReader(w, r.Body, limit+1<<20)
	mr, err := r.MultipartReader()
	if err != nil {
		return nil, invalid("multipart/form-data erwartet")
	}
	var mailboxID int64
	var tmpPath, filename string
	defer func() {
		if tmpPath != "" {
			os.Remove(tmpPath)
		}
	}()
	for {
		part, err := mr.NextPart()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, invalid("Upload abgebrochen: " + err.Error())
		}
		switch part.FormName() {
		case "mailboxId":
			b, _ := io.ReadAll(io.LimitReader(part, 32))
			if len(b) > 0 {
				id, err := strconv.ParseInt(string(b), 10, 64)
				if err != nil {
					return nil, invalid("Ungültiger Ordner")
				}
				if _, err := s.Store.MailboxOwned(r.Context(), u.ID, id); err != nil {
					return nil, err
				}
				mailboxID = id
			}
		case "file":
			// One file per request: a second part would leave the first
			// temp file behind (only tmpPath is removed afterwards).
			if tmpPath != "" {
				return nil, invalid("Bitte nur eine Datei pro Import hochladen")
			}
			filename = filepath.Base(part.FileName())
			f, err := os.CreateTemp(filepath.Join(s.Cfg.DataDir), "import-*.tmp")
			if err != nil {
				return nil, err
			}
			tmpPath = f.Name()
			_, err = io.Copy(f, part)
			f.Close()
			if err != nil {
				return nil, invalid("Upload fehlgeschlagen: " + err.Error())
			}
		}
		part.Close()
	}
	if tmpPath == "" {
		return nil, invalid("Keine Datei hochgeladen")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Hour)
	defer cancel()
	fi := &migrate.FileImporter{Delivery: s.Delivery, Account: u, MailboxID: mailboxID, MaxSize: int(s.Settings.Get().Server.MaxMessageSize) * 4}
	res, err := fi.ImportFile(ctx, tmpPath, filename)
	if err != nil {
		return nil, invalid(err.Error())
	}
	s.Log.Info("file imported", "component", "migration", "user", u.Email, "file", filename,
		"imported", res.Imported, "skipped", res.Skipped, "failed", res.Failed)
	if s.AIWorker != nil {
		s.AIWorker.Notify()
	}
	return res, nil
}
