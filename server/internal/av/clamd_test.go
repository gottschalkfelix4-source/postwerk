package av

import (
	"encoding/binary"
	"errors"
	"io"
	"net"
	"testing"
	"time"
)

// fakeClamd accepts one connection, consumes the request and answers reply.
func fakeClamd(t *testing.T, reply string, abort bool) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { l.Close() })
	go func() {
		c, err := l.Accept()
		if err != nil {
			return
		}
		defer c.Close()
		cmd := make([]byte, len("zINSTREAM\x00"))
		if _, err := io.ReadFull(c, cmd); err != nil {
			return
		}
		if abort {
			// Answer early (like clamd once StreamMaxLength is reached), then
			// drain the rest so the client can still read the answer.
			_, _ = io.WriteString(c, reply+"\x00")
			_ = c.SetReadDeadline(time.Now().Add(500 * time.Millisecond))
			_, _ = io.Copy(io.Discard, io.LimitReader(c, 2<<20))
			return
		}
		// Consume chunks until the zero-length terminator.
		var size [4]byte
		for {
			if _, err := io.ReadFull(c, size[:]); err != nil {
				return
			}
			n := binary.BigEndian.Uint32(size[:])
			if n == 0 {
				break
			}
			if _, err := io.CopyN(io.Discard, c, int64(n)); err != nil {
				return
			}
		}
		_, _ = io.WriteString(c, reply+"\x00")
	}()
	return l.Addr().String()
}

func TestScanResults(t *testing.T) {
	if v, err := Scan(fakeClamd(t, "stream: OK", false), []byte("hello")); err != nil || v != "" {
		t.Fatalf("clean: %q %v", v, err)
	}
	if v, err := Scan(fakeClamd(t, "stream: Eicar-Signature FOUND", false), []byte("x")); err != nil || v != "Eicar-Signature" {
		t.Fatalf("virus: %q %v", v, err)
	}
	if _, err := Scan(fakeClamd(t, "INSTREAM size limit exceeded. ERROR", true), make([]byte, 1<<20)); !errors.Is(err, ErrSizeLimit) {
		t.Fatalf("size limit: %v", err)
	}
	if _, err := Scan(fakeClamd(t, "UNKNOWN COMMAND", false), []byte("x")); err == nil {
		t.Fatal("unexpected reply must be an error")
	}
}

func TestScanConnectionRefused(t *testing.T) {
	l, _ := net.Listen("tcp", "127.0.0.1:0")
	addr := l.Addr().String()
	l.Close()
	if _, err := Scan(addr, []byte("x")); err == nil {
		t.Fatal("expected connection error")
	}
}
