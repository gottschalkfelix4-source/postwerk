package store

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"postwerk/internal/vault"
)

func rawSetting(t *testing.T, st *Store, key string) string {
	t.Helper()
	var v string
	if err := st.DB.QueryRow(`SELECT value FROM settings WHERE key = ?`, key).Scan(&v); err != nil {
		t.Fatal(err)
	}
	return v
}

func TestSecretsEncryptedAtRest(t *testing.T) {
	t.Setenv("POSTWERK_MASTER_KEY", "")
	t.Setenv("POSTWERK_MASTER_KEY_FILE", "")
	ctx := context.Background()
	dir := t.TempDir()
	st, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, vault.KeyFileName)); err != nil {
		t.Fatal("master.key not generated:", err)
	}
	a := &Account{Email: "u@example.test", PasswordHash: "x", Role: "user"}
	if err := st.CreateAccount(ctx, a); err != nil {
		t.Fatal(err)
	}
	secret := "JBSWY3DPEHPK3PXP"
	if err := st.UpdateAccount(ctx, a.ID, AccountPatch{TOTPSecret: &secret}); err != nil {
		t.Fatal(err)
	}
	if err := st.CreateDomain(ctx, "example.test", "pw1", "-----BEGIN PRIVATE KEY-----\nabc\n-----END PRIVATE KEY-----"); err != nil {
		t.Fatal(err)
	}
	settings, err := st.LoadSettings(ctx, "mail.example.test")
	if err != nil {
		t.Fatal(err)
	}
	cur := settings.Get()
	cur.AI.APIKey = "sk-ai"
	cur.Relay.Password = "relay-pw"
	cur.Backup.Target.WebDAV.Password = "dav-pw"
	if err := settings.Save(ctx, cur); err != nil {
		t.Fatal(err)
	}
	if _, err := st.SRSSecret(ctx); err != nil {
		t.Fatal(err)
	}

	// Raw database content: no plaintext secret.
	var totp, dkim string
	st.DB.QueryRow(`SELECT totp_secret FROM accounts WHERE id = ?`, a.ID).Scan(&totp)
	st.DB.QueryRow(`SELECT dkim_private_key FROM domains`).Scan(&dkim)
	raw := rawSetting(t, st, "settings")
	for name, v := range map[string]string{"totp": totp, "dkim": dkim, "srs": rawSetting(t, st, srsSecretKey)} {
		if !vault.IsEncrypted(v) {
			t.Errorf("%s stored in plaintext: %q", name, v)
		}
	}
	for _, plain := range []string{"sk-ai", "relay-pw", "dav-pw"} {
		if strings.Contains(raw, plain) {
			t.Errorf("settings JSON contains %q", plain)
		}
	}
	// Transparent decryption.
	got, _ := st.GetAccount(ctx, a.ID)
	if got.TOTPSecret != secret {
		t.Fatalf("totp = %q", got.TOTPSecret)
	}
	d, _ := st.GetDomain(ctx, "example.test")
	if !strings.Contains(d.DKIMPrivateKey, "BEGIN PRIVATE KEY") {
		t.Fatalf("dkim = %q", d.DKIMPrivateKey)
	}
	if settings.Get().AI.APIKey != "sk-ai" {
		t.Fatal("cache lost plaintext")
	}
	srs1, _ := st.SRSSecret(ctx)
	st.Close()

	st2, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer st2.Close()
	s2, err := st2.LoadSettings(ctx, "mail.example.test")
	if err != nil {
		t.Fatal(err)
	}
	if v := s2.Get(); v.AI.APIKey != "sk-ai" || v.Relay.Password != "relay-pw" || v.Backup.Target.WebDAV.Password != "dav-pw" {
		t.Fatalf("reloaded settings: %+v", v.AI)
	}
	if err := s2.Reload(ctx); err != nil || s2.Get().Relay.Password != "relay-pw" {
		t.Fatalf("reload: %v", err)
	}
	srs2, _ := st2.SRSSecret(ctx)
	if !bytes.Equal(srs1, srs2) || len(srs1) != 32 {
		t.Fatal("srs secret changed")
	}
}

