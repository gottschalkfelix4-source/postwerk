// Package queue delivers outbound mail to remote MX hosts (or a relay host)
// with retries, bounces, delay notices and per-domain TLS policies
// (DANE, MTA-STS, opportunistic TLS).
package queue

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/smtp"
	"net/textproto"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"postwerk/internal/metrics"
	"postwerk/internal/store"
)

type Relay struct {
	Host     string // host:port; empty = deliver directly via MX
	Security string // "tls" (implicit, e.g. 465) or "starttls"
	User     string
	Password string
}

// Bouncer notifies the sender about recipients that failed (or, for delay
// notices, are still pending).
type Bouncer func(ctx context.Context, from string, failed []string, reason string, orig []byte)

type Queue struct {
	Store    *store.Store
	Hostname func() string
	// Relay returns the current smarthost configuration (may change at runtime).
	Relay  func() Relay
	Bounce Bouncer
	// Delay sends a one-time "still trying" notice after DelayNotice.
	Delay Bouncer
	// STS and DANE select verified TLS per destination; nil disables them.
	STS  *MTASTS
	DANE *DANE
	Log  *slog.Logger

	// Workers is the number of concurrent deliveries, PerDomain the limit per
	// destination domain (or relay).
	Workers   int
	PerDomain int

	wake chan struct{}

	mu       sync.Mutex
	running  int
	inflight map[int64]bool
	active   map[string]int
	wg       sync.WaitGroup
}

func New(st *store.Store, hostname func() string, relay func() Relay, log *slog.Logger) *Queue {
	return &Queue{Store: st, Hostname: hostname, Relay: relay, Log: log.With("component", "queue"),
		Workers: 8, PerDomain: 2, wake: make(chan struct{}, 1)}
}

func (q *Queue) Notify() {
	select {
	case q.wake <- struct{}{}:
	default:
	}
}

var backoff = []time.Duration{2 * time.Minute, 5 * time.Minute, 15 * time.Minute, 30 * time.Minute, time.Hour, 2 * time.Hour, 4 * time.Hour, 8 * time.Hour}

const (
	maxAge = 5 * 24 * time.Hour
	// DelayNotice is when the sender is told that delivery is still pending.
	DelayNotice = 4 * time.Hour

	dialTimeout = 20 * time.Second
	// cmdTimeout bounds every single read/write (RFC 5321 4.5.3.2 allows
	// up to 10 minutes for the final dot; 5 minutes covers real servers).
	cmdTimeout = 5 * time.Minute
	// txnTimeout caps one complete SMTP transaction.
	txnTimeout = 15 * time.Minute
)

func (q *Queue) Run(ctx context.Context) {
	if q.wake == nil {
		q.wake = make(chan struct{}, 1)
	}
	t := time.NewTicker(30 * time.Second)
	defer t.Stop()
	for {
		q.dispatch(ctx)
		metrics.QueueSize.Set(float64(q.Store.QueueSize(ctx)))
		select {
		case <-ctx.Done():
			q.wg.Wait()
			return
		case <-q.wake:
		case <-t.C:
		}
	}
}

// destinations returns the keys used for the per-domain limit.
func destinations(it *store.QueueItem, relay Relay) []string {
	if relay.Host != "" {
		return []string{"relay:" + relay.Host}
	}
	seen := map[string]bool{}
	var out []string
	for _, r := range it.To {
		d := strings.ToLower(store.DomainOf(r))
		if !seen[d] {
			seen[d] = true
			out = append(out, d)
		}
	}
	return out
}

// dispatch starts due items on free workers, respecting the per-domain limit.
func (q *Queue) dispatch(ctx context.Context) {
	items, err := q.Store.DueQueue(ctx)
	if err != nil {
		q.Log.Error("queue read failed", "err", err)
		return
	}
	workers, perDomain := max(q.Workers, 1), max(q.PerDomain, 1)
	relay := q.Relay()
	for _, it := range items {
		if ctx.Err() != nil {
			return
		}
		keys := destinations(it, relay)
		q.mu.Lock()
		if q.inflight == nil {
			q.inflight, q.active = map[int64]bool{}, map[string]int{}
		}
		if q.running >= workers {
			q.mu.Unlock()
			return
		}
		free := !q.inflight[it.ID]
		for _, k := range keys {
			if q.active[k] >= perDomain {
				free = false
			}
		}
		if !free {
			q.mu.Unlock()
			continue
		}
		q.running++
		q.inflight[it.ID] = true
		for _, k := range keys {
			q.active[k]++
		}
		q.mu.Unlock()
		q.wg.Add(1)
		go func(it *store.QueueItem, keys []string) {
			defer q.wg.Done()
			q.attempt(ctx, it, relay)
			q.mu.Lock()
			q.running--
			delete(q.inflight, it.ID)
			for _, k := range keys {
				if q.active[k]--; q.active[k] <= 0 {
					delete(q.active, k)
				}
			}
			q.mu.Unlock()
			q.Notify()
		}(it, keys)
	}
}

