package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"
)

// chatGPTAuthKey holds the ChatGPT sign-in (OAuth tokens of the admin's
// ChatGPT subscription), encrypted with the master key. It lives outside the
// Settings struct so the tokens never pass through the settings API.
const chatGPTAuthKey = "chatgpt_auth"

// ChatGPTAuth is the token set of the "chatgpt" AI provider.
type ChatGPTAuth struct {
	AccessToken  string `json:"accessToken"`
	RefreshToken string `json:"refreshToken"`
	IDToken      string `json:"idToken"`
	AccountID    string `json:"accountId"`
	Email        string `json:"email"`
	Plan         string `json:"plan"`
	// ExpiresAt is the expiry of the access token (zero if unknown).
	ExpiresAt   time.Time `json:"expiresAt"`
	LastRefresh time.Time `json:"lastRefresh"`
	ConnectedAt time.Time `json:"connectedAt"`
	// Error is set when the tokens could not be refreshed any more; the
	// tokens are cleared then and the admin has to sign in again.
	Error   string    `json:"error,omitempty"`
	ErrorAt time.Time `json:"errorAt,omitempty"`
}

// Connected reports whether usable tokens are stored.
func (a *ChatGPTAuth) Connected() bool {
	return a != nil && a.AccessToken != "" && a.Error == ""
}

// ChatGPTAuth returns the stored ChatGPT sign-in (nil if there is none).
func (s *Store) ChatGPTAuth(ctx context.Context) (*ChatGPTAuth, error) {
	var v string
	err := s.DB.QueryRowContext(ctx, `SELECT value FROM settings WHERE key = ?`, chatGPTAuthKey).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) || (err == nil && v == "") {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	plain, err := s.Vault.Decrypt(v)
	if err != nil {
		return nil, err
	}
	var a ChatGPTAuth
	if err := json.Unmarshal([]byte(plain), &a); err != nil {
		return nil, err
	}
	return &a, nil
}

// SaveChatGPTAuth stores the sign-in encrypted; nil removes it.
func (s *Store) SaveChatGPTAuth(ctx context.Context, a *ChatGPTAuth) error {
	if a == nil {
		_, err := s.DB.ExecContext(ctx, `DELETE FROM settings WHERE key = ?`, chatGPTAuthKey)
		return err
	}
	b, err := json.Marshal(a)
	if err != nil {
		return err
	}
	_, err = s.DB.ExecContext(ctx, `INSERT INTO settings(key, value) VALUES (?, ?)
		ON CONFLICT(key) DO UPDATE SET value = excluded.value`, chatGPTAuthKey, s.Vault.Encrypt(string(b)))
	return err
}

// Store returns the store behind the settings cache.
func (c *SettingsCache) Store() *Store { return c.st }
