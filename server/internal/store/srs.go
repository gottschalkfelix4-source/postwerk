package store

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
)

const srsSecretKey = "srs_secret"

// SRSSecret returns the secret for the Sender Rewriting Scheme. It is
// generated once and persisted in the settings table.
func (s *Store) SRSSecret(ctx context.Context) ([]byte, error) {
	var v string
	err := s.DB.QueryRowContext(ctx, `SELECT value FROM settings WHERE key = ?`, srsSecretKey).Scan(&v)
	if err == nil && v != "" {
		return s.decodeSRS(v)
	}
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return nil, err
	}
	// INSERT OR IGNORE keeps a secret written concurrently by someone else.
	if _, err := s.DB.ExecContext(ctx, `INSERT OR IGNORE INTO settings(key, value) VALUES (?, ?)`, srsSecretKey, s.Vault.Encrypt(hex.EncodeToString(b))); err != nil {
		return nil, err
	}
	if err := s.DB.QueryRowContext(ctx, `SELECT value FROM settings WHERE key = ?`, srsSecretKey).Scan(&v); err != nil {
		return nil, err
	}
	if v == "" {
		return nil, errors.New("empty SRS secret")
	}
	return s.decodeSRS(v)
}

// decodeSRS decrypts and hex-decodes a stored SRS secret.
func (s *Store) decodeSRS(v string) ([]byte, error) {
	plain, err := s.Vault.Decrypt(v)
	if err != nil {
		return nil, err
	}
	return hex.DecodeString(plain)
}
