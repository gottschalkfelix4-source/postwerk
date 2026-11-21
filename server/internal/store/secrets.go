package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"postwerk/internal/vault"
)

// vaultCheckKey stores an encrypted canary that detects a wrong master key
// before any secret is used.
const vaultCheckKey = "vault_check"

const vaultCanary = "postwerk-master-key-check"

// blobEncryptionFlag enables encryption of new blobs with the master key.
const blobEncryptionFlag = "blob_encryption"

// initSecrets verifies the master key against the database, encrypts
// legacy plaintext secrets (TOTP secrets, DKIM keys, SRS secret) and
// configures blob encryption. Settings secrets are migrated by LoadSettings.
func (s *Store) initSecrets(ctx context.Context) error {
	var check string
	err := s.DB.QueryRowContext(ctx, `SELECT value FROM settings WHERE key = ?`, vaultCheckKey).Scan(&check)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		if _, err := s.DB.ExecContext(ctx, `INSERT OR IGNORE INTO settings(key, value) VALUES (?, ?)`, vaultCheckKey, s.Vault.Encrypt(vaultCanary)); err != nil {
			return err
		}
	case err != nil:
		return err
	default:
		if v, err := s.Vault.Decrypt(check); err != nil || v != vaultCanary {
			return fmt.Errorf("der Master-Schlüssel (%s) passt nicht zu dieser Datenbank – "+
				"denselben POSTWERK_MASTER_KEY bzw. dieselbe master.key wie beim Anlegen/Sichern verwenden", describeKeySource(s.Vault))
		}
	}
	if err := s.encryptColumn(ctx, "accounts", "id", "totp_secret"); err != nil {
		return fmt.Errorf("encrypt totp secrets: %w", err)
	}
	if err := s.encryptColumn(ctx, "domains", "name", "dkim_private_key"); err != nil {
		return fmt.Errorf("encrypt dkim keys: %w", err)
	}
	if err := s.encryptColumn(ctx, "dkim_keys", "id", "private_key"); err != nil {
		return err
	}
	if err := s.encryptSetting(ctx, srsSecretKey); err != nil {
		return err
	}
	s.Blobs.setKey(s.Vault.Subkey("blobs"))
	s.Blobs.SetEncrypt(s.Flag(ctx, blobEncryptionFlag))
	return nil
}

func describeKeySource(k *vault.Key) string {
	switch k.Source {
	case vault.SourceEnv:
		return "aus POSTWERK_MASTER_KEY"
	case vault.SourceEnvFile:
		return "aus POSTWERK_MASTER_KEY_FILE " + k.File
	default:
		return "aus " + k.File
	}
}

// encryptColumn encrypts every non-empty plaintext value of a text column.
func (s *Store) encryptColumn(ctx context.Context, table, keyCol, col string) error {
	rows, err := s.DB.QueryContext(ctx, `SELECT `+keyCol+`, `+col+` FROM `+table+` WHERE `+col+` != '' AND `+col+` NOT LIKE 'enc:v1:%'`)
	if err != nil {
		return err
	}
	type pair struct {
		key any
		val string
	}
	var todo []pair
	for rows.Next() {
		var p pair
		if err := rows.Scan(&p.key, &p.val); err != nil {
			rows.Close()
			return err
		}
		todo = append(todo, p)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	for _, p := range todo {
		if _, err := s.DB.ExecContext(ctx, `UPDATE `+table+` SET `+col+` = ? WHERE `+keyCol+` = ?`, s.Vault.Encrypt(p.val), p.key); err != nil {
			return err
		}
	}
	return nil
}

// encryptSetting encrypts a plaintext value in the settings table.
func (s *Store) encryptSetting(ctx context.Context, key string) error {
	var v string
	err := s.DB.QueryRowContext(ctx, `SELECT value FROM settings WHERE key = ?`, key).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) || v == "" || vault.IsEncrypted(v) {
		return nil
	}
	if err != nil {
		return err
	}
	_, err = s.DB.ExecContext(ctx, `UPDATE settings SET value = ? WHERE key = ?`, s.Vault.Encrypt(v), key)
	return err
}

// openAccount decrypts the secrets of a scanned account.
func (s *Store) openAccount(a *Account) error {
	v, err := s.Vault.Decrypt(a.TOTPSecret)
	if err != nil {
		return fmt.Errorf("account %d: totp secret: %w", a.ID, err)
	}
	a.TOTPSecret = v
	return nil
}

// BlobEncryption reports whether new blobs are encrypted on disk.
func (s *Store) BlobEncryption() bool { return s.Blobs.Encrypting() }

// SetBlobEncryption switches encryption of new blobs. Existing blobs keep
// their form until MigrateBlobs converts them.
func (s *Store) SetBlobEncryption(ctx context.Context, on bool) error {
	if err := s.SetFlag(ctx, blobEncryptionFlag, on); err != nil {
		return err
	}
	s.Blobs.SetEncrypt(on)
	return nil
}
