package api

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"postwerk/internal/store"
	"postwerk/internal/tlsutil"
)

func adminEnv(t *testing.T) *testEnv {
	t.Helper()
	e := newAuthEnv(t)
	if _, err := e.s.Store.DB.Exec(`UPDATE accounts SET role = 'admin' WHERE id = ?`, e.acct.ID); err != nil {
		t.Fatal(err)
	}
	e.s.TLS = &tlsutil.Dynamic{DataDir: e.dir, Settings: func() store.TLSSettings { return e.s.Settings.Get().TLS },
		Hostname: func() string { return "mail.example.test" }}
	return e
}

func TestLimitAndNotificationSettings(t *testing.T) {
	e := adminEnv(t)
	c := e.login(t, "")

	rec := e.do(t, "PUT", "/api/admin/settings", map[string]any{"notifications": map[string]any{"webhookUrl": "ftp://example.test/x"}}, c)
	if rec.Code != 400 {
		t.Fatalf("invalid webhook scheme accepted: %d %s", rec.Code, rec.Body.String())
	}
	rec = e.do(t, "PUT", "/api/admin/settings", map[string]any{"limits": map[string]any{"maxRecipientsPerMessage": -1}}, c)
	if rec.Code != 400 {
		t.Fatalf("negative limit accepted: %d", rec.Code)
	}
	rec = e.do(t, "PUT", "/api/admin/settings", map[string]any{
		"limits": map[string]any{"maxRecipientsPerMessage": 50, "maxMessagesPerHour": 20, "maxRecipientsPerDay": 500},
		"notifications": map[string]any{"enabled": true, "recipients": []string{" Ops@Example.test "}, "categories": []string{"backup"},
			"minSeverity": "error", "webhookUrl": "https://ntfy.example.test/postwerk", "webhookToken": "tok"},
	}, c)
	if rec.Code != 200 {
		t.Fatalf("save: %d %s", rec.Code, rec.Body.String())
	}
	var got store.Settings
	_ = json.Unmarshal(rec.Body.Bytes(), &got)
	if got.Notifications.WebhookToken != mask || got.Notifications.Recipients[0] != "ops@example.test" || got.Limits.MaxMessagesPerHour != 20 {
		t.Fatalf("saved settings: %+v %+v", got.Notifications, got.Limits)
	}
	if tok := e.s.Settings.Get().Notifications.WebhookToken; tok != "tok" {
		t.Fatalf("token = %q", tok)
	}
	// The masked token survives a save with the same URL ...
	rec = e.do(t, "PUT", "/api/admin/settings", map[string]any{"notifications": map[string]any{"enabled": false, "webhookToken": mask}}, c)
	if rec.Code != 200 || e.s.Settings.Get().Notifications.WebhookToken != "tok" {
		t.Fatalf("masked token lost: %d %s", rec.Code, rec.Body.String())
	}
	// ... but is never sent to a new URL.
	rec = e.do(t, "PUT", "/api/admin/settings", map[string]any{"notifications": map[string]any{"webhookUrl": "https://evil.example.net/", "webhookToken": mask}}, c)
	if rec.Code != 400 {
		t.Fatalf("token moved to new URL: %d", rec.Code)
	}
}

func TestAccountSendLimitsAndWarning(t *testing.T) {
	e := adminEnv(t)
	c := e.login(t, "")
	path := fmt.Sprintf("/api/admin/accounts/%d/send-limits", e.acct.ID)

	rec := e.do(t, "PUT", path, map[string]any{"maxMessagesPerHour": -5}, c)
	if rec.Code != 400 {
		t.Fatalf("negative override accepted: %d", rec.Code)
	}
	rec = e.do(t, "PUT", path, map[string]any{"maxMessagesPerHour": 5, "maxRecipientsPerDay": 0}, c)
	if rec.Code != 200 {
		t.Fatalf("save override: %d %s", rec.Code, rec.Body.String())
	}
	var info sendLimitInfo
	_ = json.Unmarshal(rec.Body.Bytes(), &info)
	if info.Effective.MaxMessagesPerHour != 5 || info.Effective.MaxRecipientsPerDay != 0 || info.Effective.MaxRecipientsPerMessage != 100 {
		t.Fatalf("effective limits: %+v", info.Effective)
	}

	ctx := context.Background()
	if err := e.s.Store.ReserveSend(ctx, e.acct.ID, 3, info.Effective, time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := e.s.Store.RecordSendLimitHit(ctx, e.acct.ID, store.SendLimitHourly, time.Now()); err != nil {
		t.Fatal(err)
	}
	rec = e.do(t, "GET", path, nil, c)
	_ = json.Unmarshal(rec.Body.Bytes(), &info)
	if info.Usage.MessagesLastHour != 1 || info.Usage.RecipientsLastDay != 3 || info.HitsToday != 1 || info.LastHit == nil {
		t.Fatalf("usage: %+v hits=%d", info.Usage, info.HitsToday)
	}
	found := false
	for _, w := range e.s.OpsWarnings(ctx) {
		if w.ID == fmt.Sprintf("sendlimit-%d", e.acct.ID) {
			found = true
		}
	}
	if !found {
		t.Fatal("send limit hit not shown as ops warning")
	}
}
