// Package logx provides a slog handler that writes to stdout and keeps the
// most recent entries in memory so the web UI can show them.
package logx

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"sync"
	"time"
)

type Entry struct {
	Time      time.Time         `json:"time"`
	Level     string            `json:"level"`
	Component string            `json:"component"`
	Message   string            `json:"message"`
	Fields    map[string]string `json:"fields"`
}

type Ring struct {
	mu      sync.Mutex
	entries []Entry
	next    int
	full    bool
}

func NewRing(size int) *Ring { return &Ring{entries: make([]Entry, size)} }

func (r *Ring) add(e Entry) {
	r.mu.Lock()
	r.entries[r.next] = e
	r.next = (r.next + 1) % len(r.entries)
	if r.next == 0 {
		r.full = true
	}
	r.mu.Unlock()
}

// Query returns entries newest first, filtered by minimum level and substring.
func (r *Ring) Query(minLevel, q string, limit int) []Entry {
	r.mu.Lock()
	defer r.mu.Unlock()
	n := r.next
	if r.full {
		n = len(r.entries)
	}
	min := levelRank(minLevel)
	q = strings.ToLower(q)
	out := make([]Entry, 0, limit)
	for i := 0; i < n && len(out) < limit; i++ {
		idx := (r.next - 1 - i + len(r.entries)) % len(r.entries)
		e := r.entries[idx]
		if levelRank(e.Level) < min {
			continue
		}
		if q != "" && !matches(e, q) {
			continue
		}
		out = append(out, e)
	}
	return out
}

func matches(e Entry, q string) bool {
	if strings.Contains(strings.ToLower(e.Message), q) || strings.Contains(strings.ToLower(e.Component), q) {
		return true
	}
	for k, v := range e.Fields {
		if strings.Contains(strings.ToLower(k+"="+v), q) {
			return true
		}
	}
	return false
}

func levelRank(l string) int {
	switch l {
	case "debug":
		return 0
	case "", "info":
		return 1
	case "warn":
		return 2
	case "error":
		return 3
	}
	return 1
}

type Handler struct {
	ring   *Ring
	out    slog.Handler
	attrs  []slog.Attr
	groups []string
}

func New(ring *Ring, debug bool) *slog.Logger {
	lvl := slog.LevelInfo
	if debug {
		lvl = slog.LevelDebug
	}
	return slog.New(&Handler{ring: ring, out: slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: lvl})})
}

func (h *Handler) Enabled(ctx context.Context, l slog.Level) bool { return h.out.Enabled(ctx, l) }

func (h *Handler) Handle(ctx context.Context, r slog.Record) error {
	e := Entry{Time: r.Time.UTC(), Level: strings.ToLower(r.Level.String()), Message: r.Message, Fields: map[string]string{}}
	add := func(a slog.Attr) {
		if a.Key == "component" {
			e.Component = a.Value.String()
			return
		}
		e.Fields[a.Key] = fmt.Sprint(a.Value.Any())
	}
	for _, a := range h.attrs {
		add(a)
	}
	r.Attrs(func(a slog.Attr) bool { add(a); return true })
	if e.Component == "" {
		e.Component = "core"
	}
	h.ring.add(e)
	return h.out.Handle(ctx, r)
}

func (h *Handler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return &Handler{ring: h.ring, out: h.out.WithAttrs(attrs), attrs: append(append([]slog.Attr{}, h.attrs...), attrs...), groups: h.groups}
}

func (h *Handler) WithGroup(name string) slog.Handler {
	return &Handler{ring: h.ring, out: h.out.WithGroup(name), attrs: h.attrs, groups: append(h.groups, name)}
}
