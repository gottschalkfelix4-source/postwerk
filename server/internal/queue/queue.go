// Package queue delivers outbound mail to remote MX hosts (or a relay host)
// with retries and bounces.
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
	"strings"
	"time"

	"postwerk/internal/metrics"
	"postwerk/internal/store"
)

type Relay struct {
	Host     string // host:port
	User     string
	Password string
}

type Bouncer func(ctx context.Context, from string, failed []string, reason string, orig []byte)

type Queue struct {
	Store    *store.Store
	Hostname func() string
	Relay    Relay
	Bounce   Bouncer
	Log      *slog.Logger
	wake     chan struct{}
}

func New(st *store.Store, hostname func() string, relay Relay, log *slog.Logger) *Queue {
	return &Queue{Store: st, Hostname: hostname, Relay: relay, Log: log.With("component", "queue"), wake: make(chan struct{}, 1)}
}

func (q *Queue) Notify() {
	select {
	case q.wake <- struct{}{}:
	default:
	}
}

var backoff = []time.Duration{2 * time.Minute, 5 * time.Minute, 15 * time.Minute, 30 * time.Minute, time.Hour, 2 * time.Hour, 4 * time.Hour, 8 * time.Hour}

const maxAge = 5 * 24 * time.Hour

func (q *Queue) Run(ctx context.Context) {
	t := time.NewTicker(30 * time.Second)
	defer t.Stop()
	for {
		q.process(ctx)
		metrics.QueueSize.Set(float64(q.Store.QueueSize(ctx)))
		select {
		case <-ctx.Done():
			return
		case <-q.wake:
		case <-t.C:
		}
	}
}

func (q *Queue) process(ctx context.Context) {
	items, err := q.Store.DueQueue(ctx)
	if err != nil {
		q.Log.Error("queue read failed", "err", err)
		return
	}
	for _, it := range items {
		if ctx.Err() != nil {
			return
		}
		q.attempt(ctx, it)
	}
}

type rcptErr struct {
	rcpt      string
	permanent bool
	msg       string
}

func (q *Queue) attempt(ctx context.Context, it *store.QueueItem) {
	data, err := q.Store.Blobs.Get(it.BlobHash)
	if err != nil {
		q.Log.Error("queue blob missing", "id", it.ID, "err", err)
		_ = q.Store.DeleteQueueItem(ctx, it.ID)
		return
	}
	byDomain := map[string][]string{}
	for _, r := range it.To {
		d := store.DomainOf(r)
		byDomain[d] = append(byDomain[d], r)
	}
	if q.Relay.Host != "" {
		byDomain = map[string][]string{"": it.To}
	}
	var temp, perm []rcptErr
	for domain, rcpts := range byDomain {
		errs := q.deliverDomain(ctx, domain, it.From, rcpts, data)
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
		if err := q.Store.DeleteQueueItem(ctx, it.ID); err != nil {
			q.Log.Error("queue delete failed", "id", it.ID, "err", err)
		}
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
		_ = q.Store.DeleteQueueItem(ctx, it.ID)
		return
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

func (q *Queue) deliverDomain(ctx context.Context, domain, from string, rcpts []string, data []byte) []rcptErr {
	if q.Relay.Host != "" {
		return q.send(ctx, q.Relay.Host, "", from, rcpts, data, true)
	}
	hosts, err := lookupMX(ctx, domain)
	if err != nil {
		var de *net.DNSError
		if errors.As(err, &de) && de.IsNotFound {
			return allErr(rcpts, true, "Domain existiert nicht")
		}
		return allErr(rcpts, false, "DNS-Fehler: "+err.Error())
	}
	var last []rcptErr
	for _, h := range hosts {
		errs := q.send(ctx, net.JoinHostPort(h, "25"), h, from, rcpts, data, false)
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

func (q *Queue) send(ctx context.Context, addr, mxHost, from string, rcpts []string, data []byte, relay bool) []rcptErr {
	d := net.Dialer{Timeout: 30 * time.Second}
	conn, err := d.DialContext(ctx, "tcp", addr)
	if err != nil {
		return allErr(rcpts, false, "Verbindung zu "+addr+" fehlgeschlagen: "+err.Error())
	}
	_ = conn.SetDeadline(time.Now().Add(10 * time.Minute))
	host, _, _ := net.SplitHostPort(addr)
	c, err := smtp.NewClient(conn, host)
	if err != nil {
		conn.Close()
		return allErr(rcpts, false, err.Error())
	}
	defer c.Close()
	if err := c.Hello(q.Hostname()); err != nil {
		return classify(rcpts, err)
	}
	if ok, _ := c.Extension("STARTTLS"); ok {
		// Opportunistic TLS. Strict verification (MTA-STS/DANE) comes with a later milestone.
		cfg := &tls.Config{ServerName: host, InsecureSkipVerify: !relay, MinVersion: tls.VersionTLS12}
		if err := c.StartTLS(cfg); err != nil {
			return allErr(rcpts, false, "STARTTLS fehlgeschlagen: "+err.Error())
		}
	} else if relay && q.Relay.User != "" {
		return allErr(rcpts, false, "Relay bietet kein STARTTLS an")
	}
	if relay && q.Relay.User != "" {
		if err := c.Auth(smtp.PlainAuth("", q.Relay.User, q.Relay.Password, host)); err != nil {
			return allErr(rcpts, false, "Relay-Anmeldung fehlgeschlagen: "+err.Error())
		}
	}
	if err := c.Mail(from); err != nil {
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
