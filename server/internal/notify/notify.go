// Package notify sends operational warnings (certificate, backup, queue,
// blocklists, send limits, ...) to the administrators by mail and optionally
// to a webhook. Each warning is announced once, repeated at most once a day
// while it stays active, and followed by a short message when it clears.
package notify

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"

	"postwerk/internal/message"
	"postwerk/internal/metrics"
	"postwerk/internal/store"

	"postwerk/internal/httpx"
)

// Warning is an operational problem as shown on the dashboard.
type Warning struct {
	ID       string
	Severity string // warning | error
	Title    string
	Detail   string
	Link     string // route in the web interface
}

// Mailer delivers server-generated mail (delivery.Service).
type Mailer interface {
	SendSystemMail(ctx context.Context, from string, rcpts []string, raw []byte) error
	SystemSender(ctx context.Context) string
}

// Event kinds.
const (
	EventNew      = "new"
	EventReminder = "reminder"
	EventResolved = "resolved"
	EventTest     = "test"
)

const (
	// RenotifyAfter is the minimum time between two mails about the same
	// still active warning.
	RenotifyAfter = 24 * time.Hour
	// DefaultInterval is how often the warnings are evaluated.
	DefaultInterval = 5 * time.Minute
	webhookTimeout  = 10 * time.Second
)

type Service struct {
	Store    *store.Store
	Settings *store.SettingsCache
	Mailer   Mailer
	// Warnings returns the current operational warnings.
	Warnings func(ctx context.Context) []Warning
	Client   *http.Client
	Log      *slog.Logger
	// Now replaces time.Now (tests).
	Now func() time.Time
	// Interval between evaluations (0: DefaultInterval).
	Interval time.Duration

	mu       sync.Mutex // serializes Evaluate
	initOnce sync.Once
	wake     chan struct{}
}

func (n *Service) init() {
	n.initOnce.Do(func() { n.wake = make(chan struct{}, 1) })
}

func (n *Service) now() time.Time {
	if n.Now != nil {
		return n.Now()
	}
	return time.Now()
}

// Trigger requests an evaluation as soon as possible (non-blocking).
func (n *Service) Trigger() {
	if n == nil {
		return
	}
	n.init()
	select {
	case n.wake <- struct{}{}:
	default:
	}
}

// Run evaluates the warnings periodically and on Trigger until ctx ends.
func (n *Service) Run(ctx context.Context) {
	n.init()
	iv := n.Interval
	if iv <= 0 {
		iv = DefaultInterval
	}
	// The first call starts the background network checks (blocklists,
	// PTR); evaluating only after a minute keeps a warning that is still
	// listed from being reported as resolved right after a restart.
	if n.Warnings != nil {
		_ = n.Warnings(ctx)
	}
	t := time.NewTimer(time.Minute)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			t.Reset(iv)
		case <-n.wake:
		}
		if err := n.Evaluate(ctx); err != nil && ctx.Err() == nil {
			n.Log.Error("evaluating notifications failed", "component", "notify", "err", err)
		}
	}
}

// Category maps a warning ID to its notification category.
func Category(id string) string {
	switch {
	case id == "cert" || strings.HasPrefix(id, "cert-"):
		return "certificate"
	case strings.HasPrefix(id, "backup"):
		return "backup"
	case id == "queue" || strings.HasPrefix(id, "queue-"):
		return "queue"
	case strings.HasPrefix(id, "dnsbl-"):
		return "blacklist"
	case strings.HasPrefix(id, "ptr-") || strings.HasPrefix(id, "dns-"):
		return "dns"
	case strings.HasPrefix(id, "sendlimit-"):
		return "sendlimit"
	}
	return "system"
}

var categoryLabels = map[string]string{
	"certificate": "Zertifikat",
	"backup":      "Sicherung",
	"queue":       "Warteschlange",
	"blacklist":   "Sperrliste",
	"dns":         "DNS",
	"sendlimit":   "Sendelimit",
	"system":      "System",
}

func severityLabel(s string) string {
	if s == "error" {
		return "Fehler"
	}
	return "Warnung"
}

