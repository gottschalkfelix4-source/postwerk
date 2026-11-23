package backup

import (
	"bytes"
	"context"
	"crypto/rand"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/net/webdav"

	"postwerk/internal/store"
)

func TestEncryptRoundTrip(t *testing.T) {
	for _, size := range []int{0, 1, chunkSize - 1, chunkSize, chunkSize + 1, 3*chunkSize + 17} {
		data := make([]byte, size)
		rand.Read(data)
		var buf bytes.Buffer
		w, err := NewEncryptWriter(&buf, "geheim")
		if err != nil {
			t.Fatal(err)
		}
		w.Write(data)
		w.Close()
		enc := buf.Bytes()

		plain, encrypted, err := Open(bytes.NewReader(enc), "geheim")
		if err != nil || !encrypted {
			t.Fatalf("size %d: open = %v, %v", size, encrypted, err)
		}
		got, err := io.ReadAll(plain)
		if err != nil || !bytes.Equal(got, data) {
			t.Fatalf("size %d: round trip failed: %v", size, err)
		}
		if _, _, err := Open(bytes.NewReader(enc), ""); !errors.Is(err, ErrNeedPassword) {
			t.Errorf("size %d: missing password: %v", size, err)
		}
		if p, _, err := Open(bytes.NewReader(enc), "falsch"); err == nil {
			if _, err := io.ReadAll(p); !errors.Is(err, ErrPassword) {
				t.Errorf("size %d: wrong password: %v", size, err)
			}
		}
		if size > 0 {
			// Truncation must be detected.
			p, _, _ := Open(bytes.NewReader(enc[:len(enc)-5]), "geheim")
			if _, err := io.ReadAll(p); err == nil {
				t.Errorf("size %d: truncated archive accepted", size)
			}
		}
	}
}

func newStore(t *testing.T) (*store.Store, string) {
	t.Helper()
	dir := t.TempDir()
	st, err := store.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	return st, dir
}

