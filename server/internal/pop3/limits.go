package pop3

import (
	"bufio"
	"bytes"
	"errors"
	"net"
	"sync"
)

// Default resource limits. A zero value in the corresponding Server field
// selects the default.
const (
	defaultMaxConns      = 500
	defaultMaxConnsPerIP = 20
	defaultMaxIdleCmds   = 100
	// RFC 2449 limits command lines to 255 octets; some clients send longer
	// AUTH PLAIN initial responses, so allow a bit more.
	maxCommandLine = 1024
	// SASL continuation lines carry base64 credentials.
	maxAuthLine = 4096
)

var errLineTooLong = errors.New("line too long")

func orDefault(v, def int) int {
	if v > 0 {
		return v
	}
	return def
}

// connLimiter tracks concurrent connections globally and per client address.
type connLimiter struct {
	mu    sync.Mutex
	total int
	perIP map[string]int
}

// ipKey groups IPv6 clients by /64; IPv4 addresses are used as-is.
func ipKey(addr net.Addr) string {
	var ip net.IP
	if a, ok := addr.(*net.TCPAddr); ok {
		ip = a.IP
	} else {
		host, _, _ := net.SplitHostPort(addr.String())
		ip = net.ParseIP(host)
	}
	if ip == nil {
		return addr.String()
	}
	if v4 := ip.To4(); v4 != nil {
		return v4.String()
	}
	return ip.Mask(net.CIDRMask(64, 128)).String()
}

func (l *connLimiter) acquire(key string, maxTotal, maxPerIP int) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.perIP == nil {
		l.perIP = map[string]int{}
	}
	if l.total >= maxTotal || l.perIP[key] >= maxPerIP {
		return false
	}
	l.total++
	l.perIP[key]++
	return true
}

func (l *connLimiter) release(key string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.total--
	if n := l.perIP[key] - 1; n > 0 {
		l.perIP[key] = n
	} else {
		delete(l.perIP, key)
	}
}

// readLimitedLine reads one line terminated by LF and returns it without the
// line ending. If the line (including its ending) exceeds max bytes, it stops
// reading and returns errLineTooLong without buffering the remainder.
func readLimitedLine(r *bufio.Reader, max int) (string, error) {
	var line []byte
	for {
		chunk, err := r.ReadSlice('\n')
		if len(line)+len(chunk) > max {
			return "", errLineTooLong
		}
		if err == bufio.ErrBufferFull {
			line = append(line, chunk...)
			continue
		}
		if err != nil {
			return "", err
		}
		line = append(line, chunk...)
		break
	}
	line = bytes.TrimSuffix(line, []byte("\n"))
	line = bytes.TrimSuffix(line, []byte("\r"))
	return string(line), nil
}