type rcptErr struct {
	rcpt      string
	permanent bool
	msg       string
}

func (q *Queue) remove(ctx context.Context, it *store.QueueItem) {
	if err := q.Store.DeleteQueueItem(ctx, it.ID); err != nil {
		q.Log.Error("queue delete failed", "id", it.ID, "err", err)
	}
	_ = q.Store.SetKV(ctx, delayKey(it.ID), "")
}

func delayKey(id int64) string { return "queue-delay:" + strconv.FormatInt(id, 10) }

// delayNoticeDue reports whether the one-time delay notice should go out now.
// The stored marker carries the creation time, so a reused queue ID never
// inherits the marker of an older message.
func (q *Queue) delayNoticeDue(ctx context.Context, it *store.QueueItem) bool {
	if q.Delay == nil || isNullSender(it.From) || time.Since(it.CreatedAt) < DelayNotice {
		return false
	}
	v, err := q.Store.KV(ctx, delayKey(it.ID))
	return err == nil && v != strconv.FormatInt(it.CreatedAt.Unix(), 10)
}

func (q *Queue) attempt(ctx context.Context, it *store.QueueItem, relay Relay) {
	data, err := q.Store.Blobs.Get(it.BlobHash)
	if err != nil {
		q.Log.Error("queue blob missing", "id", it.ID, "err", err)
		q.remove(ctx, it)
		return
	}
	byDomain := map[string][]string{}
	for _, r := range it.To {
		d := store.DomainOf(r)
		byDomain[d] = append(byDomain[d], r)
	}
	if relay.Host != "" {
		byDomain = map[string][]string{"": it.To}
	}
	var temp, perm []rcptErr
	for domain, rcpts := range byDomain {
		errs := q.deliverDomain(ctx, relay, domain, it.From, rcpts, data)
		for _, e := range errs {
			if e.permanent {
				perm = append(perm, e)
			} else {
				temp = append(temp, e)
			}
		}
	}
	delivered := len(it.To) - len(temp) - len(perm)
	if delivered > 0 {
		metrics.Messages.WithLabelValues("outbound", "sent").Add(float64(delivered))
	}
	if len(perm) > 0 {
		var failed, reasons []string
		for _, e := range perm {
			failed = append(failed, e.rcpt)
			reasons = append(reasons, e.rcpt+": "+e.msg)
		}
		q.Log.Warn("permanent delivery failure", "id", it.ID, "rcpts", strings.Join(failed, ","), "reason", strings.Join(reasons, "; "))
		if q.Bounce != nil {
			q.Bounce(ctx, it.From, failed, strings.Join(reasons, "\n"), data)
		}
	}
	if len(temp) == 0 {
		q.remove(ctx, it)
		if len(perm) == 0 {
			q.Log.Info("message delivered", "id", it.ID, "from", it.From, "rcpts", strings.Join(it.To, ","))
		}
		return
	}
	var remaining, msgs []string
	for _, e := range temp {
		remaining = append(remaining, e.rcpt)
		msgs = append(msgs, e.rcpt+": "+e.msg)
	}
	lastErr := strings.Join(msgs, "; ")
	if time.Since(it.CreatedAt) > maxAge {
		q.Log.Warn("giving up after max queue age", "id", it.ID, "err", lastErr)
		if q.Bounce != nil {
			q.Bounce(ctx, it.From, remaining, "Zustellung nach 5 Tagen aufgegeben: "+lastErr, data)
		}
		q.remove(ctx, it)
		return
	}
	if q.delayNoticeDue(ctx, it) {
		q.Log.Info("sending delay notice", "id", it.ID, "from", it.From, "rcpts", strings.Join(remaining, ","))
		q.Delay(ctx, it.From, remaining, lastErr, data)
		_ = q.Store.SetKV(ctx, delayKey(it.ID), strconv.FormatInt(it.CreatedAt.Unix(), 10))
	}
	idx := it.Attempts
	if idx >= len(backoff) {
		idx = len(backoff) - 1
	}
	metrics.Messages.WithLabelValues("outbound", "deferred").Inc()
	q.Log.Info("delivery deferred", "id", it.ID, "attempt", it.Attempts+1, "retry_in", backoff[idx].String(), "err", lastErr)
	_ = q.Store.UpdateQueueItem(ctx, it.ID, remaining, it.Attempts+1, time.Now().Add(backoff[idx]), lastErr)
}

