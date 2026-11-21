package api

import (
	"context"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"

	"postwerk/internal/notify"
	"postwerk/internal/store"
)

// Send limits per account and operational notifications.

func (s *Server) limitsRoutes(m *http.ServeMux) {
	m.HandleFunc("GET /api/admin/accounts/{id}/send-limits", s.admin(s.getSendLimits))
	m.HandleFunc("PUT /api/admin/accounts/{id}/send-limits", s.admin(s.putSendLimits))
	m.HandleFunc("GET /api/admin/notifications/state", s.admin(s.notificationState))
	m.HandleFunc("POST /api/admin/notifications/test", s.admin(s.testNotification))
}

// sendLimitInfo is what the account detail page shows.
type sendLimitInfo struct {
	Override  store.SendLimitOverride `json:"override"`
	Defaults  store.SendLimitSettings `json:"defaults"`
	Effective store.SendLimits        `json:"effective"`
	Usage     store.SendUsage         `json:"usage"`
	HitsToday int                     `json:"hitsToday"`
	LastHit   *time.Time              `json:"lastHit"`
}

func (s *Server) sendLimitInfo(ctx context.Context, id int64) (*sendLimitInfo, error) {
	if _, err := s.Store.GetAccount(ctx, id); err != nil {
		return nil, err
	}
	o, err := s.Store.GetSendLimitOverride(ctx, id)
	if err != nil {
		return nil, err
	}
	info := &sendLimitInfo{Defaults: s.Settings.Get().Limits}
	if o != nil {
		info.Override = *o
	}
	info.Effective = info.Defaults.Effective(o)
	if info.Usage, err = s.Store.SendUsage(ctx, id, time.Now()); err != nil {
		return nil, err
	}
	hits, err := s.Store.SendLimitHitsSince(ctx, time.Now().Add(-24*time.Hour))
	if err != nil {
		return nil, err
	}
	for _, h := range hits {
		if h.AccountID == id {
			info.HitsToday = h.Hits
			t := h.LastHit
			info.LastHit = &t
		}
	}
	return info, nil
}

func (s *Server) getSendLimits(w http.ResponseWriter, r *http.Request, u *store.Account) (any, error) {
	id, err := pathID(r, "id")
	if err != nil {
		return nil, err
	}
	return s.sendLimitInfo(r.Context(), id)
}

func (s *Server) putSendLimits(w http.ResponseWriter, r *http.Request, u *store.Account) (any, error) {
	id, err := pathID(r, "id")
	if err != nil {
		return nil, err
	}
	var o store.SendLimitOverride
	if err := decode(r, &o); err != nil {
		return nil, err
	}
	if err := validateLimitValue("Empfänger pro Nachricht", o.MaxRecipientsPerMessage, store.MaxSendLimitRecipientsPerMessage); err != nil {
		return nil, err
	}
	if err := validateLimitValue("Nachrichten pro Stunde", o.MaxMessagesPerHour, store.MaxSendLimitValue); err != nil {
		return nil, err
	}
	if err := validateLimitValue("Empfänger pro Tag", o.MaxRecipientsPerDay, store.MaxSendLimitValue); err != nil {
		return nil, err
	}
	acct, err := s.Store.GetAccount(r.Context(), id)
	if err != nil {
		return nil, err
	}
	if err := s.Store.SetSendLimitOverride(r.Context(), id, o); err != nil {
		return nil, err
	}
	s.Log.Info("send limits changed", "component", "admin", "account", acct.Email, "exempt", o.Exempt, "by", u.Email)
	return s.sendLimitInfo(r.Context(), id)
}

func validateLimitValue(name string, v *int, max int) error {
	if v != nil && (*v < 0 || *v > max) {
		return invalid(fmt.Sprintf("%s: 0 (unbegrenzt) bis %d", name, max))
	}
	return nil
}

func (s *Server) notificationState(w http.ResponseWriter, r *http.Request, u *store.Account) (any, error) {
	st, err := s.Store.ListNotifyState(r.Context())
	if err != nil {
		return nil, err
	}
	out := make([]*store.NotifyState, 0, len(st))
	for _, v := range st {
		out = append(out, v)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].FirstSeen.Before(out[j].FirstSeen) })
	return out, nil
}

func (s *Server) testNotification(w http.ResponseWriter, r *http.Request, u *store.Account) (any, error) {
	if s.Notifier == nil {
		return nil, errf(503, "unavailable", "Benachrichtigungen sind nicht verfügbar")
	}
	res := s.Notifier.SendTest(r.Context())
	s.Log.Info("test notification sent", "component", "notify", "recipients", strings.Join(res.Recipients, ","),
		"mail_error", res.MailError, "webhook_error", res.WebhookErr, "by", u.Email)
	return res, nil
}

