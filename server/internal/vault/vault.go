// Package vault holds the master key that protects secrets at rest (settings
// secrets, TOTP secrets, DKIM keys and, optionally, mail blobs).
//
// Key source, in this order:
//   - POSTWERK_MASTER_KEY: base64-encoded 32 bytes
//   - POSTWERK_MASTER_KEY_FILE: path to a file holding the base64 key
//   - <data dir>/master.key: generated on first start (mode 0600)
//
// Encrypted strings look like "enc:v1:<base64(nonce|ciphertext)>" and use
// AES-256-GCM. Values without the prefix are treated as legacy plaintext.
package vault

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/hkdf"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// Prefix marks an encrypted string value.
const Prefix = "enc:v1:"

// KeyFileName is the name of the generated key file inside the data dir.
const KeyFileName = "master.key"

// Key sources.
const (
	SourceEnv      = "env"       // POSTWERK_MASTER_KEY
	SourceEnvFile  = "env-file"  // POSTWERK_MASTER_KEY_FILE
	SourceDataFile = "data-file" // <data dir>/master.key
	SourceTest     = "test"
)

// ErrWrongKey is returned when a value cannot be decrypted with the key.
var ErrWrongKey = errors.New("vault: value cannot be decrypted with the master key")

// Key is a loaded master key.
type Key struct {
	raw  []byte
	aead cipher.AEAD
	// Source tells where the key came from (Source* constants).
	Source string
	// File is the key file path (empty for SourceEnv).
	File string
	// InDataDir reports whether the key file lives inside the data
	// directory (and therefore ends up in backup archives).
	InDataDir bool
	// Generated is true when the key file was created by this Load.
	Generated bool
}

// New wraps 32 raw key bytes.
func New(raw []byte) (*Key, error) {
	if len(raw) != 32 {
		return nil, fmt.Errorf("vault: master key must be 32 bytes, got %d", len(raw))
	}
	block, err := aes.NewCipher(raw)
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	return &Key{raw: append([]byte(nil), raw...), aead: aead, Source: SourceTest}, nil
}

// Generate returns a new random key (not persisted).
func Generate() (*Key, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return nil, err
	}
	return New(b)
}

// Load resolves the master key for dataDir (see package doc).
func Load(dataDir string) (*Key, error) {
	if v := strings.TrimSpace(os.Getenv("POSTWERK_MASTER_KEY")); v != "" {
		raw, err := decodeKey(v)
		if err != nil {
			return nil, fmt.Errorf("POSTWERK_MASTER_KEY: %w", err)
		}
		k, err := New(raw)
		if err != nil {
			return nil, err
		}
		k.Source = SourceEnv
		return k, nil
	}
	if p := strings.TrimSpace(os.Getenv("POSTWERK_MASTER_KEY_FILE")); p != "" {
		k, err := readKeyFile(p)
		if err != nil {
			return nil, fmt.Errorf("POSTWERK_MASTER_KEY_FILE: %w", err)
		}
		k.Source = SourceEnvFile
		k.File = p
		k.InDataDir = within(dataDir, p)
		return k, nil
	}
	p := filepath.Join(dataDir, KeyFileName)
	k, err := readKeyFile(p)
	if errors.Is(err, os.ErrNotExist) {
		k, err = createKeyFile(p)
		if err == nil {
			k.Generated = true
		}
	}
	if err != nil {
		return nil, fmt.Errorf("%s: %w", p, err)
	}
	k.Source = SourceDataFile
	k.File = p
	k.InDataDir = true
	return k, nil
}

func within(dir, p string) bool {
	d, err1 := filepath.Abs(dir)
	f, err2 := filepath.Abs(p)
	if err1 != nil || err2 != nil {
		return false
	}
	rel, err := filepath.Rel(d, f)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

func decodeKey(v string) ([]byte, error) {
	v = strings.TrimSpace(v)
	for _, enc := range []*base64.Encoding{base64.StdEncoding, base64.RawStdEncoding, base64.URLEncoding, base64.RawURLEncoding} {
		if b, err := enc.DecodeString(v); err == nil {
			if len(b) != 32 {
				return nil, fmt.Errorf("key must decode to 32 bytes, got %d", len(b))
			}
			return b, nil
		}
	}
	return nil, errors.New("key is not valid base64 (generate one with: openssl rand -base64 32)")
}

func readKeyFile(p string) (*Key, error) {
	b, err := os.ReadFile(p)
	if err != nil {
		return nil, err
	}
	var raw []byte
	if len(b) == 32 {
		raw = b // raw binary key
	} else if raw, err = decodeKey(string(b)); err != nil {
		return nil, err
	}
	return New(raw)
}

func createKeyFile(p string) (*Key, error) {
	k, err := Generate()
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o750); err != nil {
		return nil, err
	}
	// O_EXCL: never overwrite a key another process just wrote.
	f, err := os.OpenFile(p, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		if errors.Is(err, os.ErrExist) {
			return readKeyFile(p)
		}
		return nil, err
	}
	if _, err := f.WriteString(base64.StdEncoding.EncodeToString(k.raw) + "\n"); err != nil {
		f.Close()
		_ = os.Remove(p)
		return nil, err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return nil, err
	}
	return k, f.Close()
}

// IsEncrypted reports whether s carries the encryption prefix.
func IsEncrypted(s string) bool { return strings.HasPrefix(s, Prefix) }

// Seal encrypts b with AES-256-GCM (random nonce, prepended).
func (k *Key) Seal(b, aad []byte) []byte {
	nonce := make([]byte, k.aead.NonceSize(), k.aead.NonceSize()+len(b)+k.aead.Overhead())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		panic("vault: no randomness: " + err.Error())
	}
	return k.aead.Seal(nonce, nonce, b, aad)
}

// Open reverses Seal.
func (k *Key) Open(b, aad []byte) ([]byte, error) {
	n := k.aead.NonceSize()
	if len(b) < n+k.aead.Overhead() {
		return nil, ErrWrongKey
	}
	out, err := k.aead.Open(nil, b[:n], b[n:], aad)
	if err != nil {
		return nil, ErrWrongKey
	}
	return out, nil
}

// Encrypt returns the "enc:v1:" form of s. Empty strings and values that are
// already encrypted are returned unchanged.
func (k *Key) Encrypt(s string) string {
	if s == "" || IsEncrypted(s) {
		return s
	}
	return Prefix + base64.StdEncoding.EncodeToString(k.Seal([]byte(s), []byte(Prefix)))
}

// Decrypt returns the plaintext of an "enc:v1:" value. Values without the
// prefix are legacy plaintext and returned unchanged.
func (k *Key) Decrypt(s string) (string, error) {
	if !IsEncrypted(s) {
		return s, nil
	}
	b, err := base64.StdEncoding.DecodeString(s[len(Prefix):])
	if err != nil {
		return "", ErrWrongKey
	}
	out, err := k.Open(b, []byte(Prefix))
	if err != nil {
		return "", err
	}
	return string(out), nil
}

// Subkey derives an independent 32-byte key for a purpose (HKDF-SHA256).
func (k *Key) Subkey(purpose string) []byte {
	out, err := hkdf.Key(sha256.New, k.raw, nil, "postwerk "+purpose, 32)
	if err != nil {
		panic("vault: hkdf: " + err.Error())
	}
	return out
}

// Fingerprint is a short, non-secret identifier of the key (for display).
func (k *Key) Fingerprint() string {
	sum := sha256.Sum256(k.Subkey("fingerprint"))
	return fmt.Sprintf("%X", sum[:6])
}