// Matches reports whether cfg selects the warning (category and severity).
func Matches(cfg store.NotificationSettings, w Warning) bool {
	if cfg.MinSeverity == "error" && w.Severity != "error" {
		return false
	}
	cat := Category(w.ID)
	for _, c := range cfg.Categories {
		if c == cat {
			return true
		}
	}
	return false
}

// Evaluate compares the current warnings with the persisted state and sends
// the due notifications.
func (n *Service) Evaluate(ctx context.Context) error {
	n.mu.Lock()
	defer n.mu.Unlock()
	cfg := n.Settings.Get()
	nc := cfg.Notifications
	if !nc.Enabled || n.Warnings == nil {
		// Forget what was announced, so that switching notifications on
		// again reports the then active warnings instead of stale clears.
		state, err := n.Store.ListNotifyState(ctx)
		if err != nil {
			return err
		}
		for key := range state {
			if err := n.Store.DeleteNotifyState(ctx, key); err != nil {
				return err
			}
		}
		return nil
	}
	now := n.now()
	all := n.Warnings(ctx)
	present := map[string]bool{}
	active := map[string]Warning{}
	var ids []string
	for _, w := range all {
		present[w.ID] = true
		if Matches(nc, w) {
			if _, dup := active[w.ID]; !dup {
				ids = append(ids, w.ID)
			}
			active[w.ID] = w
		}
	}
	sort.Strings(ids)
	state, err := n.Store.ListNotifyState(ctx)
	if err != nil {
		return err
	}
	var firstErr error
	keep := func(err error) {
		if firstErr == nil {
			firstErr = err
		}
	}
	for _, id := range ids {
		w := active[id]
		st := state[id]
		switch {
		case st == nil:
			if err := n.send(ctx, cfg, EventNew, w, now); err != nil {
				keep(err) // not recorded: retried on the next evaluation
				continue
			}
			st = &store.NotifyState{Key: id, FirstSeen: now, LastNotified: now}
		case w.Severity == "error" && st.Severity != "error":
			// Escalation from warning to error is announced like a new problem.
			if err := n.send(ctx, cfg, EventNew, w, st.FirstSeen); err != nil {
				keep(err)
				continue
			}
			st.LastNotified = now
		case now.Sub(st.LastNotified) >= RenotifyAfter:
			if err := n.send(ctx, cfg, EventReminder, w, st.FirstSeen); err != nil {
				keep(err)
			} else {
				st.LastNotified = now
			}
		}
		st.Severity, st.Title, st.LastSeen = w.Severity, w.Title, now
		if err := n.Store.PutNotifyState(ctx, st); err != nil {
			keep(err)
		}
	}
	var gone []string
	for id := range state {
		if _, ok := active[id]; !ok {
			gone = append(gone, id)
		}
	}
	sort.Strings(gone)
	for _, id := range gone {
		st := state[id]
		// Still present but no longer selected (settings changed): forget
		// it silently instead of claiming it was fixed.
		if !present[id] {
			w := Warning{ID: id, Severity: st.Severity, Title: st.Title}
			if err := n.send(ctx, cfg, EventResolved, w, st.FirstSeen); err != nil {
				keep(err)
				continue
			}
		}
		if err := n.Store.DeleteNotifyState(ctx, id); err != nil {
			keep(err)
		}
	}
	return firstErr
}

// TestResult reports the outcome of a test notification.
type TestResult struct {
	Recipients []string `json:"recipients"`
	MailError  string   `json:"mailError,omitempty"`
	Webhook    bool     `json:"webhook"`
	WebhookErr string   `json:"webhookError,omitempty"`
}

// SendTest sends a test notification with the saved settings, also while
// notifications are disabled.
func (n *Service) SendTest(ctx context.Context) TestResult {
	cfg := n.Settings.Get()
	w := Warning{ID: "test", Severity: "warning", Title: "Testbenachrichtigung",
		Detail: "Diese Nachricht bestätigt, dass Benachrichtigungen von Postwerk ankommen. Es ist nichts zu tun."}
	res := TestResult{Recipients: []string{}}
	rcpts, err := n.recipients(ctx, cfg.Notifications)
	res.Recipients = append(res.Recipients, rcpts...)
	if err == nil {
		err = n.sendMail(ctx, cfg, EventTest, w, n.now(), rcpts)
	}
	if err != nil {
		res.MailError = err.Error()
	}
	if cfg.Notifications.WebhookURL != "" {
		res.Webhook = true
		if err := n.sendWebhook(ctx, cfg, EventTest, w, n.now()); err != nil {
			res.WebhookErr = err.Error()
		}
	}
	return res
}