func TestArchiveCreateExtractApply(t *testing.T) {
	ctx := context.Background()
	st, dir := newStore(t)
	acct := &store.Account{Email: "anna@example.test", PasswordHash: "x", Role: "user"}
	if err := st.CreateAccount(ctx, acct); err != nil {
		t.Fatal(err)
	}
	hash, err := st.Blobs.Put([]byte("Subject: Hallo\r\n\r\nText"))
	if err != nil {
		t.Fatal(err)
	}
	os.MkdirAll(filepath.Join(dir, "acme"), 0o750)
	os.WriteFile(filepath.Join(dir, "acme", "cert"), []byte("zertifikat"), 0o600)
	os.MkdirAll(filepath.Join(dir, "backups"), 0o750)
	os.WriteFile(filepath.Join(dir, "backups", "alt.tar.gz"), []byte("nicht mitsichern"), 0o600)

	for _, pw := range []string{"", "passwort"} {
		var buf bytes.Buffer
		n, err := Create(ctx, &buf, Source{DataDir: dir, DB: st.DB, Manifest: Manifest{Hostname: "mail.example.test"}}, pw)
		if err != nil {
			t.Fatal(err)
		}
		if n < 3 {
			t.Errorf("files = %d", n)
		}
		staging := StagingDir(dir)
		os.RemoveAll(staging)
		man, err := Extract(ctx, bytes.NewReader(buf.Bytes()), pw, staging)
		if err != nil {
			t.Fatalf("extract (pw %q): %v", pw, err)
		}
		if man.Accounts != 0 || man.Hostname != "mail.example.test" || man.Encrypted != (pw != "") {
			t.Errorf("manifest = %+v", man)
		}
		for _, p := range []string{"data/postwerk.db", "data/acme/cert"} {
			if _, err := os.Stat(filepath.Join(staging, filepath.FromSlash(p))); err != nil {
				t.Errorf("%s missing", p)
			}
		}
		if _, err := os.Stat(filepath.Join(staging, "data", "backups")); err == nil {
			t.Error("backups folder must not be archived")
		}
		blob := filepath.Join(staging, "data", "blobs")
		found := false
		filepath.WalkDir(blob, func(p string, d os.DirEntry, err error) error {
			if err == nil && !d.IsDir() && strings.HasSuffix(filepath.ToSlash(p), hash[:2]+"/"+hash[2:]) {
				found = true
			}
			return nil
		})
		if !found {
			t.Error("blob missing in archive")
		}
	}

	// A run recorded after the archive must survive the restore.
	id, _ := st.StartBackupRun(ctx, "manual", "x")
	st.FinishBackupRun(ctx, id, "postwerk-20261121-120000.tar.gz", 1, 1, nil)

	// Apply: the staged data replaces the live directory on the next start.
	if err := markPending(dir, "test.tar.gz"); err != nil {
		t.Fatal(err)
	}
	st.Close()
	os.WriteFile(filepath.Join(dir, "acme", "cert"), []byte("geändert"), 0o600)
	applied, err := ApplyPending(dir)
	if err != nil || applied == nil || applied.Archive != "test.tar.gz" {
		t.Fatalf("apply = %+v, %v", applied, err)
	}
	b, _ := os.ReadFile(filepath.Join(dir, "acme", "cert"))
	if string(b) != "zertifikat" {
		t.Errorf("cert after restore = %q", b)
	}
	if _, err := os.Stat(filepath.Join(dir, "backups", "alt.tar.gz")); err != nil {
		t.Error("backups folder must survive a restore")
	}
	st2, err := store.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer st2.Close()
	if a, err := st2.GetAccountByEmail(ctx, "anna@example.test"); err != nil || a == nil {
		t.Errorf("account after restore: %v", err)
	}
	runs, _ := st2.BackupRuns(ctx, 10)
	if len(runs) != 2 || runs[0].Trigger != "restore" || runs[0].Archive != "test.tar.gz" || runs[1].Archive != "postwerk-20261121-120000.tar.gz" {
		t.Errorf("history after restore = %+v", runs)
	}
	if a, _ := ApplyPending(dir); a != nil {
		t.Error("restore applied twice")
	}
}

func TestSchedule(t *testing.T) {
	loc := time.FixedZone("CET", 3600)
	now := time.Date(2026, 11, 21, 10, 15, 0, 0, loc) // Saturday
	cases := []struct {
		s          store.BackupSchedule
		last, next string
	}{
		{store.BackupSchedule{Frequency: "hourly"}, "2026-11-21 10:00", "2026-11-21 11:00"},
		{store.BackupSchedule{Frequency: "daily", Time: "03:30"}, "2026-11-21 03:30", "2026-11-22 03:30"},
		{store.BackupSchedule{Frequency: "daily", Time: "23:00"}, "2026-11-20 23:00", "2026-11-21 23:00"},
		{store.BackupSchedule{Frequency: "weekly", Time: "03:30", Weekday: 0}, "2026-11-15 03:30", "2026-11-22 03:30"},
		{store.BackupSchedule{Frequency: "weekly", Time: "12:00", Weekday: 6}, "2026-11-14 12:00", "2026-11-21 12:00"},
	}
	for _, c := range cases {
		if got := LastSlot(c.s, now).Format("2006-01-02 15:04"); got != c.last {
			t.Errorf("%+v last = %s, want %s", c.s, got, c.last)
		}
		if got := NextSlot(c.s, now).Format("2006-01-02 15:04"); got != c.next {
			t.Errorf("%+v next = %s, want %s", c.s, got, c.next)
		}
	}
}

