// Package av scans messages with a ClamAV daemon using the INSTREAM command.
package av

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"strings"
	"time"
)

// Scan returns the virus name, or "" if clean.
func Scan(addr string, data []byte) (string, error) {
	conn, err := net.DialTimeout("tcp", addr, 5*time.Second)
	if err != nil {
		return "", fmt.Errorf("clamd connect: %w", err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(60 * time.Second))
	if _, err := conn.Write([]byte("zINSTREAM\x00")); err != nil {
		return "", err
	}
	const chunk = 64 << 10
	var size [4]byte
	for off := 0; off < len(data); off += chunk {
		end := off + chunk
		if end > len(data) {
			end = len(data)
		}
		binary.BigEndian.PutUint32(size[:], uint32(end-off))
		if _, err := conn.Write(size[:]); err != nil {
			return "", err
		}
		if _, err := conn.Write(data[off:end]); err != nil {
			return "", err
		}
	}
	binary.BigEndian.PutUint32(size[:], 0)
	if _, err := conn.Write(size[:]); err != nil {
		return "", err
	}
	resp, err := io.ReadAll(conn)
	if err != nil {
		return "", err
	}
	r := strings.TrimSpace(string(bytes.TrimRight(resp, "\x00")))
	// Responses: "stream: OK", "stream: Eicar-Signature FOUND", "INSTREAM size limit exceeded. ERROR"
	switch {
	case strings.HasSuffix(r, " OK"):
		return "", nil
	case strings.HasSuffix(r, " FOUND"):
		r = strings.TrimSuffix(r, " FOUND")
		if i := strings.Index(r, ": "); i >= 0 {
			r = r[i+2:]
		}
		return r, nil
	default:
		return "", errors.New("clamd: " + r)
	}
}

// Ping checks that clamd answers.
func Ping(addr string) error {
	conn, err := net.DialTimeout("tcp", addr, 3*time.Second)
	if err != nil {
		return err
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
	if _, err := conn.Write([]byte("zPING\x00")); err != nil {
		return err
	}
	b, err := io.ReadAll(conn)
	if err != nil {
		return err
	}
	if !strings.HasPrefix(string(b), "PONG") {
		return errors.New("unexpected clamd reply")
	}
	return nil
}