// recipients returns the configured addresses or, if none, all admins.
func (n *Service) recipients(ctx context.Context, nc store.NotificationSettings) ([]string, error) {
	var out []string
	seen := map[string]bool{}
	for _, r := range nc.Recipients {
		if r = store.Normalize(r); r != "" && !seen[r] {
			seen[r] = true
			out = append(out, r)
		}
	}
	if len(out) > 0 {
		return out, nil
	}
	admins, err := n.Store.AdminEmails(ctx)
	if err != nil {
		return nil, err
	}
	if len(admins) == 0 {
		return nil, errors.New("keine Empfänger: weder Adressen eingetragen noch Admin-Konten vorhanden")
	}
	return admins, nil
}

// send delivers one notification by mail and webhook. Only a failed mail
// is reported: a broken webhook must not cause a storm of repeated mails.
func (n *Service) send(ctx context.Context, cfg store.Settings, event string, w Warning, since time.Time) error {
	if cfg.Notifications.WebhookURL != "" {
		if err := n.sendWebhook(ctx, cfg, event, w, since); err != nil {
			n.Log.Warn("notification webhook failed", "component", "notify", "warning", w.ID, "err", err)
		}
	}
	rcpts, err := n.recipients(ctx, cfg.Notifications)
	if err == nil {
		err = n.sendMail(ctx, cfg, event, w, since, rcpts)
	}
	if err != nil {
		n.Log.Error("notification mail failed", "component", "notify", "warning", w.ID, "err", err)
		return err
	}
	n.Log.Info("notification sent", "component", "notify", "event", event, "warning", w.ID, "recipients", strings.Join(rcpts, ","))
	return nil
}

func subject(event string, w Warning) string {
	switch event {
	case EventReminder:
		return "[Postwerk] Weiterhin aktiv: " + w.Title
	case EventResolved:
		return "[Postwerk] Behoben: " + w.Title
	case EventTest:
		return "[Postwerk] Testbenachrichtigung"
	}
	return "[Postwerk] " + severityLabel(w.Severity) + ": " + w.Title
}

func linkURL(cfg store.Settings, w Warning) string {
	if w.Link == "" {
		return ""
	}
	base := strings.TrimRight(cfg.Web.PublicURL, "/")
	if base == "" {
		return ""
	}
	return base + w.Link
}

// Body returns the German mail text for a notification.
func Body(cfg store.Settings, event string, w Warning, since time.Time) string {
	host := cfg.Server.Hostname
	var b strings.Builder
	switch event {
	case EventResolved:
		fmt.Fprintf(&b, "Postwerk auf %s meldet: Das folgende Problem besteht nicht mehr.\n\n%s\n\n", host, w.Title)
		fmt.Fprintf(&b, "Erstmals festgestellt: %s\n", since.Local().Format("02.01.2006 15:04"))
	case EventTest:
		fmt.Fprintf(&b, "Postwerk auf %s: %s\n\n%s\n", host, w.Title, w.Detail)
	default:
		if event == EventReminder {
			fmt.Fprintf(&b, "Postwerk auf %s erinnert: Das folgende Problem besteht weiterhin.\n\n", host)
		} else {
			fmt.Fprintf(&b, "Postwerk auf %s meldet ein Problem:\n\n", host)
		}
		b.WriteString(w.Title + "\n\n")
		if w.Detail != "" {
			b.WriteString(w.Detail + "\n\n")
		}
		fmt.Fprintf(&b, "Schweregrad: %s\nKategorie: %s\nErstmals festgestellt: %s\n",
			severityLabel(w.Severity), categoryLabels[Category(w.ID)], since.Local().Format("02.01.2006 15:04"))
		if u := linkURL(cfg, w); u != "" {
			fmt.Fprintf(&b, "Details: %s\n", u)
		}
	}
	b.WriteString("\n-- \nDiese Nachricht wurde automatisch erzeugt. Solange ein Problem besteht, wird höchstens einmal pro Tag erinnert. " +
		"Empfänger und Kategorien lassen sich im Admin-Bereich unter Einstellungen > Benachrichtigungen ändern.\n")
	return b.String()
}