func testTarget(t *testing.T, tg Target) {
	t.Helper()
	ctx := context.Background()
	names := []string{"postwerk-20261120-033000.tar.gz", "postwerk-20261121-033000.tar.gz.enc"}
	for _, n := range names {
		data := []byte("inhalt " + n)
		size := int64(len(data))
		if tg.Streaming() {
			size = -1
		}
		if err := tg.Put(ctx, n, bytes.NewReader(data), size); err != nil {
			t.Fatalf("put: %v", err)
		}
	}
	if err := tg.Put(ctx, "fremd.txt", strings.NewReader("x"), 1); err != nil {
		t.Fatal(err)
	}
	list, err := tg.List(ctx)
	if err != nil || len(list) != 2 || list[0].Name != names[1] || !list[0].Encrypted || list[1].Size != int64(len("inhalt "+names[0])) {
		t.Fatalf("list = %+v, %v", list, err)
	}
	rc, _, err := tg.Open(ctx, names[0])
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(rc)
	rc.Close()
	if string(b) != "inhalt "+names[0] {
		t.Errorf("open = %q", b)
	}
	if err := tg.Delete(ctx, names[0]); err != nil {
		t.Fatal(err)
	}
	if _, _, err := tg.Open(ctx, names[0]); !errors.Is(err, ErrNotFound) {
		t.Errorf("open deleted = %v", err)
	}
}

func TestLocalTarget(t *testing.T) {
	testTarget(t, &localTarget{dir: filepath.Join(t.TempDir(), "a", "b")})
}

func TestWebDAVTarget(t *testing.T) {
	h := &webdav.Handler{FileSystem: webdav.NewMemFS(), LockSystem: webdav.NewMemLS()}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if u, p, ok := r.BasicAuth(); !ok || u != "felix" || p != "pw" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		h.ServeHTTP(w, r)
	}))
	defer srv.Close()
	tg, err := newWebDAV(srv.URL+"/remote.php/dav/files/felix/Postwerk Backups", "felix", "pw", false)
	if err != nil {
		t.Fatal(err)
	}
	testTarget(t, tg)

	bad, _ := newWebDAV(srv.URL+"/x", "felix", "falsch", false)
	if _, err := bad.List(context.Background()); err == nil || !strings.Contains(err.Error(), "Anmeldung") {
		t.Errorf("wrong password: %v", err)
	}
}

func TestManagerBackupAndRetention(t *testing.T) {
	ctx := context.Background()
	st, dir := newStore(t)
	settings, err := st.LoadSettings(ctx, "mail.example.test")
	if err != nil {
		t.Fatal(err)
	}
	cfg := settings.Get()
	cfg.Backup.Keep = 2
	cfg.Backup.Encrypt = true
	cfg.Backup.Password = "pw"
	settings.Save(ctx, cfg)
	m := &Manager{Store: st, Settings: settings, DataDir: dir, Version: "test", Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	for i := 0; i < 3; i++ {
		if err := m.Start("manual"); err != nil {
			t.Fatal(err)
		}
		deadline := time.Now().Add(20 * time.Second)
		for m.Status().Job != nil && time.Now().Before(deadline) {
			time.Sleep(20 * time.Millisecond)
		}
		if l := m.Status().Last; l == nil || !l.OK {
			t.Fatalf("backup %d: %+v", i, l)
		}
		time.Sleep(1100 * time.Millisecond) // archive names have second resolution
	}
	list, err := m.List(ctx)
	if err != nil || len(list) != 2 || !list[0].Encrypted {
		t.Fatalf("list after retention = %+v, %v", list, err)
	}
	runs, _ := st.BackupRuns(ctx, 10)
	if len(runs) != 3 || runs[0].Status != "success" || runs[0].Size == 0 {
		t.Errorf("runs = %+v", runs)
	}
	if err := m.StartRestore(ctx, list[0].Name, "falsch", ""); !errors.Is(err, ErrPassword) {
		t.Errorf("restore with wrong password: %v", err)
	}
	restarted := make(chan struct{})
	m.Restart = func() { close(restarted) }
	if err := m.StartRestore(ctx, list[0].Name, "pw", ""); err != nil {
		t.Fatal(err)
	}
	select {
	case <-restarted:
	case <-time.After(20 * time.Second):
		t.Fatalf("restore did not finish: %+v", m.Status())
	}
	if _, err := os.Stat(filepath.Join(dir, pendingFile)); err != nil {
		t.Error("restore marker missing")
	}
}
