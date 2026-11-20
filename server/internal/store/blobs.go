package store

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

// BlobStore keeps raw RFC 5322 messages, addressed by their SHA-256 hash.
type BlobStore struct{ dir string }

func NewBlobStore(dir string) (*BlobStore, error) {
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return nil, err
	}
	return &BlobStore{dir: dir}, nil
}

func (b *BlobStore) path(hash string) string {
	return filepath.Join(b.dir, hash[:2], hash[2:])
}

func (b *BlobStore) Put(data []byte) (string, error) {
	sum := sha256.Sum256(data)
	hash := hex.EncodeToString(sum[:])
	p := b.path(hash)
	if _, err := os.Stat(p); err == nil {
		return hash, nil
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o750); err != nil {
		return "", err
	}
	tmp := p + ".tmp"
	if err := os.WriteFile(tmp, data, 0o640); err != nil {
		return "", err
	}
	if err := os.Rename(tmp, p); err != nil {
		return "", err
	}
	return hash, nil
}

func (b *BlobStore) Get(hash string) ([]byte, error) {
	if len(hash) < 3 {
		return nil, fmt.Errorf("invalid blob hash")
	}
	return os.ReadFile(b.path(hash))
}

func (b *BlobStore) Open(hash string) (io.ReadCloser, error) {
	if len(hash) < 3 {
		return nil, fmt.Errorf("invalid blob hash")
	}
	return os.Open(b.path(hash))
}

func (b *BlobStore) Delete(hash string) error {
	if len(hash) < 3 {
		return nil
	}
	err := os.Remove(b.path(hash))
	if os.IsNotExist(err) {
		return nil
	}
	return err
}
