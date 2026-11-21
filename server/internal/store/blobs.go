package store

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
)

// BlobStore keeps raw RFC 5322 messages, addressed by the SHA-256 hash of
// their plaintext. With encryption enabled, new blobs are written encrypted
// with a key derived from the master key (see blobcrypt.go); reads handle
// both forms transparently.
type BlobStore struct {
	dir     string
	key     []byte
	encrypt atomic.Bool
	// migrateMu serializes MigrateBlobs runs.
	migrateMu sync.Mutex
}

func NewBlobStore(dir string) (*BlobStore, error) {
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return nil, err
	}
	return &BlobStore{dir: dir}, nil
}

func (b *BlobStore) setKey(key []byte) { b.key = key }

// SetEncrypt switches encryption of newly written blobs.
func (b *BlobStore) SetEncrypt(on bool) { b.encrypt.Store(on && b.key != nil) }

// Encrypting reports whether new blobs are written encrypted.
func (b *BlobStore) Encrypting() bool { return b.encrypt.Load() }

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
	stored := data
	if b.Encrypting() {
		var err error
		if stored, err = sealBlob(b.key, data); err != nil {
			return "", err
		}
	}
	if err := writeAtomic(p, stored); err != nil {
		return "", err
	}
	return hash, nil
}

func writeAtomic(p string, data []byte) error {
	tmp := p + ".tmp"
	if err := os.WriteFile(tmp, data, 0o640); err != nil {
		return err
	}
	return os.Rename(tmp, p)
}

func (b *BlobStore) Get(hash string) ([]byte, error) {
	if len(hash) < 3 {
		return nil, fmt.Errorf("invalid blob hash")
	}
	data, err := os.ReadFile(b.path(hash))
	if err != nil || !isSealedBlob(data) {
		return data, err
	}
	return b.unseal(hash, data)
}

// unseal decrypts a blob file. A plaintext blob that merely starts with the
// magic bytes is recognized by its hash.
func (b *BlobStore) unseal(hash string, data []byte) ([]byte, error) {
	if b.key == nil {
		return nil, errors.New("encrypted blob but no key")
	}
	plain, err := openBlob(b.key, bytes.NewReader(data))
	if err == nil {
		return plain, nil
	}
	if sum := sha256.Sum256(data); hex.EncodeToString(sum[:]) == hash {
		return data, nil
	}
	return nil, fmt.Errorf("blob %s: %w", hash, err)
}

func (b *BlobStore) Open(hash string) (io.ReadCloser, error) {
	if len(hash) < 3 {
		return nil, fmt.Errorf("invalid blob hash")
	}
	f, err := os.Open(b.path(hash))
	if err != nil {
		return nil, err
	}
	head := make([]byte, len(blobMagic))
	n, _ := io.ReadFull(f, head)
	if n < len(head) || !isSealedBlob(head) {
		if _, err := f.Seek(0, io.SeekStart); err != nil {
			f.Close()
			return nil, err
		}
		return f, nil
	}
	if b.key == nil {
		f.Close()
		return nil, errors.New("encrypted blob but no key")
	}
	r, err := newBlobReader(b.key, io.MultiReader(bytes.NewReader(head), f))
	if err != nil {
		f.Close()
		return nil, err
	}
	return struct {
		io.Reader
		io.Closer
	}{r, f}, nil
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

// BlobStats counts blob files by form.
type BlobStats struct {
	Total     int   `json:"total"`
	Encrypted int   `json:"encrypted"`
	Bytes     int64 `json:"bytes"`
}

// Stats walks the blob directory and counts encrypted and plaintext blobs.
func (b *BlobStore) Stats(ctx context.Context) (BlobStats, error) {
	var st BlobStats
	err := b.walk(ctx, func(hash, p string, info fs.FileInfo) error {
		st.Total++
		st.Bytes += info.Size()
		if sealed, _ := fileSealed(p); sealed {
			st.Encrypted++
		}
		return nil
	})
	return st, err
}

// MigrateBlobs rewrites every blob into the requested form (encrypt=true:
// encrypted with the master key, false: plaintext). progress is called with
// the number of processed and total blobs. Blobs whose content does not match
// their hash are left untouched and counted as failed.
func (b *BlobStore) MigrateBlobs(ctx context.Context, encrypt bool, progress func(done, total, failed int)) error {
	if encrypt && b.key == nil {
		return errors.New("no blob key")
	}
	if !b.migrateMu.TryLock() {
		return errors.New("blob migration already running")
	}
	defer b.migrateMu.Unlock()
	var hashes []string
	if err := b.walk(ctx, func(hash, p string, info fs.FileInfo) error {
		hashes = append(hashes, hash)
		return nil
	}); err != nil {
		return err
	}
	failed := 0
	for i, hash := range hashes {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := b.convert(hash, encrypt); err != nil && !errors.Is(err, fs.ErrNotExist) {
			failed++
		}
		if progress != nil {
			progress(i+1, len(hashes), failed)
		}
	}
	if failed > 0 {
		return fmt.Errorf("%d Blobs konnten nicht umgewandelt werden", failed)
	}
	return nil
}

func (b *BlobStore) convert(hash string, encrypt bool) error {
	p := b.path(hash)
	sealed, err := fileSealed(p)
	if err != nil {
		return err
	}
	if sealed == encrypt {
		return nil
	}
	plain, err := b.Get(hash)
	if err != nil {
		return err
	}
	if sum := sha256.Sum256(plain); hex.EncodeToString(sum[:]) != hash {
		return fmt.Errorf("blob %s: content does not match hash", hash)
	}
	out := plain
	if encrypt {
		if out, err = sealBlob(b.key, plain); err != nil {
			return err
		}
	}
	return writeAtomic(p, out)
}

func fileSealed(p string) (bool, error) {
	f, err := os.Open(p)
	if err != nil {
		return false, err
	}
	defer f.Close()
	head := make([]byte, len(blobMagic))
	if _, err := io.ReadFull(f, head); err != nil {
		return false, nil
	}
	return isSealedBlob(head), nil
}

// walk calls fn for every blob file (skipping temp files).
func (b *BlobStore) walk(ctx context.Context, fn func(hash, p string, info fs.FileInfo) error) error {
	return filepath.WalkDir(b.dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if d.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(b.dir, p)
		if err != nil {
			return nil
		}
		parts := strings.Split(filepath.ToSlash(rel), "/")
		if len(parts) != 2 || len(parts[0]) != 2 || strings.HasSuffix(parts[1], ".tmp") || !isHex(parts[0]+parts[1]) {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return nil
		}
		return fn(parts[0]+parts[1], p, info)
	})
}
