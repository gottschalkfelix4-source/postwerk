package store

import (
	"context"
	"time"
)

// NotificationSettings configure alerts about operational warnings by mail
// and webhook (settings section "notifications").
type NotificationSettings struct {
	Enabled bool `json:"enabled"`
	// Recipients get the mails; empty means all admin accounts.
	Recipients []string `json:"recipients"`
	// Categories that trigger a notification (see NotificationCategories).
	Categories []string `json:"categories"`
	// MinSeverity is "warning" (everything) or "error" (only errors).
	MinSeverity string `json:"minSeverity"`
	// WebhookURL receives a JSON POST per notification (optional).
	WebhookURL string `json:"webhookUrl"`
	// WebhookToken is sent as "Authorization: Bearer <token>" (optional).
	WebhookToken string `json:"webhookToken" secret:"true"`
	// Telegram sends a message to a chat via a bot (optional).
	Telegram struct {
		Enabled  bool   `json:"enabled"`
		BotToken string `json:"botToken" secret:"true"`
		ChatID   string `json:"chatId"`
	} `json:"telegram"`
	// Ntfy pushes to an ntfy.sh-compatible server (optional).
	Ntfy struct {
		Enabled  bool   `json:"enabled"`
		Server   string `json:"server"`
		Topic    string `json:"topic"`
		Token    string `json:"token" secret:"true"`
		Priority int    `json:"priority"`
	} `json:"ntfy"`
}

// NotificationCategories are the known warning categories.
var NotificationCategories = []string{"certificate", "backup", "queue", "blacklist", "dns", "sendlimit", "system"}

func applyNotificationDefaults(s *Settings) {
	s.Notifications = NotificationSettings{
		Recipients:  []string{},
		Categories:  append([]string{}, NotificationCategories...),
		MinSeverity: "warning",
	}
}

// NotifyState is the notification state of one active warning.
type NotifyState struct {
	Key          string    `json:"key"`
	Severity     string    `json:"severity"`
	Title        string    `json:"title"`
	FirstSeen    time.Time `json:"firstSeen"`
	LastSeen     time.Time `json:"lastSeen"`
	LastNotified time.Time `json:"lastNotified"`
}

// ListNotifyState returns all warnings that were notified and not yet cleared.
func (s *Store) ListNotifyState(ctx context.Context) (map[string]*NotifyState, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT key, severity, title, first_seen, last_seen, last_notified FROM notify_state`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]*NotifyState{}
	for rows.Next() {
		var n NotifyState
		var first, last, notified int64
		if err := rows.Scan(&n.Key, &n.Severity, &n.Title, &first, &last, &notified); err != nil {
			return nil, err
		}
		n.FirstSeen, n.LastSeen, n.LastNotified = ts(first), ts(last), ts(notified)
		out[n.Key] = &n
	}
	return out, rows.Err()
}

// PutNotifyState inserts or replaces the state of a warning.
func (s *Store) PutNotifyState(ctx context.Context, n *NotifyState) error {
	_, err := s.DB.ExecContext(ctx, `INSERT INTO notify_state(key, severity, title, first_seen, last_seen, last_notified)
		VALUES(?, ?, ?, ?, ?, ?)
		ON CONFLICT(key) DO UPDATE SET severity = excluded.severity, title = excluded.title, first_seen = excluded.first_seen,
			last_seen = excluded.last_seen, last_notified = excluded.last_notified`,
		n.Key, n.Severity, n.Title, n.FirstSeen.Unix(), n.LastSeen.Unix(), n.LastNotified.Unix())
	return err
}

// DeleteNotifyState forgets a cleared warning.
func (s *Store) DeleteNotifyState(ctx context.Context, key string) error {
	_, err := s.DB.ExecContext(ctx, `DELETE FROM notify_state WHERE key = ?`, key)
	return err
}

// AdminEmails returns the addresses of all enabled admin accounts.
func (s *Store) AdminEmails(ctx context.Context) ([]string, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT email FROM accounts WHERE role = 'admin' AND disabled = 0 ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var e string
		if err := rows.Scan(&e); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}
