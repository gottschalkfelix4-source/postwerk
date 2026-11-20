package backup

import (
	"bufio"
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"fmt"
	"io"

	"golang.org/x/crypto/scrypt"
)

// Encrypted archives:
//
//	magic "PWBACKUP1\n" | salt (16) | records...
//	record = uint32 big-endian (ciphertext length | finalFlag) | ciphertext
//
// The key comes from scrypt(password, salt). Every record is sealed with
// AES-256-GCM; the nonce is the record counter and the AAD carries the final
// flag, so reordered, dropped or truncated records are detected.
const (
	magic     = "PWBACKUP1\n"
	saltLen   = 16
	chunkSize = 64 << 10
	finalFlag = 1 << 31
)

var ErrPassword = errors.New("falsches Passwort oder beschädigtes Archiv")

func deriveKey(password string, salt []byte) (cipher.AEAD, error) {
	key, err := scrypt.Key([]byte(password), salt, 1<<15, 8, 1, 32)
	if err != nil {
		return nil, err
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}

func nonce(counter uint64) []byte {
	n := make([]byte, 12)
	binary.BigEndian.PutUint64(n[4:], counter)
	return n
}

func aad(final bool) []byte {
	if final {
		return []byte{1}
	}
	return []byte{0}
}

type encWriter struct {
	w       io.Writer
	aead    cipher.AEAD
	buf     []byte
	counter uint64
	closed  bool
}

// NewEncryptWriter returns a writer that encrypts into w. Close must be
// called to write the final record; it does not close w.
func NewEncryptWriter(w io.Writer, password string) (io.WriteCloser, error) {
	salt := make([]byte, saltLen)
	if _, err := rand.Read(salt); err != nil {
		return nil, err
	}
	aead, err := deriveKey(password, salt)
	if err != nil {
		return nil, err
	}
	if _, err := io.WriteString(w, magic); err != nil {
		return nil, err
	}
	if _, err := w.Write(salt); err != nil {
		return nil, err
	}
	return &encWriter{w: w, aead: aead, buf: make([]byte, 0, chunkSize)}, nil
}

func (e *encWriter) Write(p []byte) (int, error) {
	n := 0
	for len(p) > 0 {
		space := chunkSize - len(e.buf)
		take := min(space, len(p))
		e.buf = append(e.buf, p[:take]...)
		p = p[take:]
		n += take
		// Keep a full buffer until more data arrives, so the last record can
		// always carry the final flag.
		if len(e.buf) == chunkSize && len(p) > 0 {
			if err := e.flush(false); err != nil {
				return n, err
			}
		}
	}
	return n, nil
}

func (e *encWriter) flush(final bool) error {
	ct := e.aead.Seal(nil, nonce(e.counter), e.buf, aad(final))
	e.counter++
	hdr := uint32(len(ct))
	if final {
		hdr |= finalFlag
	}
	var lb [4]byte
	binary.BigEndian.PutUint32(lb[:], hdr)
	if _, err := e.w.Write(lb[:]); err != nil {
		return err
	}
	if _, err := e.w.Write(ct); err != nil {
		return err
	}
	e.buf = e.buf[:0]
	return nil
}

func (e *encWriter) Close() error {
	if e.closed {
		return nil
	}
	e.closed = true
	return e.flush(true)
}

type decReader struct {
	r       io.Reader
	aead    cipher.AEAD
	buf     []byte
	counter uint64
	done    bool
}

// NewDecryptReader reads an archive written by NewEncryptWriter. The magic
// header must already have been consumed.
func NewDecryptReader(r io.Reader, password string) (io.Reader, error) {
	salt := make([]byte, saltLen)
	if _, err := io.ReadFull(r, salt); err != nil {
		return nil, fmt.Errorf("archiv zu kurz: %w", err)
	}
	aead, err := deriveKey(password, salt)
	if err != nil {
		return nil, err
	}
	return &decReader{r: r, aead: aead}, nil
}

func (d *decReader) Read(p []byte) (int, error) {
	for len(d.buf) == 0 {
		if d.done {
			return 0, io.EOF
		}
		var lb [4]byte
		if _, err := io.ReadFull(d.r, lb[:]); err != nil {
			return 0, errors.New("archiv ist unvollständig")
		}
		hdr := binary.BigEndian.Uint32(lb[:])
		final := hdr&finalFlag != 0
		size := int(hdr &^ finalFlag)
		if size > chunkSize+d.aead.Overhead() {
			return 0, ErrPassword
		}
		ct := make([]byte, size)
		if _, err := io.ReadFull(d.r, ct); err != nil {
			return 0, errors.New("archiv ist unvollständig")
		}
		pt, err := d.aead.Open(ct[:0], nonce(d.counter), ct, aad(final))
		if err != nil {
			return 0, ErrPassword
		}
		d.counter++
		d.buf = pt
		d.done = final
	}
	n := copy(p, d.buf)
	d.buf = d.buf[n:]
	return n, nil
}

// Format describes how an archive stream starts.
type Format int

const (
	FormatUnknown Format = iota
	FormatGzip
	FormatEncrypted
)

// Sniff peeks at the start of r and reports the archive format; the returned
// reader still yields the complete stream.
func Sniff(r io.Reader) (Format, *bufio.Reader, error) {
	br := bufio.NewReaderSize(r, 64<<10)
	head, err := br.Peek(len(magic))
	if err != nil && !errors.Is(err, io.EOF) {
		return FormatUnknown, br, err
	}
	switch {
	case bytes.Equal(head, []byte(magic)):
		return FormatEncrypted, br, nil
	case len(head) >= 2 && head[0] == 0x1f && head[1] == 0x8b:
		return FormatGzip, br, nil
	}
	return FormatUnknown, br, nil
}
