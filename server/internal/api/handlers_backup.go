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
	"sync"
	"time"
	"unicode/utf8"

	"postwerk/internal/auth"
	"postwerk/internal/backup"
	"postwerk/internal/store"
)

// A backup holds every mailbox, all secrets and the master key. Downloading
// one therefore needs the admin's password (and 2FA code) once more: the
// confirmation yields a short-lived, single-use token for the download link.
type downloadToken struct {
	name    string
	account int64
	expires time.Time
}

var (
	backupTokensMu sync.Mutex
	backupTokens   = map[string]downloadToken{}
)

const backupTokenTTL = 2 * time.Minute

func (s *Server) backupDownloadToken(w http.ResponseWriter, r *http.Request, u *store.Account) (any, error) {
	var req struct {
		Password string `json:"password"`
		Code     string `json:"code"`
	}
	if err := decode(r, &req); err != nil {
		return nil, err
	}
	if err := s.reauth(r, u, req.Password, req.Code, true); err != nil {
		return nil, err
	}
	name := r.PathValue("name")
	if !backup.ValidName(name) {
		return nil, errNotFound
	}
	tok := auth.RandomToken(24)
	now := time.Now()
	backupTokensMu.Lock()
	for k, t := range backupTokens {
		if now.After(t.expires) {
			delete(backupTokens, k)
		}
	}
	backupTokens[tok] = downloadToken{name: name, account: u.ID, expires: now.Add(backupTokenTTL)}
	backupTokensMu.Unlock()
	s.Log.Info("backup download authorized", "component", "backup", "archive", name, "by", u.Email)
	return map[string]string{"token": tok}, nil
}

func takeBackupToken(tok, name string, account int64) bool {
	backupTokensMu.Lock()
	defer backupTokensMu.Unlock()
	t, ok := backupTokens[tok]
	if !ok {
		return false
	}
	delete(backupTokens, tok)
	return t.name == name && t.account == account && time.Now().Before(t.expires)
}

func backupErr(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, backup.ErrNotFound):
		return errNotFound
	case errors.Is(err, backup.ErrBusy):
		return errf(409, "busy", err.Error())
	case errors.Is(err, backup.ErrPassword), errors.Is(err, backup.ErrNeedPassword):
		return errf(400, "archive_password", err.Error())
	case errors.Is(err, backup.ErrKeyMissing):
		return errf(422, "master_key_required", err.Error())
	case errors.Is(err, backup.ErrKeyMismatch):
		return errf(422, "master_key_mismatch", err.Error())
	case errors.Is(err, backup.ErrFormat):
		return errf(422, "archive_format", err.Error())
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
	if !takeBackupToken(r.URL.Query().Get("token"), name, u.ID) {
		return nil, errf(403, "reauth_required", "Der Download-Link ist abgelaufen – bitte erneut bestätigen.")
	}
	rc, size, err := s.Backups.Open(r.Context(), name)
	if err != nil {
		return nil, backupErr(err)
	}
	defer rc.Close()
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s"`, name))
	w.Header().Set("Cache-Control", "no-store")
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
		Password        string `json:"password"` // archive password
		MasterKey       string `json:"masterKey"` // only for archives without master.key
		AccountPassword string `json:"accountPassword"`
		Code            string `json:"code"`
	}
	if err := decode(r, &req); err != nil {
		return nil, err
	}
	// A restore replaces the whole server: confirm the admin's identity.
	if err := s.reauth(r, u, req.AccountPassword, req.Code, true); err != nil {
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
	if err := s.Backups.StartRestore(ctx, name, req.Password, req.MasterKey); err != nil {
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
	for _, sc := range []maskedSecret{
		{&req.WebDAV.Password, cur.WebDAV.Password, req.WebDAV.URL, cur.WebDAV.URL, webdavSecretMsg},
		{&req.SMB.Password, cur.SMB.Password, req.SMB.Host, cur.SMB.Host, smbSecretMsg},
	} {
		if err := sc.restore(); err != nil {
			return nil, err
		}
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

const minBackupPassword = 12

// validateBackup checks backup settings; old is the stored state, so a short
// password from before the current minimum keeps working until it is changed.
func validateBackup(b, old *store.BackupSettings) error {
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
	// Archives contain every mailbox, all secrets and the master key; on
	// someone else's storage they must be encrypted.
	if b.Target.Type != "local" && !b.Encrypt {
		return invalid("Sicherungen auf WebDAV- oder SMB-Speicher müssen verschlüsselt werden – bitte die Verschlüsselung einschalten und ein Backup-Passwort vergeben")
	}
	if b.Encrypt && b.Password == "" {
		return invalid("Bitte ein Backup-Passwort angeben")
	}
	if b.Encrypt && (!old.Encrypt || b.Password != old.Password) && utf8.RuneCountInString(b.Password) < minBackupPassword {
		return invalid(fmt.Sprintf("Das Backup-Passwort muss mindestens %d Zeichen haben", minBackupPassword))
	}
	if b.Target.SMB.Port == 0 {
		b.Target.SMB.Port = 445
	}
	if b.Target.SMB.Port < 1 || b.Target.SMB.Port > 65535 {
		return invalid("Ungültiger SMB-Port")
	}
	return nil
}