// OpsWarnings returns the dashboard warnings for the notification service.
func (s *Server) OpsWarnings(ctx context.Context) []notify.Warning {
	ws := s.dashboardWarnings(ctx)
	out := make([]notify.Warning, 0, len(ws))
	for _, w := range ws {
		out = append(out, notify.Warning{ID: w.ID, Severity: w.Severity, Title: w.Title, Detail: w.Detail, Link: w.Link})
	}
	return out
}

var sendLimitKinds = map[string]string{
	store.SendLimitRecipients: "Empfänger pro Nachricht",
	store.SendLimitHourly:     "Nachrichten pro Stunde",
	store.SendLimitDaily:      "Empfänger pro Tag",
}

// sendLimitWarnings lists the accounts that hit a send limit in the last
// 24 hours: a sign of a compromised account or a misbehaving script.
func (s *Server) sendLimitWarnings(ctx context.Context) []dashWarning {
	hits, err := s.Store.SendLimitHitsSince(ctx, time.Now().Add(-24*time.Hour))
	if err != nil {
		return nil
	}
	var out []dashWarning
	for _, h := range hits {
		email := h.Email
		if email == "" {
			email = fmt.Sprintf("Konto #%d", h.AccountID)
		}
		times := "einmal"
		if h.Hits > 1 {
			times = fmt.Sprintf("%d-mal", h.Hits)
		}
		out = append(out, dashWarning{
			ID: fmt.Sprintf("sendlimit-%d", h.AccountID), Severity: "warning",
			Title: "Sendelimit erreicht: " + email,
			Detail: fmt.Sprintf("Das Konto ist in den letzten 24 Stunden %s an ein Sendelimit gestoßen (zuletzt %s: %s). "+
				"Prüfen, ob das Konto kompromittiert ist (Passwort ändern, App-Passwörter widerrufen) oder das Limit für dieses Konto anheben.",
				times, h.LastHit.Local().Format("02.01.2006 15:04"), sendLimitKinds[h.LastKind]),
			Link: fmt.Sprintf("/admin/accounts/%d", h.AccountID),
		})
	}
	return out
}

// validateLimitsAndNotifications checks the "limits" and "notifications"
// settings sections.
func validateLimitsAndNotifications(c *store.Settings) error {
	l := &c.Limits
	if l.MaxRecipientsPerMessage < 0 || l.MaxRecipientsPerMessage > store.MaxSendLimitRecipientsPerMessage {
		return invalid(fmt.Sprintf("Sendelimits: Empfänger pro Nachricht 0 (unbegrenzt) bis %d", store.MaxSendLimitRecipientsPerMessage))
	}
	if l.MaxMessagesPerHour < 0 || l.MaxMessagesPerHour > store.MaxSendLimitValue || l.MaxRecipientsPerDay < 0 || l.MaxRecipientsPerDay > store.MaxSendLimitValue {
		return invalid("Sendelimits: ungültiger Wert")
	}
	n := &c.Notifications
	rcpts := []string{}
	seen := map[string]bool{}
	for _, r := range n.Recipients {
		r = store.Normalize(r)
		if r == "" || seen[r] {
			continue
		}
		if !validEmail(r) {
			return invalid("Benachrichtigungen: ungültige Adresse " + r)
		}
		seen[r] = true
		rcpts = append(rcpts, r)
	}
	n.Recipients = rcpts
	known := map[string]bool{}
	for _, k := range store.NotificationCategories {
		known[k] = true
	}
	cats := []string{}
	seenCat := map[string]bool{}
	for _, k := range n.Categories {
		if !known[k] {
			return invalid("Benachrichtigungen: unbekannte Kategorie " + k)
		}
		if !seenCat[k] {
			seenCat[k] = true
			cats = append(cats, k)
		}
	}
	n.Categories = cats
	switch n.MinSeverity {
	case "", "warning":
		n.MinSeverity = "warning"
	case "error":
	default:
		return invalid("Benachrichtigungen: ungültiger Schweregrad")
	}
	n.WebhookURL = strings.TrimSpace(n.WebhookURL)
	if n.WebhookURL != "" && !notify.ValidWebhookURL(n.WebhookURL) {
		return invalid("Benachrichtigungen: Die Webhook-URL muss mit http:// oder https:// beginnen")
	}
	if n.WebhookURL == "" {
		n.WebhookToken = ""
	}
	return nil
}
