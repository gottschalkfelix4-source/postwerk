package backup

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"postwerk/internal/store"
	"postwerk/internal/vault"
)

const (
	testICS   = "BEGIN:VCALENDAR\r\nVERSION:2.0\r\nPRODID:x\r\nBEGIN:VTODO\r\nUID:task-1\r\nDTSTAMP:20260101T000000Z\r\nSUMMARY:Umzug\r\nEND:VTODO\r\nEND:VCALENDAR\r\n"
	testVCard = "BEGIN:VCARD\r\nVERSION:3.0\r\nUID:kontakt-1\r\nFN:Bernd Beispiel\r\nEMAIL:bernd@example.test\r\nEND:VCARD\r\n"
	testPEM   = "-----BEGIN PRIVATE KEY-----\nMIIBVQIBADANBgkqhkiG9w0BAQEFAASCAT8wggE7AgEAAkEAtest\n-----END PRIVATE KEY-----\n"
	testMail  = "From: Bernd <bernd@example.test>\r\nTo: anna@alt.test\r\nSubject: Hallo Umzug\r\n\r\nGeheimer Inhalt.\r\n"
)

func clearKeyEnv(t *testing.T) {
	t.Helper()
	t.Setenv("POSTWERK_MASTER_KEY", "")
	t.Setenv("POSTWERK_MASTER_KEY_FILE", "")
}

func quietLog() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

// populate fills a store with one of everything a server move must keep.
func populate(t *testing.T, st *store.Store, dir string) {
	t.Helper()
	ctx := context.Background()
	settings, err := st.LoadSettings(ctx, "mail.alt.test")
	if err != nil {
		t.Fatal(err)
	}
	cfg := settings.Get()
	cfg.Server.Hostname = "mail.alt.test"
	cfg.MailAuth.SkipReportDelivery = true
	cfg.Backup.Encrypt = true
	cfg.Backup.Password = "ein-langes-backup-passwort"
	if err := settings.Save(ctx, cfg); err != nil {
		t.Fatal(err)
	}
	if err := st.SetBlobEncryption(ctx, true); err != nil {
		t.Fatal(err)
	}
	if err := st.CreateDomain(ctx, "alt.test", "pw202611", testPEM); err != nil {
		t.Fatal(err)
	}
	a := &store.Account{Email: "anna@alt.test", Name: "Anna", PasswordHash: "hash", Role: "admin"}
	if err := st.CreateAccount(ctx, a); err != nil {
		t.Fatal(err)
	}
	secret, on := "JBSWY3DPEHPK3PXP", true
	if err := st.UpdateAccount(ctx, a.ID, store.AccountPatch{TOTPSecret: &secret, TOTPEnabled: &on}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.CreateAlias(ctx, "info@alt.test", []string{"anna@alt.test"}); err != nil {
		t.Fatal(err)
	}
	if err := st.PutSieveScript(ctx, a.ID, "regeln", `require "fileinto"; if header :contains "subject" "Umzug" { fileinto "Projekte"; }`); err != nil {
		t.Fatal(err)
	}
	box, err := st.CreateMailbox(ctx, a.ID, "Projekte")
	if err != nil {
		t.Fatal(err)
	}
	hash, err := st.Blobs.Put([]byte(testMail))
	if err != nil {
		t.Fatal(err)
	}
	m := &store.Message{AccountID: a.ID, MailboxID: box.ID, MessageID: "<1@alt.test>", Subject: "Hallo Umzug",
		From: store.Address{Name: "Bernd", Email: "bernd@example.test"}, ReceivedAt: time.Now().UTC(), Size: int64(len(testMail)), BlobHash: hash}
	if err := st.InsertMessage(ctx, m); err != nil {
		t.Fatal(err)
	}
	for kind, obj := range map[string][2]string{store.KindCalendar: {"task-1.ics", testICS}, store.KindAddressBook: {"kontakt-1.vcf", testVCard}} {
		cols, err := st.ListCollections(ctx, a.ID, kind)
		if err != nil || len(cols) == 0 {
			t.Fatalf("%s collections: %v", kind, err)
		}
		if _, _, err := st.PutObject(ctx, cols[0], obj[0], []byte(obj[1]), store.PutOptions{IfNoneMatch: true}); err != nil {
			t.Fatalf("put %s: %v", obj[0], err)
		}
	}
	// A file outside the database (certificates live like this).
	os.MkdirAll(filepath.Join(dir, "acme"), 0o750)
	os.WriteFile(filepath.Join(dir, "acme", "mail.alt.test.crt"), []byte("zertifikat"), 0o600)
}

// dumpDB returns every row of every table except the backup history.
func dumpDB(t *testing.T, dbPath string) map[string][]string {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+filepath.ToSlash(dbPath)+"?mode=ro")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	rows, err := db.Query(`SELECT name FROM sqlite_master WHERE type = 'table' AND name NOT IN ('backup_runs', 'sqlite_sequence')`)
	if err != nil {
		t.Fatal(err)
	}
	var tables []string
	for rows.Next() {
		var n string
		rows.Scan(&n)
		tables = append(tables, n)
	}
	rows.Close()
	out := map[string][]string{}
	for _, tbl := range tables {
		r, err := db.Query(`SELECT * FROM "` + tbl + `"`)
		if err != nil {
			continue // virtual tables of extensions that are not loaded
		}
		cols, _ := r.Columns()
		var lines []string
		for r.Next() {
			vals := make([]any, len(cols))
			ptrs := make([]any, len(cols))
			for i := range vals {
				ptrs[i] = &vals[i]
			}
			if err := r.Scan(ptrs...); err != nil {
				t.Fatal(err)
			}
			lines = append(lines, fmt.Sprintf("%v", vals))
		}
		r.Close()
		sort.Strings(lines)
		out[tbl] = lines
	}
	return out
}

