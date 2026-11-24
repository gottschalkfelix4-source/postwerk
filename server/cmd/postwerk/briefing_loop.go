package main

import (
	"context"
	"log/slog"
	"strconv"
	"strings"
	"time"

	"postwerk/internal/ai"
	"postwerk/internal/delivery"
	"postwerk/internal/store"
)

// briefingLoop sends the optional daily AI summary. It is a no-op unless
// AI and AI.Briefing are enabled; the run time is "HH:MM" in server local
// time. Each account with new messages receives one summary mail per day
// (the last run date is remembered in the settings KV).
func briefingLoop(ctx context.Context, settings *store.SettingsCache, client *ai.Client, st *store.Store, svc *delivery.Service, log *slog.Logger) {
	log = log.With("component", "briefing")
	t := time.NewTicker(time.Minute)
	defer t.Stop()
	for {
		cfg := settings.Get()
		if cfg.AI.Enabled && cfg.AI.Briefing.Enabled && client != nil {
			now := time.Now()
			if dueBriefing(now, cfg.AI.Briefing.Time) {
				last, _ := st.KV(ctx, "briefing-last-run")
				today := now.Format("2006-01-02")
				if last != today && !cfg.AI.Features.Summarize {
					log.Warn("briefing enabled but summarize feature is off", "time", cfg.AI.Briefing.Time)
				}
				if last != today {
					since := now.Add(-24 * time.Hour)
					res, err := client.Briefing(ctx, st, svc, since, 15)
					if err != nil {
						log.Error("briefing failed", "err", err)
					} else {
						log.Info("briefing sent", "accounts", res.Accounts, "sent", res.Sent, "mails", res.Mails, "errors", len(res.Errors))
						_ = st.SetKV(ctx, "briefing-last-run", today)
					}
				}
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// dueBriefing reports whether the configured time has been reached.
func dueBriefing(now time.Time, t string) bool {
	h, m, ok := parseHHMM(t)
	if !ok {
		return false
	}
	return now.Hour() > h || (now.Hour() == h && now.Minute() >= m)
}

func parseHHMM(s string) (int, int, bool) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, 0, false
	}
	parts := strings.Split(s, ":")
	if len(parts) != 2 {
		return 0, 0, false
	}
	h, err := strconv.Atoi(parts[0])
	if err != nil || h < 0 || h > 23 {
		return 0, 0, false
	}
	m, err := strconv.Atoi(parts[1])
	if err != nil || m < 0 || m > 59 {
		return 0, 0, false
	}
	return h, m, true
}