func (n *Service) sendMail(ctx context.Context, cfg store.Settings, event string, w Warning, since time.Time, rcpts []string) error {
	if n.Mailer == nil {
		return errors.New("kein Mailversand verfügbar")
	}
	from := n.Mailer.SystemSender(ctx)
	to := make([]store.Address, 0, len(rcpts))
	for _, r := range rcpts {
		to = append(to, store.Address{Email: r})
	}
	raw := message.Build(message.Draft{
		From:      store.Address{Name: "Postwerk", Email: from},
		To:        to,
		Subject:   subject(event, w),
		Text:      Body(cfg, event, w, since),
		MessageID: message.NewMessageID(store.DomainOf(from)),
		Extra:     map[string]string{"Auto-Submitted": "auto-generated", "Precedence": "bulk"},
	})
	err := n.Mailer.SendSystemMail(ctx, from, rcpts, raw)
	result := "ok"
	if err != nil {
		result = "failed"
	}
	metrics.Notifications.WithLabelValues("mail", result).Inc()
	return err
}

// WebhookPayload is the JSON body posted to the webhook. title, message and
// priority match what Gotify expects; the other fields are for generic
// receivers.
type WebhookPayload struct {
	Event    string `json:"event"`
	ID       string `json:"id"`
	Category string `json:"category"`
	Severity string `json:"severity"`
	Title    string `json:"title"`
	Message  string `json:"message"`
	Priority int    `json:"priority"`
	Host     string `json:"host"`
	Link     string `json:"link,omitempty"`
	Since    string `json:"since"`
	Time     string `json:"time"`
}

// Payload builds the webhook body for a notification.
func Payload(cfg store.Settings, event string, w Warning, since, now time.Time) WebhookPayload {
	prio := 5
	switch {
	case event == EventResolved || event == EventTest:
		prio = 3
	case w.Severity == "error":
		prio = 8
	}
	msg := w.Detail
	switch event {
	case EventResolved:
		msg = "Das Problem besteht nicht mehr."
	case EventReminder:
		msg = strings.TrimSpace("Besteht weiterhin. " + w.Detail)
	}
	return WebhookPayload{
		Event: event, ID: w.ID, Category: Category(w.ID), Severity: w.Severity,
		Title: strings.TrimPrefix(subject(event, w), "[Postwerk] "), Message: msg, Priority: prio,
		Host: cfg.Server.Hostname, Link: linkURL(cfg, w),
		Since: since.UTC().Format(time.RFC3339), Time: now.UTC().Format(time.RFC3339),
	}
}

// ValidWebhookURL checks that u is an absolute http(s) URL.
func ValidWebhookURL(u string) bool {
	p, err := url.Parse(u)
	return err == nil && (p.Scheme == "http" || p.Scheme == "https") && p.Host != "" && p.User == nil
}

func (n *Service) sendWebhook(ctx context.Context, cfg store.Settings, event string, w Warning, since time.Time) (err error) {
	defer func() {
		result := "ok"
		if err != nil {
			result = "failed"
		}
		metrics.Notifications.WithLabelValues("webhook", result).Inc()
	}()
	nc := cfg.Notifications
	if !ValidWebhookURL(nc.WebhookURL) {
		return errors.New("ungültige Webhook-URL")
	}
	body, err := json.Marshal(Payload(cfg, event, w, since, n.now()))
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, webhookTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, nc.WebhookURL, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "Postwerk")
	if nc.WebhookToken != "" {
		req.Header.Set("Authorization", "Bearer "+nc.WebhookToken)
	}
	client := n.Client
	if client == nil {
		client = &http.Client{Timeout: webhookTimeout, CheckRedirect: httpx.SameHostRedirect}
	}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return fmt.Errorf("Webhook antwortet mit HTTP %d", resp.StatusCode)
	}
	return nil
}
