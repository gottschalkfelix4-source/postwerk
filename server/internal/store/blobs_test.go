package store

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"os"
	"testing"
)

func newBlobTestStore(t *testing.T) *BlobStore {
	t.Helper()
	b, err := NewBlobStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	key := make([]byte, 32)
	rand.Read(key)
	b.setKey(key)
	return b
}

func TestBlobEncryptionRoundtrip(t *testing.T) {
	b := newBlobTestStore(t)
	b.SetEncrypt(true)
	big := make([]byte, 3*blobChunkSize+123)
	rand.Read(big)
	for _, data := range [][]byte{{}, []byte("Subject: hi\r\n\r\nhello"), big, make([]byte, blobChunkSize)} {
		hash, err := b.Put(data)
		if err != nil {
			t.Fatal(err)
		}
		sum := sha256.Sum256(data)
		if hash != hex.EncodeToString(sum[:]) {
			t.Fatal("hash is not the plaintext hash")
		}
		disk, _ := os.ReadFile(b.path(hash))
		if !isSealedBlob(disk) || (len(data) > 10 && bytes.Contains(disk, data[:10])) {
			t.Fatal("blob not encrypted on disk")
		}
		got, err := b.Get(hash)
		if err != nil || !bytes.Equal(got, data) {
			t.Fatalf("Get: %v (len %d vs %d)", err, len(got), len(data))
		}
		rc, err := b.Open(hash)
		if err != nil {
			t.Fatal(err)
		}
		streamed, err := io.ReadAll(rc)
		rc.Close()
		if err != nil || !bytes.Equal(streamed, data) {
			t.Fatalf("Open: %v", err)
		}
	}
}

func TestBlobTamperDetected(t *testing.T) {
	b := newBlobTestStore(t)
	b.SetEncrypt(true)
	data := make([]byte, 2*blobChunkSize+10)
	rand.Read(data)
	hash, _ := b.Put(data)
	disk, _ := os.ReadFile(b.path(hash))

	flip := append([]byte(nil), disk...)
	flip[len(flip)-5] ^= 1
	os.WriteFile(b.path(hash), flip, 0o640)
	if _, err := b.Get(hash); err == nil {
		t.Fatal("bit flip not detected")
	}
	// Dropping the last chunk must not yield a shorter valid message.
	cut := disk[:len(blobMagic)+blobSaltSize+2*(4+blobChunkSize+16)]
	os.WriteFile(b.path(hash), cut, 0o640)
	if _, err := b.Get(hash); err == nil {
		t.Fatal("truncation not detected")
	}
	// A different key cannot read the blob.
	os.WriteFile(b.path(hash), disk, 0o640)
	other := make([]byte, 32)
	rand.Read(other)
	b.setKey(other)
	if _, err := b.Get(hash); err == nil {
		t.Fatal("wrong key not detected")
	}
}

func TestBlobPlaintextWithMagic(t *testing.T) {
	b := newBlobTestStore(t)
	data := append(append([]byte(nil), blobMagic...), []byte("not really encrypted")...)
	hash, _ := b.Put(data)
	got, err := b.Get(hash)
	if err != nil || !bytes.Equal(got, data) {
		t.Fatalf("plaintext blob with magic prefix: %v", err)
	}
}

func TestBlobMigration(t *testing.T) {
	ctx := context.Background()
	b := newBlobTestStore(t)
	var hashes []string
	var contents [][]byte
	for i := 0; i < 5; i++ {
		data := make([]byte, 1000*(i+1))
		rand.Read(data)
		h, _ := b.Put(data)
		hashes = append(hashes, h)
		contents = append(contents, data)
	}
	st, _ := b.Stats(ctx)
	if st.Total != 5 || st.Encrypted != 0 {
		t.Fatalf("stats before: %+v", st)
	}
	var lastDone, lastTotal int
	if err := b.MigrateBlobs(ctx, true, func(done, total, failed int) { lastDone, lastTotal = done, total }); err != nil {
		t.Fatal(err)
	}
	if lastDone != 5 || lastTotal != 5 {
		t.Fatalf("progress %d/%d", lastDone, lastTotal)
	}
	if st, _ := b.Stats(ctx); st.Encrypted != 5 {
		t.Fatalf("stats after encrypt: %+v", st)
	}
	for i, h := range hashes {
		if got, err := b.Get(h); err != nil || !bytes.Equal(got, contents[i]) {
			t.Fatal("content changed by migration")
		}
	}
	if err := b.MigrateBlobs(ctx, false, nil); err != nil {
		t.Fatal(err)
	}
	if st, _ := b.Stats(ctx); st.Encrypted != 0 {
		t.Fatalf("stats after decrypt: %+v", st)
	}
	disk, _ := os.ReadFile(b.path(hashes[0]))
	if !bytes.Equal(disk, contents[0]) {
		t.Fatal("decrypted blob differs")
	}
}

func TestBlobEncryptionFlagPersisted(t *testing.T) {
	t.Setenv("POSTWERK_MASTER_KEY", "")
	t.Setenv("POSTWERK_MASTER_KEY_FILE", "")
	ctx := context.Background()
	dir := t.TempDir()
	st, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if st.BlobEncryption() {
		t.Fatal("blob encryption must default to off")
	}
	if err := st.SetBlobEncryption(ctx, true); err != nil {
		t.Fatal(err)
	}
	h, _ := st.Blobs.Put([]byte("x"))
	st.Close()
	st, err = Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if !st.BlobEncryption() {
		t.Fatal("flag not persisted")
	}
	if got, err := st.Blobs.Get(h); err != nil || string(got) != "x" {
		t.Fatalf("read after reopen: %v", err)
	}
}
