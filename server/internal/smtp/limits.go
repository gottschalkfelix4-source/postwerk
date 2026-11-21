package smtp

import (
	"net"
	"sync"
)

// Default resource limits. A zero value in the corresponding Server field
// selects the default.
const (
	defaultMaxConns       = 500
	defaultMaxConnsPerIP  = 20
	defaultMaxLineLength  = 4096 // data lines, excluding CRLF (RFC 5322 allows 998)
	defaultMaxJunkCmds    = 100
	maxCommandLine        = 4096 // command lines including CRLF
	maxAddressLength      = 320
	defaultSessionMinutes = 30
)

// connLimiter tracks concurrent connections globally and per client address.
type connLimiter struct {
	mu    sync.Mutex
	total int
	perIP map[string]int
}

// ipKey groups IPv6 clients by /64 since a single host usually controls a
// whole prefix; IPv4 addresses are used as-is.
func ipKey(ip net.IP) string {
	if ip == nil {
		return ""
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

// validAddress performs a light syntax check on an envelope address: it
// rejects control characters, whitespace and angle brackets and enforces a
// maximum length. Deeper validation is left to the backend.
func validAddress(addr string) bool {
	if len(addr) > maxAddressLength {
		return false
	}
	for i := 0; i < len(addr); i++ {
		c := addr[i]
		if c < 0x21 || c == 0x7f || c == '<' || c == '>' {
			return false
		}
	}
	return true
}
