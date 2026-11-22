package imapd

import (
	"net"
	"sync"
)

// Connection limits. Mail clients keep several connections per account
// open and offices share one address, so the per-IP limit is generous; it
// still stops a single host from exhausting file descriptors and memory.
const (
	defaultMaxConns      = 1000
	defaultMaxConnsPerIP = 50
)

// limitListener refuses connections above the total and per-IP limits
// before any IMAP or TLS processing happens.
type limitListener struct {
	net.Listener
	maxTotal, maxPerIP int

	mu    sync.Mutex
	total int
	perIP map[string]int
}

func newLimitListener(ln net.Listener, maxTotal, maxPerIP int) *limitListener {
	if maxTotal <= 0 {
		maxTotal = defaultMaxConns
	}
	if maxPerIP <= 0 {
		maxPerIP = defaultMaxConnsPerIP
	}
	return &limitListener{Listener: ln, maxTotal: maxTotal, maxPerIP: maxPerIP, perIP: map[string]int{}}
}

// ipKey groups IPv6 clients by /64, since one host usually controls a
// whole prefix.
func ipKey(addr net.Addr) string {
	host, _, err := net.SplitHostPort(addr.String())
	if err != nil {
		return addr.String()
	}
	ip := net.ParseIP(host)
	if ip == nil {
		return host
	}
	if ip.To4() == nil {
		return ip.Mask(net.CIDRMask(64, 128)).String()
	}
	return ip.String()
}

func (l *limitListener) Accept() (net.Conn, error) {
	for {
		c, err := l.Listener.Accept()
		if err != nil {
			return nil, err
		}
		key := ipKey(c.RemoteAddr())
		l.mu.Lock()
		ok := l.total < l.maxTotal && l.perIP[key] < l.maxPerIP
		if ok {
			l.total++
			l.perIP[key]++
		}
		l.mu.Unlock()
		if !ok {
			_, _ = c.Write([]byte("* BYE Too many connections\r\n"))
			_ = c.Close()
			continue
		}
		return &limitConn{Conn: c, release: func() { l.release(key) }}, nil
	}
}

func (l *limitListener) release(key string) {
	l.mu.Lock()
	l.total--
	if l.perIP[key]--; l.perIP[key] <= 0 {
		delete(l.perIP, key)
	}
	l.mu.Unlock()
}

type limitConn struct {
	net.Conn
	once    sync.Once
	release func()
}

func (c *limitConn) Close() error {
	err := c.Conn.Close()
	c.once.Do(c.release)
	return err
}
