package api

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"mime/multipart"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"testing/fstest"
	"time"

	"postwerk/internal/auth"
	"postwerk/internal/backup"
	"postwerk/internal/config"
	"postwerk/internal/store"
)

// newFreshEnv is a server without any account, as on first start.
func newFreshEnv(t *testing.T) (*testEnv, chan struct{}) {
	t.Helper()
	t.Setenv("POSTWERK_MASTER_KEY", "")
	t.Setenv("POSTWERK_MASTER_KEY_FILE", "")
	dir := t.TempDir()
	st, err := store.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	settings, err := st.LoadSettings(context.Background(), "neu.example.test")
	if err != nil {
		t.Fatal(err)
	}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	restarted := make(chan struct{}, 4)
	guard := auth.NewGuard(func() auth.Limits { return auth.Limits{MaxAttempts: 50, Window: time.Minute, BanFor: time.Minute} })
	s := &Server{Cfg: &config.Config{DataDir: dir}, Store: st, Settings: settings, Auth: &auth.Authenticator{Store: st, Guard: guard},
		Log: log, UI: fstest.MapFS{}, SetupToken: "SETUP-TOKEN-1234",
		Backups: &backup.Manager{Store: st, Settings: settings, DataDir: dir, Version: "test", Log: log, Restart: func() { restarted <- struct{}{} }}}
	return &testEnv{s: s, h: s.Handler(), dir: dir}, restarted
}

// oldServerArchive builds an unencrypted archive of another server.
func oldServerArchive(t *testing.T) []byte {
	t.Helper()
	dir := t.TempDir()
	st, err := store.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if err := st.CreateAccount(context.Background(), &store.Account{Email: "anna@alt.example.test", PasswordHash: "x", Role: "admin"}); err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	man := backup.Manifest{Version: "test", Hostname: "mail.alt.example.test", Accounts: 1, KeyFingerprint: st.Vault.Fingerprint()}
	if _, err := backup.Create(context.Background(), &buf, backup.Source{DataDir: dir, DB: st.DB, Manifest: man}, ""); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func (e *testEnv) upload(t *testing.T, fields map[string]string, filename string, data []byte) *httptest.ResponseRecorder {
	t.Helper()
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	for _, k := range []string{"token", "password", "masterKey"} {
		if v, ok := fields[k]; ok {
			mw.WriteField(k, v)
		}
	}
	fw, _ := mw.CreateFormFile("file", filename)
	fw.Write(data)
	mw.Close()
	r := httptest.NewRequest("POST", "/api/setup/restore/upload", &body)
	r.RemoteAddr = "198.51.100.10:5555"
	r.Header.Set("X-Postwerk", "1")
	r.Header.Set("Content-Type", mw.FormDataContentType())
	rec := httptest.NewRecorder()
	e.h.ServeHTTP(rec, r)
	return rec
}

func TestSetupRestoreUpload(t *testing.T) {
	e, restarted := newFreshEnv(t)
	archive := oldServerArchive(t)

	if rec := e.upload(t, map[string]string{"token": "falsch"}, "postwerk-20261120-033000.tar.gz", archive); rec.Code != 403 {
		t.Fatalf("wrong token: %d %s", rec.Code, rec.Body.String())
	}
	if rec := e.upload(t, map[string]string{"token": "SETUP-TOKEN-1234"}, "x.tar.gz", []byte("keine Sicherung")); rec.Code != 400 {
		t.Fatalf("garbage: %d %s", rec.Code, rec.Body.String())
	}
	if _, err := os.Stat(filepath.Join(e.dir, "restore-pending")); err == nil {
		t.Fatal("garbage marked as pending")
	}
	rec := e.upload(t, map[string]string{"token": "setup-token-1234", "password": ""}, "postwerk-20261120-033000.tar.gz", archive)
	if rec.Code != 200 {
		t.Fatalf("upload: %d %s", rec.Code, rec.Body.String())
	}
	var res struct{ Manifest backup.Manifest }
	json.Unmarshal(rec.Body.Bytes(), &res)
	if res.Manifest.Hostname != "mail.alt.example.test" || res.Manifest.Accounts != 1 || !res.Manifest.KeyInArchive {
		t.Errorf("manifest = %+v", res.Manifest)
	}
	if _, err := os.Stat(filepath.Join(e.dir, "restore-pending")); err != nil {
		t.Error("restore not pending after upload")
	}
	select {
	case <-restarted:
	case <-time.After(10 * time.Second):
		t.Fatal("no restart")
	}
	if rec := e.do(t, "POST", "/api/setup/restore/status", map[string]string{"token": "SETUP-TOKEN-1234"}, nil); rec.Code != 200 {
		t.Errorf("status: %d %s", rec.Code, rec.Body.String())
	}
}

func TestSetupRestoreFromTarget(t *testing.T) {
	e, restarted := newFreshEnv(t)
	src := t.TempDir()
	os.WriteFile(filepath.Join(src, "postwerk-20261120-033000.tar.gz"), oldServerArchive(t), 0o600)
	target := store.BackupTarget{Type: "local", LocalPath: src}

	rec := e.do(t, "POST", "/api/setup/restore/archives", map[string]any{"token": "SETUP-TOKEN-1234", "target": target}, nil)
	var list struct {
		Archives []backup.Entry
		Error    string
	}
	json.Unmarshal(rec.Body.Bytes(), &list)
	if rec.Code != 200 || len(list.Archives) != 1 || list.Error != "" {
		t.Fatalf("archives: %d %s", rec.Code, rec.Body.String())
	}
	rec = e.do(t, "POST", "/api/setup/restore/start", map[string]any{"token": "SETUP-TOKEN-1234", "target": target, "name": list.Archives[0].Name}, nil)
	if rec.Code != 200 {
		t.Fatalf("start: %d %s", rec.Code, rec.Body.String())
	}
	select {
	case <-restarted:
	case <-time.After(10 * time.Second):
		t.Fatalf("no restart: %+v", e.s.Backups.Status())
	}
	if _, err := os.Stat(filepath.Join(e.dir, "restore-pending")); err != nil {
		t.Error("restore not pending")
	}
}

func TestSetupRestoreOnlyWhileUninitialized(t *testing.T) {
	e, _ := newFreshEnv(t)
	// A rolled back restore is reported by the setup status.
	os.WriteFile(filepath.Join(e.dir, "restore-failed.json"), []byte(`{"archive":"a.tar.gz","error":"kaputt"}`), 0o600)
	rec := e.do(t, "GET", "/api/setup/status", nil, nil)
	var st struct{ Mode, RestoreError string }
	json.Unmarshal(rec.Body.Bytes(), &st)
	if st.Mode != "initial" || st.RestoreError != "kaputt" {
		t.Errorf("setup status = %s", rec.Body.String())
	}

	if err := e.s.Store.CreateAccount(context.Background(), &store.Account{Email: "admin@neu.example.test", PasswordHash: "x", Role: "admin"}); err != nil {
		t.Fatal(err)
	}
	rec = e.do(t, "POST", "/api/setup/restore/archives", map[string]any{"token": "SETUP-TOKEN-1234", "target": store.BackupTarget{Type: "local"}}, nil)
	if rec.Code != 409 || errCode(rec) != "already_initialized" {
		t.Fatalf("restore after setup: %d %s", rec.Code, rec.Body.String())
	}
	if rec := e.upload(t, map[string]string{"token": "SETUP-TOKEN-1234"}, "x.tar.gz", oldServerArchive(t)); rec.Code != 409 {
		t.Fatalf("upload after setup: %d %s", rec.Code, rec.Body.String())
	}
}
