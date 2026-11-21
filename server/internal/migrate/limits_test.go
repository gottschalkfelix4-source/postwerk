package migrate

import (
	"archive/zip"
	"bufio"
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestImportZipBomb(t *testing.T) {
	svc, acct := setup(t)
	p := filepath.Join(t.TempDir(), "bomb.zip")
	f, _ := os.Create(p)
	zw := zip.NewWriter(f)
	w, _ := zw.Create("Inbox.mbox")
	w.Write([]byte(mbox))
	// 256 MB of 'A' without a newline compresses to a few hundred KB.
	chunk := bytes.Repeat([]byte("A"), 1<<20)
	for range 256 {
		w.Write(chunk)
	}
	zw.Close()
	f.Close()
	st, _ := os.Stat(p)
	if st.Size() > 2<<20 {
		t.Fatalf("test archive unexpectedly large: %d", st.Size())
	}
	fi := &FileImporter{Delivery: svc, Account: acct, MaxSize: 10 << 20}
	res, err := fi.ImportFile(context.Background(), p, "bomb.zip")
	if err != nil {
		t.Fatal(err)
	}
	if res.Failed == 0 || !strings.Contains(strings.Join(res.Errors, "\n"), "Limit") {
		t.Fatalf("zip bomb not stopped: %+v", res)
	}
	if fi.remaining > 0 {
		t.Fatalf("budget not exhausted: %d", fi.remaining)
	}
}

func TestImportZipTooManyEntries(t *testing.T) {
	svc, acct := setup(t)
	p := filepath.Join(t.TempDir(), "many.zip")
	f, _ := os.Create(p)
	zw := zip.NewWriter(f)
	for i := range 5 {
		w, _ := zw.Create(filepath.ToSlash(filepath.Join("x", string(rune('a'+i))+".eml")))
		w.Write([]byte("Subject: x\r\n\r\nx\r\n"))
	}
	zw.Close()
	f.Close()
	fi := &FileImporter{Delivery: svc, Account: acct, MaxSize: 10 << 20, MaxEntries: 3}
	if _, err := fi.ImportFile(context.Background(), p, "many.zip"); err == nil || !strings.Contains(err.Error(), "zu viele") {
		t.Fatalf("expected entry limit error, got %v", err)
	}
}

func TestImportMboxEndlessLine(t *testing.T) {
	svc, acct := setup(t)
	p := filepath.Join(t.TempDir(), "Inbox")
	f, _ := os.Create(p)
	f.WriteString(mbox)
	f.WriteString("\nFrom eve@example.org Wed Jan  8 12:00:00 2025\nSubject: Lang\n\n")
	// 8 MB line without newline at the end of the file.
	f.Write(bytes.Repeat([]byte("x"), 8<<20))
	f.Close()
	fi := &FileImporter{Delivery: svc, Account: acct, MaxSize: 10 << 20, MaxLine: 1 << 20}
	res, err := fi.ImportFile(context.Background(), p, "Inbox")
	if err != nil {
		t.Fatal(err)
	}
	if res.Imported != 2 || res.Failed != 1 {
		t.Fatalf("result = %+v", res)
	}
}

func TestReadLineBounded(t *testing.T) {
	in := "kurz\n" + strings.Repeat("y", 300) + "\nende"
	br := bufio.NewReaderSize(strings.NewReader(in), 16)
	l, long, err := readLine(br, nil, 100)
	if string(l) != "kurz\n" || long || err != nil {
		t.Fatalf("first = %q %v %v", l, long, err)
	}
	_, long, err = readLine(br, nil, 100)
	if !long || err != nil {
		t.Fatalf("second: long=%v err=%v", long, err)
	}
	l, long, _ = readLine(br, nil, 100)
	if string(l) != "ende" || long {
		t.Fatalf("third = %q %v", l, long)
	}
}
