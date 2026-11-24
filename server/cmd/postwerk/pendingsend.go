package main

import (
	"context"
	"log/slog"
	"time"

	"postwerk/internal/delivery"
	"postwerk/internal/store"
)

// runPendingSends delivers parked webmail sends (Undo Send) whose delay has
// passed. Pending sends survive restarts: due entries are submitted on boot.
func runPendingSends(ctx context.Context, st *store.Store, svc *delivery.Service, log *slog.Logger) {
	t := time.NewTicker(time.Second)
	defer t.Stop()
	for {
		due, err := st.DuePendingSends(ctx, time.Now())
		if err != nil {
			log.Error("listing pending sends failed", "component", "undosend", "err", err)
		} else {
			for _, p := range due {
				acct, err := st.GetAccount(ctx, p.AccountID)
				if err != nil {
					log.Warn("pending send: account gone, dropping", "component", "undosend", "pending", p.ID, "err", err)
					_ = st.DeletePendingSend(ctx, p.AccountID, p.ID)
					continue
				}
				if _, err := svc.Submit(ctx, acct, p.From, p.Rcpts, p.Raw, true); err != nil {
					log.Error("pending send failed, dropping", "component", "undosend", "pending", p.ID, "user", acct.Email, "err", err)
				} else {
					log.Info("pending send delivered", "component", "undosend", "pending", p.ID, "user", acct.Email)
				}
				_ = st.DeletePendingSend(ctx, p.AccountID, p.ID)
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}
