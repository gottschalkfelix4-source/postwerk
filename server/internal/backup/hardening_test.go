package backup

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// writeV1 builds an archive in the old PWBACKUP1 format.
func writeV1(t *testing.T, data []byte, password string) []byte {
	t.Helper()
	salt := make([]byte, saltLen)
	rand.Read(salt)
	aead, err := deriveKey(password, salt, kdfV1)
	if err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	buf.WriteString(magicV1)
	buf.Write(salt)
	w := &encWriter{w: &buf, aead: aead, buf: make([]byte, 0, chunkSize)}
	w.Write(data)
	w.Close()
	return buf.Bytes()
}

func TestDecryptV1Archive(t *testing.T) {
	data := bytes.Repeat([]byte("alte Sicherung "), 10000)
	enc := writeV1(t, data, "altes-passwort")
	f, _, err := Sniff(bytes.NewReader(enc))
	if err != nil || f != FormatEncrypted {
		t.Fatalf("sniff = %v, %v", f, err)
	}
	plain, encrypted, err := Open(bytes.NewReader(enc), "altes-passwort")
	if err != nil || !encrypted {
		t.Fatal(err)
	}
	got, err := io.ReadAll(plain)
	if err != nil || !bytes.Equal(got, data) {
		t.Fatalf("v1 round trip failed: %v", err)
	}
}

func TestEncryptWritesV2Header(t *testing.T) {
	var buf bytes.Buffer
	w, err := NewEncryptWriter(&buf, "ein-langes-passwort")
	if err != nil {
		t.Fatal(err)
	}
	w.Write([]byte("x"))
	w.Close()
	b := buf.Bytes()
	if !strings.HasPrefix(string(b), "PWBACKUP2\n") {
		t.Fatalf("header = %q", b[:10])
	}
	if b[10] != 17 || b[11] != 8 || b[12] != 1 {
		t.Fatalf("kdf params = %v", b[10:13])
	}
	// Hostile parameters are rejected before running scrypt.
	bad := append([]byte(nil), b...)
	bad[10] = 30
	if _, err := NewDecryptReader(bytes.NewReader(bad), "ein-langes-passwort"); err == nil {
		t.Fatal("accepted logN=30")
	}
	bad[10], bad[11] = 22, 32
	if _, err := NewDecryptReader(bytes.NewReader(bad), "ein-langes-passwort"); err == nil {
		t.Fatal("accepted 16 GB scrypt memory")
	}
}

// tarArchive builds a plain gzip archive from name -> content entries plus
// a manifest; modes are set to 0777 to check masking.
func tarArchive(t *testing.T, files map[string][]byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	man, _ := json.Marshal(Manifest{Format: 1})
	tw.WriteHeader(&tar.Header{Name: manifestName, Mode: 0o644, Size: int64(len(man)), Typeflag: tar.TypeReg})
	tw.Write(man)
	tw.WriteHeader(&tar.Header{Name: "data/sub/", Mode: 0o777, Typeflag: tar.TypeDir})
	for name, c := range files {
		tw.WriteHeader(&tar.Header{Name: name, Mode: 0o777, Size: int64(len(c)), Typeflag: tar.TypeReg})
		tw.Write(c)
	}
	tw.Close()
	gz.Close()
	return buf.Bytes()
}

func TestExtractLimits(t *testing.T) {
	ctx := context.Background()
	arc := tarArchive(t, map[string][]byte{
		"data/sub/a": bytes.Repeat([]byte("a"), 1000),
		"data/sub/b": bytes.Repeat([]byte("b"), 1000),
	})
	_, err := ExtractWithLimits(ctx, bytes.NewReader(arc), "", t.TempDir(), ExtractLimits{MaxBytes: 1500})
	if !errors.Is(err, errRestoreTooLarge) {
		t.Fatalf("size limit: %v", err)
	}
	_, err = ExtractWithLimits(ctx, bytes.NewReader(arc), "", t.TempDir(), ExtractLimits{MaxFiles: 2})
	if err == nil || !strings.Contains(err.Error(), "Dateien") {
		t.Fatalf("file limit: %v", err)
	}
	// Within limits the archive is extracted (and fails only on the missing database).
	dest := t.TempDir()
	_, err = ExtractWithLimits(ctx, bytes.NewReader(arc), "", dest, ExtractLimits{MaxBytes: 5000, MaxFiles: 10})
	if err == nil || !strings.Contains(err.Error(), "Datenbank fehlt") {
		t.Fatalf("expected missing database error, got %v", err)
	}
	if runtime.GOOS != "windows" {
		st, err := os.Stat(filepath.Join(dest, "data", "sub", "a"))
		if err != nil {
			t.Fatal(err)
		}
		if st.Mode().Perm()&^0o640 != 0 {
			t.Errorf("file mode not masked: %v", st.Mode())
		}
	}
}

func TestFreeDiskSpace(t *testing.T) {
	if runtime.GOOS != "linux" && runtime.GOOS != "windows" && runtime.GOOS != "darwin" {
		t.Skip()
	}
	if free, ok := freeDiskSpace(t.TempDir()); !ok || free == 0 {
		t.Fatalf("free = %d, %v", free, ok)
	}
}
