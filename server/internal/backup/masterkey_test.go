package backup

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"os"
	"path/filepath"
	"testing"

	"postwerk/internal/store"
	"postwerk/internal/vault"
)

func archiveHas(t *testing.T, st *store.Store, dir string, exclude []string, rel string) bool {
	t.Helper()
	var buf bytes.Buffer
	if _, err := Create(context.Background(), &buf, Source{DataDir: dir, DB: st.DB, Exclude: exclude}, ""); err != nil {
		t.Fatal(err)
	}
	staging := filepath.Join(t.TempDir(), "x")
	if _, err := Extract(context.Background(), bytes.NewReader(buf.Bytes()), "", staging); err != nil {
		t.Fatal(err)
	}
	_, err := os.Stat(filepath.Join(staging, "data", filepath.FromSlash(rel)))
	return err == nil
}

func TestMasterKeyInArchive(t *testing.T) {
	t.Setenv("POSTWERK_MASTER_KEY", "")
	t.Setenv("POSTWERK_MASTER_KEY_FILE", "")
	st, dir := newStore(t)
	os.MkdirAll(filepath.Join(dir, "exports"), 0o750)
	os.WriteFile(filepath.Join(dir, "exports", "abc.zip"), []byte("export"), 0o600)
	m := &Manager{Store: st, DataDir: dir}
	if !archiveHas(t, st, dir, KeyExcludes(st, dir), vault.KeyFileName) {
		t.Fatal("master.key from the data dir must be archived")
	}
	if archiveHas(t, st, dir, nil, "exports/abc.zip") {
		t.Fatal("exports must not be archived")
	}
	if info := m.masterKeyInfo(); info.Source != vault.SourceDataFile || !info.InArchive {
		t.Fatalf("info = %+v", info)
	}
}

func TestEnvMasterKeyNotArchived(t *testing.T) {
	key := make([]byte, 32)
	rand.Read(key)
	t.Setenv("POSTWERK_MASTER_KEY", base64.StdEncoding.EncodeToString(key))
	st, dir := newStore(t)
	// A stale key file from before the switch to the environment variable.
	os.WriteFile(filepath.Join(dir, vault.KeyFileName), []byte(base64.StdEncoding.EncodeToString(key)), 0o600)
	m := &Manager{Store: st, DataDir: dir}
	if archiveHas(t, st, dir, KeyExcludes(st, dir), vault.KeyFileName) {
		t.Fatal("stale master.key archived although the key comes from the environment")
	}
	if info := m.masterKeyInfo(); info.Source != vault.SourceEnv || info.InArchive {
		t.Fatalf("info = %+v", info)
	}
}