func allErr(rcpts []string, permanent bool, msg string) []rcptErr {
	out := make([]rcptErr, len(rcpts))
	for i, r := range rcpts {
		out[i] = rcptErr{r, permanent, msg}
	}
	return out
}

func (q *Queue) deliverDomain(ctx context.Context, relay Relay, domain, from string, rcpts []string, data []byte) []rcptErr {
	if relay.Host != "" {
		return q.send(ctx, relay.Host, "", from, rcpts, data, &relay, nil)
	}
	hosts, err := lookupMX(ctx, domain)
	if err != nil {
		var de *net.DNSError
		if errors.As(err, &de) && de.IsNotFound {
			return allErr(rcpts, true, "Domain existiert nicht")
		}
		return allErr(rcpts, false, "DNS-Fehler: "+err.Error())
	}
	pol := q.domainPolicy(ctx, domain)
	var last []rcptErr
	for _, h := range hosts {
		tp, err := q.hostPolicy(ctx, pol, h)
		if err != nil {
			q.Log.Warn("mx host skipped by tls policy", "domain", domain, "mx", h, "err", err)
			last = allErr(rcpts, false, err.Error())
			continue
		}
		errs := q.send(ctx, net.JoinHostPort(h, "25"), h, from, rcpts, data, nil, tp)
		if len(errs) == 0 {
			return nil
		}
		// Retry the next MX only if the whole transaction failed temporarily.
		if errs[0].permanent || len(errs) < len(rcpts) {
			return errs
		}
		last = errs
	}
	return last
}

func lookupMX(ctx context.Context, domain string) ([]string, error) {
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	mxs, err := net.DefaultResolver.LookupMX(ctx, domain)
	if err != nil {
		var de *net.DNSError
		if errors.As(err, &de) && de.IsNotFound {
			// Implicit MX (RFC 5321 5.1): fall back to A/AAAA.
			if _, aerr := net.DefaultResolver.LookupHost(ctx, domain); aerr == nil {
				return []string{domain}, nil
			}
		}
		return nil, err
	}
	sort.Slice(mxs, func(i, j int) bool { return mxs[i].Pref < mxs[j].Pref })
	var hosts []string
	for _, mx := range mxs {
		h := strings.TrimSuffix(mx.Host, ".")
		if h == "" { // null MX (RFC 7505)
			return nil, &net.DNSError{Err: "null MX", Name: domain, IsNotFound: true}
		}
		hosts = append(hosts, h)
	}
	return hosts, nil
}

func codeOf(err error) (int, string) {
	var te *textproto.Error
	if errors.As(err, &te) {
		return te.Code, te.Msg
	}
	msg := err.Error()
	var code int
	if _, e := fmt.Sscanf(msg, "%d", &code); e == nil {
		return code, msg
	}
	return 0, msg
}

// idleConn bounds every read and write by cmdTimeout and the whole
// connection by a hard deadline.
type idleConn struct {
	net.Conn
	hard time.Time
}

func (c *idleConn) deadline() {
	d := time.Now().Add(cmdTimeout)
	if !c.hard.IsZero() && c.hard.Before(d) {
		d = c.hard
	}
	_ = c.Conn.SetDeadline(d)
}

func (c *idleConn) Read(p []byte) (int, error)  { c.deadline(); return c.Conn.Read(p) }
func (c *idleConn) Write(p []byte) (int, error) { c.deadline(); return c.Conn.Write(p) }

