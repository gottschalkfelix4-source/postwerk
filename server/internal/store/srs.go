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
		return hex.DecodeString(v)
	}
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return nil, err
	}
	// INSERT OR IGNORE keeps a secret written concurrently by someone else.
	if _, err := s.DB.ExecContext(ctx, `INSERT OR IGNORE INTO settings(key, value) VALUES (?, ?)`, srsSecretKey, hex.EncodeToString(b)); err != nil {
		return nil, err
	}
	if err := s.DB.QueryRowContext(ctx, `SELECT value FROM settings WHERE key = ?`, srsSecretKey).Scan(&v); err != nil {
		return nil, err
	}
	if v == "" {
		return nil, errors.New("empty SRS secret")
	}
	return hex.DecodeString(v)
}
