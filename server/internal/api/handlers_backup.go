package api

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"postwerk/internal/backup"
	"postwerk/internal/store"
)

func backupErr(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, backup.ErrNotFound):
		return errNotFound
	case errors.Is(err, backup.ErrBusy):
		return errf(409, "busy", err.Error())
	case errors.Is(err, backup.ErrPassword), errors.Is(err, backup.ErrNeedPassword):
		return invalid(err.Error())
	}
	return invalid(err.Error())
}

func (s *Server) backupStatus(w http.ResponseWriter, r *http.Request, u *store.Account) (any, error) {
	runs, err := s.Store.BackupRuns(r.Context(), 30)
	if err != nil {
		return nil, err
	}
	return map[string]any{"status": s.Backups.Status(), "runs": runs}, nil
}

func (s *Server) backupArchives(w http.ResponseWriter, r *http.Request, u *store.Account) (any, error) {
	ctx, cancel := context.WithTimeout(r.Context(), 60*time.Second)
	defer cancel()
	list, err := s.Backups.List(ctx)
	if err != nil {
		// The page still works when the target is unreachable.
		return map[string]any{"archives": []backup.Entry{}, "error": err.Error()}, nil
	}
	return map[string]any{"archives": list}, nil
}

func (s *Server) backupStart(w http.ResponseWriter, r *http.Request, u *store.Account) (any, error) {
	if err := s.Backups.Start("manual"); err != nil {
		return nil, backupErr(err)
	}
	s.Log.Info("backup started", "component", "backup", "by", u.Email)
	return s.Backups.Status(), nil
}

func (s *Server) backupDownload(w http.ResponseWriter, r *http.Request, u *store.Account) (any, error) {
	name := r.PathValue("name")
	rc, size, err := s.Backups.Open(r.Context(), name)
	if err != nil {
		return nil, backupErr(err)
	}
	defer rc.Close()
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s"`, name))
	if size > 0 {
		w.Header().Set("Content-Length", strconv.FormatInt(size, 10))
	}
	if _, err := io.Copy(w, rc); err != nil {
		s.Log.Warn("backup download aborted", "component", "backup", "archive", name, "err", err)
	}
	return nil, nil
}

func (s *Server) backupDelete(w http.ResponseWriter, r *http.Request, u *store.Account) (any, error) {
	if err := s.Backups.Delete(r.Context(), r.PathValue("name")); err != nil {
		return nil, backupErr(err)
	}
	return ok(), nil
}

func (s *Server) backupRestore(w http.ResponseWriter, r *http.Request, u *store.Account) (any, error) {
	var req struct {
		Password string `json:"password"`
	}
	if err := decode(r, &req); err != nil {
		return nil, err
	}
	name := r.PathValue("name")
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Minute)
	defer cancel()
	if req.Password == "" {
		// Archives written by this server use the configured password.
		if cfg := s.Settings.Get().Backup; cfg.Password != "" {
			req.Password = cfg.Password
		}
	}
	if err := s.Backups.StartRestore(ctx, name, req.Password); err != nil {
		return nil, backupErr(err)
	}
	s.Log.Warn("restore started", "component", "backup", "archive", name, "by", u.Email)
	return s.Backups.Status(), nil
}

const maxBackupUpload = 200 << 30

func (s *Server) backupUpload(w http.ResponseWriter, r *http.Request, u *store.Account) (any, error) {
	r.Body = http.MaxBytesReader(w, r.Body, maxBackupUpload)
	mr, err := r.MultipartReader()
	if err != nil {
		return nil, invalid("multipart/form-data erwartet")
	}
	for {
		part, err := mr.NextPart()
		if err == io.EOF {
			return nil, invalid("Keine Datei hochgeladen")
		}
		if err != nil {
			return nil, invalid("Upload abgebrochen: " + err.Error())
		}
		if part.FormName() != "file" {
			part.Close()
			continue
		}
		f, err := os.CreateTemp(s.Cfg.DataDir, ".backup-import-*.tmp")
		if err != nil {
			return nil, err
		}
		defer os.Remove(f.Name())
		defer f.Close()
		if _, err := io.Copy(f, part); err != nil {
			return nil, invalid("Upload fehlgeschlagen: " + err.Error())
		}
		if _, err := f.Seek(0, io.SeekStart); err != nil {
			return nil, err
		}
		ctx, cancel := context.WithTimeout(context.Background(), 6*time.Hour)
		defer cancel()
		name, err := s.Backups.Upload(ctx, filepath.Base(part.FileName()), f)
		if err != nil {
			return nil, invalid(err.Error())
		}
		s.Log.Info("backup archive uploaded", "component", "backup", "archive", name, "by", u.Email)
		return map[string]any{"name": name}, nil
	}
}

func (s *Server) backupTest(w http.ResponseWriter, r *http.Request, u *store.Account) (any, error) {
	var req store.BackupTarget
	if err := decode(r, &req); err != nil {
		return nil, err
	}
	cur := s.Settings.Get().Backup.Target
	if req.WebDAV.Password == mask {
		req.WebDAV.Password = cur.WebDAV.Password
	}
	if req.SMB.Password == mask {
		req.SMB.Password = cur.SMB.Password
	}
	ctx, cancel := context.WithTimeout(r.Context(), 60*time.Second)
	defer cancel()
	start := time.Now()
	where, n, err := s.Backups.TestTarget(ctx, req)
	res := map[string]any{"ok": err == nil, "target": where, "archives": n, "latencyMs": time.Since(start).Milliseconds()}
	if err != nil {
		res["error"] = err.Error()
	}
	return res, nil
}

func validateBackup(b *store.BackupSettings) error {
	switch b.Target.Type {
	case "local", "webdav", "smb":
	default:
		return invalid("Unbekanntes Backup-Ziel")
	}
	switch b.Schedule.Frequency {
	case "hourly", "daily", "weekly":
	default:
		return invalid("Ungültige Backup-Häufigkeit")
	}
	if b.Schedule.Frequency != "hourly" {
		if _, err := time.Parse("15:04", b.Schedule.Time); err != nil {
			return invalid("Backup-Uhrzeit im Format HH:MM angeben")
		}
	}
	if b.Schedule.Weekday < 0 || b.Schedule.Weekday > 6 {
		return invalid("Ungültiger Wochentag")
	}
	if b.Keep < 0 || b.Keep > 1000 {
		return invalid("Aufbewahrung: 0 bis 1000 Sicherungen")
	}
	if b.Encrypt && len(b.Password) < 8 {
		return invalid("Das Backup-Passwort muss mindestens 8 Zeichen haben")
	}
	if b.Target.SMB.Port == 0 {
		b.Target.SMB.Port = 445
	}
	if b.Target.SMB.Port < 1 || b.Target.SMB.Port > 65535 {
		return invalid("Ungültiger SMB-Port")
	}
	return nil
}