// dataFiles lists the files of a data directory that a restore must bring
// back, with their content.
func dataFiles(t *testing.T, dir string) map[string]string {
	t.Helper()
	out := map[string]string{}
	filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil || p == dir {
			return nil
		}
		rel, _ := filepath.Rel(dir, p)
		rel = filepath.ToSlash(rel)
		top := strings.SplitN(rel, "/", 2)[0]
		if skipTop(top) || keepOnRestore(top) {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if !d.IsDir() {
			b, _ := os.ReadFile(p)
			out[rel] = string(b)
		}
		return nil
	})
	return out
}

// TestRestoreRoundTrip backs up a populated server, restores the archive on
// a freshly started one (as the setup assistant does) and checks that the
// result is identical: database, mail blobs, files and master key.
func TestRestoreRoundTrip(t *testing.T) {
	clearKeyEnv(t)
	ctx := context.Background()

	// Old server.
	dirA := t.TempDir()
	stA, err := store.Open(dirA)
	if err != nil {
		t.Fatal(err)
	}
	populate(t, stA, dirA)
	settingsA, _ := stA.LoadSettings(ctx, "")
	mA := &Manager{Store: stA, Settings: settingsA, DataDir: dirA, Version: "test", Log: quietLog()}
	if err := mA.Start("manual"); err != nil {
		t.Fatal(err)
	}
	waitIdle(t, mA)
	if l := mA.Status().Last; l == nil || !l.OK {
		t.Fatalf("backup: %+v", mA.Status().Last)
	}
	list, err := mA.List(ctx)
	if err != nil || len(list) != 1 || !list[0].Encrypted {
		t.Fatalf("archives = %+v, %v", list, err)
	}
	archive := filepath.Join(dirA, "backups", list[0].Name)
	fingerprint := stA.Vault.Fingerprint()
	stA.Close()
	wantDB := dumpDB(t, filepath.Join(dirA, dbName))
	for _, tbl := range []string{"accounts", "domains", "dkim_keys", "aliases", "messages", "mailboxes", "settings", "sieve_scripts", "dav_collections", "calendar_objects", "address_objects"} {
		if len(wantDB[tbl]) == 0 {
			t.Fatalf("test setup: table %s empty or missing", tbl)
		}
	}
	wantFiles := dataFiles(t, dirA)
	if _, ok := wantFiles[vault.KeyFileName]; !ok {
		t.Fatal("old server has no master.key")
	}

	// The manifest describes the archive.
	f, _ := os.Open(archive)
	plain, _, err := Open(f, "ein-langes-backup-passwort")
	if err != nil {
		t.Fatal(err)
	}
	gz, _ := gzip.NewReader(plain)
	h, err := tar.NewReader(gz).Next()
	if err != nil || h.Name != manifestName {
		t.Fatalf("first entry = %v, %v", h, err)
	}
	f.Close()

	// New server: fresh data directory with its own generated key.
	dirB := t.TempDir()
	stB, err := store.Open(dirB)
	if err != nil {
		t.Fatal(err)
	}
	if stB.Vault.Fingerprint() == fingerprint {
		t.Fatal("test setup: both servers have the same key")
	}
	settingsB, _ := stB.LoadSettings(ctx, "neu.test")
	restarted := make(chan struct{})
	mB := &Manager{Store: stB, Settings: settingsB, DataDir: dirB, Version: "test", Log: quietLog(), Restart: func() { close(restarted) }}

	// Wrong password: rejected, nothing staged.
	f, _ = os.Open(archive)
	if _, err := mB.RestoreStream(ctx, f, -1, list[0].Name, "falsch", ""); !errors.Is(err, ErrPassword) {
		t.Fatalf("wrong password: %v", err)
	}
	f.Close()
	assertNothingPending(t, dirB)

	f, _ = os.Open(archive)
	man, err := mB.RestoreStream(ctx, f, -1, list[0].Name, "ein-langes-backup-passwort", "")
	f.Close()
	if err != nil {
		t.Fatal(err)
	}
	if man.Format != ArchiveFormat || man.Accounts != 1 || man.Messages != 1 || man.Domains != 1 || !man.KeyInArchive ||
		man.KeyFingerprint != fingerprint || man.Schema != store.SchemaVersion() || man.Hostname != "mail.alt.test" {
		t.Errorf("manifest = %+v", man)
	}
	select {
	case <-restarted:
	case <-time.After(10 * time.Second):
		t.Fatal("no restart after the restore was staged")
	}
	stB.Close()

	// Next start.
	applied, err := ApplyPending(dirB)
	if err != nil || applied == nil {
		t.Fatalf("apply = %+v, %v", applied, err)
	}
	st, err := store.Open(dirB)
	if err != nil {
		t.Fatalf("restored data does not open: %v", err)
	}
	defer st.Close()

	if got := st.Vault.Fingerprint(); got != fingerprint {
		t.Errorf("master key fingerprint = %s, want %s", got, fingerprint)
	}
	if !st.BlobEncryption() {
		t.Error("blob encryption setting lost")
	}
	set, err := st.LoadSettings(ctx, "neu.test")
	if err != nil {
		t.Fatal(err)
	}
	if c := set.Get(); c.Server.Hostname != "mail.alt.test" || !c.MailAuth.SkipReportDelivery || c.Backup.Password != "ein-langes-backup-passwort" {
		t.Errorf("settings after restore: hostname=%q skipReportDelivery=%v backupPassword=%q", c.Server.Hostname, c.MailAuth.SkipReportDelivery, c.Backup.Password)
	}
	a, err := st.GetAccountByEmail(ctx, "anna@alt.test")
	if err != nil || a.TOTPSecret != "JBSWY3DPEHPK3PXP" || !a.TOTPEnabled {
		t.Fatalf("account after restore: %+v, %v", a, err)
	}
	d, err := st.GetDomain(ctx, "alt.test")
	if err != nil || d.DKIMPrivateKey != testPEM {
		t.Errorf("DKIM key after restore: %v", err)
	}
	if al, err := st.GetAlias(ctx, "info@alt.test"); err != nil || len(al.Targets) != 1 {
		t.Errorf("alias after restore: %+v, %v", al, err)
	}
	if sc, err := st.GetSieveScript(ctx, a.ID, "regeln"); err != nil || !strings.Contains(sc.Content, "Projekte") {
		t.Errorf("sieve script after restore: %+v, %v", sc, err)
	}
	msgs, total, err := st.ListMessages(ctx, store.MessageQuery{AccountID: a.ID, Limit: 10})
	if err != nil || total != 1 || len(msgs) != 1 {
		t.Fatalf("messages after restore: %d, %v", total, err)
	}
	full, err := st.GetMessage(ctx, a.ID, msgs[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	body, err := st.Blobs.Get(full.BlobHash)
	if err != nil || string(body) != testMail {
		t.Errorf("mail body after restore = %q, %v", body, err)
	}

	// Everything else, byte for byte.
	st.Close()
	gotDB := dumpDB(t, filepath.Join(dirB, dbName))
	for tbl, rows := range wantDB {
		if strings.Join(gotDB[tbl], "\n") != strings.Join(rows, "\n") {
			t.Errorf("table %s differs after restore:\n got %v\nwant %v", tbl, gotDB[tbl], rows)
		}
	}
	gotFiles := dataFiles(t, dirB)
	for p, content := range wantFiles {
		if gotFiles[p] != content {
			t.Errorf("file %s differs or is missing after restore", p)
		}
	}
	if _, err := os.Stat(StagingDir(dirB)); err == nil {
		t.Error("staging folder left behind")
	}
}

func waitIdle(t *testing.T, m *Manager) {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for m.Status().Job != nil {
		if time.Now().After(deadline) {
			t.Fatalf("job did not finish: %+v", m.Status().Job)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func assertNothingPending(t *testing.T, dir string) {
	t.Helper()
	if _, err := os.Stat(filepath.Join(dir, pendingFile)); err == nil {
		t.Error("restore marked as pending")
	}
	if _, err := os.Stat(StagingDir(dir)); err == nil {
		t.Error("staging folder left behind")
	}
}

// archiveOf creates an unencrypted archive of a fresh store with one account.
func archiveOf(t *testing.T, mutate func(st *store.Store)) []byte {
	t.Helper()
	st, dir := newStore(t)
	if err := st.CreateAccount(context.Background(), &store.Account{Email: "x@alt.test", PasswordHash: "x", Role: "admin"}); err != nil {
		t.Fatal(err)
	}
	if mutate != nil {
		mutate(st)
	}
	var buf bytes.Buffer
	if _, err := Create(context.Background(), &buf, Source{DataDir: dir, DB: st.DB, Exclude: KeyExcludes(st, dir)}, ""); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// rewriteManifest re-packs an archive with a changed manifest.
func rewriteManifest(t *testing.T, archive []byte, fn func(m map[string]any)) []byte {
	t.Helper()
	gz, err := gzip.NewReader(bytes.NewReader(archive))
	if err != nil {
		t.Fatal(err)
	}
	tr := tar.NewReader(gz)
	var out bytes.Buffer
	gw := gzip.NewWriter(&out)
	tw := tar.NewWriter(gw)
	for {
		h, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		b, _ := io.ReadAll(tr)
		if h.Name == manifestName {
			var m map[string]any
			json.Unmarshal(b, &m)
			fn(m)
			b, _ = json.Marshal(m)
			h.Size = int64(len(b))
		}
		tw.WriteHeader(h)
		tw.Write(b)
	}
	tw.Close()
	gw.Close()
	return out.Bytes()
}

// TestRestoreRejectsBadArchives checks that broken or foreign archives are
// refused before anything is marked for the next start and that the live
// data stays untouched.
func TestRestoreRejectsBadArchives(t *testing.T) {
	clearKeyEnv(t)
	good := archiveOf(t, nil)
	newer := archiveOf(t, func(st *store.Store) {
		st.DB.Exec(`INSERT INTO schema_version(version) VALUES (?)`, store.SchemaVersion()+5)
	})
	truncated := good[:len(good)/2]
	flipped := append([]byte(nil), good...)
	flipped[len(flipped)/2] ^= 0xff

	// An archive whose first entry is not the manifest.
	var noManifest bytes.Buffer
	gw := gzip.NewWriter(&noManifest)
	tw := tar.NewWriter(gw)
	tw.WriteHeader(&tar.Header{Name: "data/postwerk.db", Mode: 0o600, Size: 3, Typeflag: tar.TypeReg})
	tw.Write([]byte("abc"))
	tw.Close()
	gw.Close()

	cases := []struct {
		name string
		data []byte
		want error
	}{
		{"newer format", rewriteManifest(t, good, func(m map[string]any) { m["format"] = ArchiveFormat + 1 }), ErrFormat},
		{"format zero", rewriteManifest(t, good, func(m map[string]any) { m["format"] = 0 }), ErrFormat},
		{"newer schema", newer, ErrFormat},
		{"truncated", truncated, nil},
		{"corrupted", flipped, nil},
		{"no manifest", noManifest.Bytes(), nil},
		{"not an archive", []byte("das ist keine Sicherung"), nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			st, dir := newStore(t)
			acct := &store.Account{Email: "live@neu.test", PasswordHash: "x", Role: "admin"}
			if err := st.CreateAccount(context.Background(), acct); err != nil {
				t.Fatal(err)
			}
			_, err := PrepareRestore(context.Background(), bytes.NewReader(c.data), dir, RestoreOptions{Name: "x.tar.gz"})
			if err == nil {
				t.Fatal("bad archive accepted")
			}
			if c.want != nil && !errors.Is(err, c.want) {
				t.Errorf("err = %v, want %v", err, c.want)
			}
			assertNothingPending(t, dir)
			if a, err := ApplyPending(dir); a != nil || err != nil {
				t.Errorf("apply after rejected restore = %+v, %v", a, err)
			}
			if _, err := st.GetAccountByEmail(context.Background(), "live@neu.test"); err != nil {
				t.Errorf("live data changed: %v", err)
			}
		})
	}
}

// TestRestoreNeedsMasterKey covers an old server that read its key from
// POSTWERK_MASTER_KEY: the archive has no key, the restore stops, keeps the
// unpacked archive and continues once the right key is given.
func TestRestoreNeedsMasterKey(t *testing.T) {
	ctx := context.Background()
	raw := make([]byte, 32)
	rand.Read(raw)
	oldKey := base64.StdEncoding.EncodeToString(raw)

	t.Setenv("POSTWERK_MASTER_KEY_FILE", "")
	t.Setenv("POSTWERK_MASTER_KEY", oldKey)
	dirA := t.TempDir()
	stA, err := store.Open(dirA)
	if err != nil {
		t.Fatal(err)
	}
	if err := stA.CreateDomain(ctx, "alt.test", "s1", testPEM); err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	if _, err := Create(ctx, &buf, Source{DataDir: dirA, DB: stA.DB, Exclude: KeyExcludes(stA, dirA),
		Manifest: Manifest{KeyFingerprint: stA.Vault.Fingerprint()}}, ""); err != nil {
		t.Fatal(err)
	}
	fingerprint := stA.Vault.Fingerprint()
	stA.Close()

	// New server without the variable.
	t.Setenv("POSTWERK_MASTER_KEY", "")
	stB, dirB := newStore(t)
	settingsB, _ := stB.LoadSettings(ctx, "neu.test")
	mB := &Manager{Store: stB, Settings: settingsB, DataDir: dirB, Log: quietLog()}
	man, err := mB.RestoreStream(ctx, bytes.NewReader(buf.Bytes()), -1, "alt.tar.gz", "", "")
	if !errors.Is(err, ErrKeyMissing) || man == nil || man.KeyInArchive || man.KeyFingerprint != fingerprint {
		t.Fatalf("restore without key = %+v, %v", man, err)
	}
	if l := mB.Status().Last; l == nil || l.Code != "master_key_required" || l.KeyFingerprint != fingerprint {
		t.Errorf("status = %+v", l)
	}
	if _, err := os.Stat(filepath.Join(dirB, pendingFile)); err == nil {
		t.Fatal("restore pending without key")
	}

	other := make([]byte, 32)
	rand.Read(other)
	if _, err := mB.ResumeRestore(ctx, base64.StdEncoding.EncodeToString(other)); !errors.Is(err, ErrKeyMismatch) {
		t.Fatalf("wrong key: %v", err)
	}
	if _, err := mB.ResumeRestore(ctx, "kein schlüssel"); !errors.Is(err, ErrKeyMismatch) {
		t.Fatalf("invalid key: %v", err)
	}
	if _, err := mB.ResumeRestore(ctx, oldKey); err != nil {
		t.Fatalf("right key: %v", err)
	}
	stB.Close()
	if _, err := ApplyPending(dirB); err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(dirB)
	if err != nil {
		t.Fatalf("restored data does not open: %v", err)
	}
	defer st.Close()
	if st.Vault.Source != vault.SourceDataFile || st.Vault.Fingerprint() != fingerprint {
		t.Errorf("key after restore: %s %s", st.Vault.Source, st.Vault.Fingerprint())
	}
	if d, err := st.GetDomain(ctx, "alt.test"); err != nil || d.DKIMPrivateKey != testPEM {
		t.Errorf("DKIM key after restore: %v", err)
	}
}

// TestRestoreKeyMismatchWithEnv: the new server sets POSTWERK_MASTER_KEY to
// a different key than the one in the archive; the restore is refused
// instead of producing a server that cannot start.
func TestRestoreKeyMismatchWithEnv(t *testing.T) {
	clearKeyEnv(t)
	archive := archiveOf(t, nil)
	raw := make([]byte, 32)
	rand.Read(raw)
	t.Setenv("POSTWERK_MASTER_KEY", base64.StdEncoding.EncodeToString(raw))
	_, dir := newStore(t)
	_, err := PrepareRestore(context.Background(), bytes.NewReader(archive), dir, RestoreOptions{Name: "x.tar.gz"})
	if !errors.Is(err, ErrKeyMismatch) {
		t.Fatalf("err = %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, pendingFile)); err == nil {
		t.Error("restore pending despite key mismatch")
	}
	// The staged archive is dropped on the next start.
	if a, err := ApplyPending(dir); a != nil || err != nil {
		t.Fatalf("apply = %+v, %v", a, err)
	}
	if _, err := os.Stat(StagingDir(dir)); err == nil {
		t.Error("staging folder survives a restart")
	}
}

// TestRevertWhenRestoredDataFails: if the restored data cannot be opened on
// start, Revert brings the previous data back and records the failure.
func TestRevertWhenRestoredDataFails(t *testing.T) {
	clearKeyEnv(t)
	ctx := context.Background()
	st, dir := newStore(t)
	if err := st.CreateAccount(ctx, &store.Account{Email: "live@neu.test", PasswordHash: "x", Role: "admin"}); err != nil {
		t.Fatal(err)
	}
	st.Close()
	// A staged database that does not match the master key, as if it had
	// been staged by hand.
	staged := filepath.Join(StagingDir(dir), "data")
	os.MkdirAll(staged, 0o750)
	other, _ := vault.Generate()
	db, _ := sql.Open("sqlite", "file:"+filepath.ToSlash(filepath.Join(staged, dbName)))
	db.Exec(`CREATE TABLE settings (key TEXT PRIMARY KEY, value TEXT NOT NULL)`)
	db.Exec(`INSERT INTO settings VALUES ('vault_check', ?)`, other.Encrypt("postwerk-master-key-check"))
	db.Close()
	if err := markPending(dir, "kaputt.tar.gz"); err != nil {
		t.Fatal(err)
	}
	applied, err := ApplyPending(dir)
	if err != nil || applied == nil {
		t.Fatalf("apply = %+v, %v", applied, err)
	}
	_, openErr := store.Open(dir)
	if openErr == nil {
		t.Fatal("broken restored data opened")
	}
	if err := Revert(dir, applied, openErr); err != nil {
		t.Fatal(err)
	}
	st2, err := store.Open(dir)
	if err != nil {
		t.Fatalf("previous data does not open after revert: %v", err)
	}
	defer st2.Close()
	if _, err := st2.GetAccountByEmail(ctx, "live@neu.test"); err != nil {
		t.Errorf("previous account missing after revert: %v", err)
	}
	if f := LastFailure(dir); f == nil || f.Archive != "kaputt.tar.gz" || f.Error == "" {
		t.Errorf("failure = %+v", f)
	}
}