func TestLegacyPlaintextMigrated(t *testing.T) {
	t.Setenv("POSTWERK_MASTER_KEY", "")
	t.Setenv("POSTWERK_MASTER_KEY_FILE", "")
	ctx := context.Background()
	dir := t.TempDir()
	st, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	a := &Account{Email: "u@example.test", PasswordHash: "x", Role: "user"}
	st.CreateAccount(ctx, a)
	// Simulate data written by an older version.
	st.DB.Exec(`UPDATE accounts SET totp_secret = 'PLAINTOTP' WHERE id = ?`, a.ID)
	st.DB.Exec(`INSERT INTO domains(name, dkim_selector, dkim_private_key, created_at) VALUES ('old.test', 's', 'PLAINKEY', 0)`)
	st.DB.Exec(`INSERT OR REPLACE INTO settings(key, value) VALUES ('settings', '{"ai":{"apiKey":"legacy-key"},"dms":{"paperless":{"token":"legacy-token"}}}')`)
	st.DB.Exec(`INSERT OR REPLACE INTO settings(key, value) VALUES (?, ?)`, srsSecretKey, strings.Repeat("ab", 32))
	st.Close()

	st, err = Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	settings, err := st.LoadSettings(ctx, "h")
	if err != nil {
		t.Fatal(err)
	}
	var totp, dkim string
	st.DB.QueryRow(`SELECT totp_secret FROM accounts WHERE id = ?`, a.ID).Scan(&totp)
	st.DB.QueryRow(`SELECT dkim_private_key FROM domains WHERE name = 'old.test'`).Scan(&dkim)
	raw := rawSetting(t, st, "settings")
	if !vault.IsEncrypted(totp) || !vault.IsEncrypted(dkim) || strings.Contains(raw, "legacy-key") || strings.Contains(raw, "legacy-token") ||
		!vault.IsEncrypted(rawSetting(t, st, srsSecretKey)) {
		t.Fatalf("legacy values not migrated: totp=%q dkim=%q settings=%s", totp, dkim, raw)
	}
	if g := settings.Get(); g.AI.APIKey != "legacy-key" || g.DMS.Paperless.Token != "legacy-token" {
		t.Fatal("legacy settings lost")
	}
	if acc, _ := st.GetAccount(ctx, a.ID); acc.TOTPSecret != "PLAINTOTP" {
		t.Fatal("legacy totp lost")
	}
	if srs, _ := st.SRSSecret(ctx); len(srs) != 32 || srs[0] != 0xab {
		t.Fatal("legacy srs lost")
	}
}

func TestWrongMasterKeyRefused(t *testing.T) {
	dir := t.TempDir()
	key := make([]byte, 32)
	rand.Read(key)
	t.Setenv("POSTWERK_MASTER_KEY", base64.StdEncoding.EncodeToString(key))
	st, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, vault.KeyFileName)); err == nil {
		t.Fatal("key file written although the env key is set")
	}
	st.Close()
	rand.Read(key)
	t.Setenv("POSTWERK_MASTER_KEY", base64.StdEncoding.EncodeToString(key))
	if _, err := Open(dir); err == nil || !strings.Contains(err.Error(), "Master-Schlüssel") {
		t.Fatalf("wrong key accepted: %v", err)
	}
}

func TestSecretSettingPaths(t *testing.T) {
	paths := strings.Join(vault.SecretPaths(Settings{}), ",")
	for _, want := range []string{"AI.APIKey", "AI.EmbeddingAPIKey", "DMS.Paperless.Token", "DynDNS.Password", "Relay.Password",
		"Backup.Password", "Backup.Target.WebDAV.Password", "Backup.Target.SMB.Password"} {
		if !strings.Contains(paths, want) {
			t.Errorf("%s not tagged as secret (have %s)", want, paths)
		}
	}
}