// dialSMTP connects to addr and returns a client after EHLO, TLS and (for a
// relay) authentication. tp selects the TLS policy towards an MX host (nil =
// opportunistic).
func dialSMTP(ctx context.Context, addr, helo string, relay *Relay, tp *hostTLS) (*smtp.Client, error) {
	d := net.Dialer{Timeout: dialTimeout}
	raw, err := d.DialContext(ctx, "tcp", addr)
	if err != nil {
		return nil, fmt.Errorf("Verbindung zu %s fehlgeschlagen: %w", addr, err)
	}
	hard, _ := ctx.Deadline()
	var conn net.Conn = &idleConn{Conn: raw, hard: hard}
	// Shutdown or the transaction deadline abort a hanging connection.
	context.AfterFunc(ctx, func() { raw.Close() })
	host, _, _ := net.SplitHostPort(addr)
	if relay != nil && relay.Security == "tls" {
		tc := tls.Client(conn, &tls.Config{ServerName: host, MinVersion: tls.VersionTLS12})
		if err := tc.HandshakeContext(ctx); err != nil {
			conn.Close()
			return nil, fmt.Errorf("TLS zu %s fehlgeschlagen: %w", addr, err)
		}
		conn = tc
	}
	c, err := smtp.NewClient(conn, host)
	if err != nil {
		conn.Close()
		return nil, err
	}
	if err := c.Hello(helo); err != nil {
		c.Close()
		return nil, err
	}
	if _, isTLS := conn.(*tls.Conn); !isTLS {
		if ok, _ := c.Extension("STARTTLS"); ok {
			// Verified TLS towards a relay; towards MX hosts as the policy says.
			cfg := &tls.Config{ServerName: host, MinVersion: tls.VersionTLS12}
			label := "relay"
			if relay == nil {
				if tp == nil {
					tp = opportunistic()
				}
				cfg, label = tp.config, tp.label
			}
			if err := c.StartTLS(cfg); err != nil {
				c.Close()
				metrics.OutboundTLS.WithLabelValues(label, "failed").Inc()
				return nil, fmt.Errorf("STARTTLS fehlgeschlagen: %w", err)
			}
			metrics.OutboundTLS.WithLabelValues(label, "ok").Inc()
		} else if relay != nil && relay.User != "" {
			c.Close()
			return nil, fmt.Errorf("der Smarthost bietet kein STARTTLS an – Port/Sicherheit prüfen (465 = TLS, 587 = STARTTLS)")
		} else if relay == nil && tp != nil && tp.required {
			c.Close()
			metrics.OutboundTLS.WithLabelValues(tp.label, "failed").Inc()
			return nil, fmt.Errorf("%s bietet kein STARTTLS an, %s verlangt aber TLS", host, tp.label)
		} else {
			lbl := "opportunistic"
			if relay != nil {
				lbl = "relay"
			} else if tp != nil {
				lbl = tp.label
			}
			metrics.OutboundTLS.WithLabelValues(lbl, "plaintext").Inc()
		}
	}
	if relay != nil && relay.User != "" {
		if err := c.Auth(smtp.PlainAuth("", relay.User, relay.Password, host)); err != nil {
			c.Close()
			return nil, fmt.Errorf("Anmeldung am Smarthost fehlgeschlagen: %w", err)
		}
	}
	return c, nil
}

// TestRelay checks connection, TLS and authentication of a smarthost.
func TestRelay(ctx context.Context, r Relay, helo string) error {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	c, err := dialSMTP(ctx, r.Host, helo, &r, nil)
	if err != nil {
		return err
	}
	_ = c.Quit()
	return nil
}

// isNullSender reports the empty reverse path used by bounces.
func isNullSender(from string) bool {
	f := strings.TrimSpace(from)
	return f == "" || f == "<>"
}

// envelopeFrom returns the address for MAIL FROM without angle brackets;
// net/smtp adds them, so the null sender becomes "MAIL FROM:<>".
func envelopeFrom(from string) string {
	if isNullSender(from) {
		return ""
	}
	return strings.TrimSuffix(strings.TrimPrefix(strings.TrimSpace(from), "<"), ">")
}

func (q *Queue) send(ctx context.Context, addr, mxHost, from string, rcpts []string, data []byte, relay *Relay, tp *hostTLS) []rcptErr {
	ctx, cancel := context.WithTimeout(ctx, txnTimeout)
	defer cancel()
	c, err := dialSMTP(ctx, addr, q.Hostname(), relay, tp)
	if err != nil {
		if relay != nil {
			// Smarthost problems (wrong password, port …) are configuration
			// errors: keep the mail queued instead of bouncing it.
			return allErr(rcpts, false, err.Error())
		}
		return classify(rcpts, err)
	}
	defer c.Close()
	if err := c.Mail(envelopeFrom(from)); err != nil {
		return classify(rcpts, err)
	}
	var errs []rcptErr
	var ok []string
	for _, r := range rcpts {
		if err := c.Rcpt(r); err != nil {
			code, msg := codeOf(err)
			errs = append(errs, rcptErr{r, code >= 500, msg})
			continue
		}
		ok = append(ok, r)
	}
	if len(ok) == 0 {
		return errs
	}
	w, err := c.Data()
	if err != nil {
		return append(errs, classify(ok, err)...)
	}
	if _, err := w.Write(data); err != nil {
		return append(errs, allErr(ok, false, err.Error())...)
	}
	if err := w.Close(); err != nil {
		return append(errs, classify(ok, err)...)
	}
	_ = c.Quit()
	return errs
}

func classify(rcpts []string, err error) []rcptErr {
	code, msg := codeOf(err)
	return allErr(rcpts, code >= 500, msg)
}
