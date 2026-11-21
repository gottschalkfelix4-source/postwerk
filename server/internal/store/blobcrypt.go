package store

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/hkdf"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"io"
)

// Encrypted blob format (streaming AES-256-GCM, STREAM construction):
//
//	magic "\x00PWBLOB1" | salt (16 bytes) | chunk*
//	chunk = header uint32 BE (bit 31: last chunk, bits 0-30: ciphertext length) | ciphertext
//
// Each blob gets its own key, HKDF-SHA256(blob key, salt). The nonce is the
// chunk counter plus the last-chunk flag, so chunks cannot be reordered,
// dropped or truncated without detection. Plaintext chunks are 64 KiB.

var blobMagic = []byte("\x00PWBLOB1")

const (
	blobSaltSize  = 16
	blobChunkSize = 64 << 10
	blobLastFlag  = 1 << 31
)

var errBlobCorrupt = errors.New("encrypted blob is corrupt or the master key does not match")

func isSealedBlob(head []byte) bool { return bytes.HasPrefix(head, blobMagic) }

func blobAEAD(key, salt []byte) (cipher.AEAD, error) {
	k, err := hkdf.Key(sha256.New, key, salt, "postwerk blob chunk v1", 32)
	if err != nil {
		return nil, err
	}
	block, err := aes.NewCipher(k)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}

func chunkNonce(counter uint64, last bool) []byte {
	n := make([]byte, 12)
	binary.BigEndian.PutUint64(n, counter)
	if last {
		n[11] = 1
	}
	return n
}

// sealBlob encrypts data into the blob format.
func sealBlob(key, data []byte) ([]byte, error) {
	salt := make([]byte, blobSaltSize)
	if _, err := rand.Read(salt); err != nil {
		return nil, err
	}
	aead, err := blobAEAD(key, salt)
	if err != nil {
		return nil, err
	}
	chunks := len(data)/blobChunkSize + 1
	out := make([]byte, 0, len(blobMagic)+blobSaltSize+len(data)+chunks*(4+aead.Overhead()))
	out = append(out, blobMagic...)
	out = append(out, salt...)
	for i := uint64(0); ; i++ {
		n := min(blobChunkSize, len(data))
		last := n == len(data)
		ctLen := n + aead.Overhead()
		hdr := uint32(ctLen)
		if last {
			hdr |= blobLastFlag
		}
		out = binary.BigEndian.AppendUint32(out, hdr)
		out = aead.Seal(out, chunkNonce(i, last), data[:n], nil)
		data = data[n:]
		if last {
			return out, nil
		}
	}
}

// openBlob decrypts a complete blob.
func openBlob(key []byte, r io.Reader) ([]byte, error) {
	br, err := newBlobReader(key, r)
	if err != nil {
		return nil, err
	}
	return io.ReadAll(br)
}

type blobReader struct {
	r       io.Reader
	aead    cipher.AEAD
	counter uint64
	buf     []byte
	done    bool
	err     error
}

func newBlobReader(key []byte, r io.Reader) (io.Reader, error) {
	head := make([]byte, len(blobMagic)+blobSaltSize)
	if _, err := io.ReadFull(r, head); err != nil || !isSealedBlob(head) {
		return nil, errBlobCorrupt
	}
	aead, err := blobAEAD(key, head[len(blobMagic):])
	if err != nil {
		return nil, err
	}
	return &blobReader{r: r, aead: aead}, nil
}

func (b *blobReader) Read(p []byte) (int, error) {
	for len(b.buf) == 0 {
		if b.err != nil {
			return 0, b.err
		}
		if b.done {
			return 0, io.EOF
		}
		b.err = b.next()
	}
	n := copy(p, b.buf)
	b.buf = b.buf[n:]
	return n, nil
}

func (b *blobReader) next() error {
	var hdr [4]byte
	if _, err := io.ReadFull(b.r, hdr[:]); err != nil {
		return errBlobCorrupt // truncated before the last chunk
	}
	h := binary.BigEndian.Uint32(hdr[:])
	last := h&blobLastFlag != 0
	ctLen := int(h &^ blobLastFlag)
	if ctLen < b.aead.Overhead() || ctLen > blobChunkSize+b.aead.Overhead() {
		return errBlobCorrupt
	}
	ct := make([]byte, ctLen)
	if _, err := io.ReadFull(b.r, ct); err != nil {
		return errBlobCorrupt
	}
	plain, err := b.aead.Open(ct[:0], chunkNonce(b.counter, last), ct, nil)
	if err != nil {
		return errBlobCorrupt
	}
	b.counter++
	b.buf = plain
	if last {
		var extra [1]byte
		if n, _ := b.r.Read(extra[:]); n > 0 {
			return errBlobCorrupt // trailing data after the last chunk
		}
		b.done = true
	}
	return nil
}
