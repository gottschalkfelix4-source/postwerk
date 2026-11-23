package api

import (
	"context"
	"crypto/subtle"
	"io"
	"net/http"
	"path/filepath"
	"strings"
	"time"

	"postwerk/internal/auth"
	"postwerk/internal/backup"
	"postwerk/internal/store"
)

// Restoring a backup from the setup assistant: on a fresh server (no account
// yet) the setup token authorizes loading an archive, either uploaded or from
// a backup target entered in the assistant. The archive is unpacked and
// checked, then the server restarts with the restored data.

// setupTokenCheck verifies the setup token while the server is still
// uninitialized; failures count towards the login ban.
func (s *Server) setupTokenCheck(r *http.Request, token string) error {
	ip := s.clientIP(r)
	if s.Auth.Guard.Banned(ip) {
		return errf(429, "rate_limited", "Zu viele Fehlversuche. Bitte später erneut versuchen.")
	}
	if mode, err := s.setupMode(r.Context()); err != nil {
		return err
	} else if mode != "initial" {
		return errf(409, "already_initialized", "Postwerk ist bereits eingerichtet – eine Sicherung lässt sich dann unter Administration → Sicherung einspielen")
	}
	if s.SetupToken == "" || subtle.ConstantTimeCompare([]byte(auth.NormalizeAppPassword(token)), []byte(auth.NormalizeAppPassword(s.SetupToken))) != 1 {
		s.Auth.Guard.Fail(ip, "setup token")
		return errf(403, "invalid_token", "Setup-Token ist ungültig")
	}
	if s.Backups == nil {
		return errf(503, "unavailable", "Sicherungen sind nicht verfügbar")
	}
	return nil
}

type setupRestoreTarget struct {
	Token  string             `json:"token"`
	Target store.BackupTarget `json:"target"`
}

// setupRestoreArchives lists the archives on a backup target.
func (s *Server) setupRestoreArchives(w http.ResponseWriter, r *http.Request) (any, error) {
	var req setupRestoreTarget
	if err := decode(r, &req); err != nil {
		return nil, err
	}
	if err := s.setupTokenCheck(r, req.Token); err != nil {
		return nil, err
	}
	if req.Target.SMB.Port == 0 {
		req.Target.SMB.Port = 445
	}
	ctx, cancel := context.WithTimeout(r.Context(), 60*time.Second)
	defer cancel()
	list, where, err := s.Backups.ListTarget(ctx, req.Target)
	if err != nil {
		return map[string]any{"archives": []backup.Entry{}, "target": where, "error": err.Error()}, nil
	}
	if list == nil {
		list = []backup.Entry{}
	}
	return map[string]any{"archives": list, "target": where}, nil
}

// setupRestoreStart restores an archive from a backup target in the
// background; progress comes from setupRestoreStatus.
func (s *Server) setupRestoreStart(w http.ResponseWriter, r *http.Request) (any, error) {
	var req struct {
		setupRestoreTarget
		Name      string `json:"name"`
		Password  string `json:"password"`
		MasterKey string `json:"masterKey"`
	}
	if err := decode(r, &req); err != nil {
		return nil, err
	}
	if err := s.setupTokenCheck(r, req.Token); err != nil {
		return nil, err
	}
	if req.Target.SMB.Port == 0 {
		req.Target.SMB.Port = 445
	}
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Minute)
	defer cancel()
	if err := s.Backups.StartRestoreFrom(ctx, req.Target, req.Name, req.Password, req.MasterKey); err != nil {
		return nil, backupErr(err)
	}
	s.Log.Warn("setup: restore started", "component", "setup", "archive", req.Name)
	return s.Backups.Status(), nil
}

// setupRestoreUpload receives an archive as multipart/form-data (fields
// token, password, masterKey before the file) and unpacks it while it
// arrives, so the archive never needs twice its size on disk.
func (s *Server) setupRestoreUpload(w http.ResponseWriter, r *http.Request) (any, error) {
	r.Body = http.MaxBytesReader(w, r.Body, maxBackupUpload)
	mr, err := r.MultipartReader()
	if err != nil {
		return nil, invalid("multipart/form-data erwartet")
	}
	fields := map[string]string{}
	for {
		part, err := mr.NextPart()
		if err == io.EOF {
			return nil, invalid("Keine Datei hochgeladen")
		}
		if err != nil {
			return nil, invalid("Upload abgebrochen: " + err.Error())
		}
		name := part.FormName()
		if name != "file" {
			b, _ := io.ReadAll(io.LimitReader(part, 4096))
			part.Close()
			fields[name] = string(b)
			continue
		}
		if err := s.setupTokenCheck(r, fields["token"]); err != nil {
			return nil, err
		}
		archive := filepath.Base(part.FileName())
		if !backup.ValidName(archive) {
			archive = "upload-" + time.Now().Format("20060102-150405") + ".tar.gz"
		}
		ctx, cancel := context.WithTimeout(context.Background(), 12*time.Hour)
		defer cancel()
		s.Log.Warn("setup: restore from upload started", "component", "setup", "archive", archive)
		man, err := s.Backups.RestoreStream(ctx, part, r.ContentLength, archive, fields["password"], strings.TrimSpace(fields["masterKey"]))
		if err != nil {
			return nil, backupErr(err)
		}
		return map[string]any{"manifest": man}, nil
	}
}

// setupRestoreResume continues a restore that stopped because the master
// key was missing or wrong; the archive does not have to be loaded again.
func (s *Server) setupRestoreResume(w http.ResponseWriter, r *http.Request) (any, error) {
	var req struct {
		Token     string `json:"token"`
		MasterKey string `json:"masterKey"`
	}
	if err := decode(r, &req); err != nil {
		return nil, err
	}
	if err := s.setupTokenCheck(r, req.Token); err != nil {
		return nil, err
	}
	man, err := s.Backups.ResumeRestore(r.Context(), req.MasterKey)
	if err != nil {
		return nil, backupErr(err)
	}
	return map[string]any{"manifest": man}, nil
}

func (s *Server) setupRestoreStatus(w http.ResponseWriter, r *http.Request) (any, error) {
	var req struct {
		Token string `json:"token"`
	}
	if err := decode(r, &req); err != nil {
		return nil, err
	}
	if err := s.setupTokenCheck(r, req.Token); err != nil {
		return nil, err
	}
	return s.Backups.Status(), nil
}
