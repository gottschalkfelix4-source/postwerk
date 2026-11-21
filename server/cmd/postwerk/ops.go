package main

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"postwerk/internal/delivery"
	"postwerk/internal/message"
	"postwerk/internal/metrics"
	"postwerk/internal/queue"
	"postwerk/internal/store"
	"postwerk/internal/tlsutil"
)

// delayNotifier tells the sender once that a message is still queued.
// Local senders get the notice in their inbox, external ones via the queue
// with the null sender.
func delayNotifier(st *store.Store, svc *delivery.Service, q *queue.Queue, hostname func() string, log *slog.Logger) queue.Bouncer {
	return func(ctx context.Context, from string, pending []string, reason string, orig []byte) {
		host := hostname()
		headers := orig
		if i := bytes.Index(orig, []byte("\r\n\r\n")); i >= 0 {
			headers = orig[:i]
		}
		text := fmt.Sprintf("Dies ist eine automatische Nachricht von %s.\n\n"+
			"Ihre Nachricht konnte seit mehr als %d Stunden nicht an folgende Empfänger zugestellt werden:\n\n  %s\n\n"+
			"Letzter Fehler: %s\n\n"+
			"Es ist nichts zu tun: Der Server versucht es weiter, bis zu 5 Tage lang. "+
			"Schlägt die Zustellung endgültig fehl, erhalten Sie eine weitere Nachricht.\n\n"+
			"--- Kopfzeilen der ursprünglichen Nachricht ---\n\n%s\n",
			host, int(queue.DelayNotice.Hours()), strings.Join(pending, "\n  "), reason, string(headers))
		raw := message.Build(message.Draft{
			From:      store.Address{Name: "Mail Delivery System", Email: "mailer-daemon@" + host},
			To:        []store.Address{{Email: from}},
			Subject:   "Verzögerte Zustellung: Nachricht noch nicht zugestellt",
			Text:      text,
			MessageID: message.NewMessageID(host),
			Extra:     map[string]string{"Auto-Submitted": "auto-replied"},
		})
		local, external, err := st.ResolveRecipient(ctx, from)
		if err != nil {
			return
		}
		for _, addr := range local {
			acct, err := st.GetAccountByEmail(ctx, addr)
			if err != nil {
				continue
			}
			inbox, err := st.MailboxByRole(ctx, acct.ID, "inbox")
			if err != nil {
				continue
			}
			if _, err := svc.ImportMessage(ctx, acct, inbox, raw, delivery.ImportOptions{SkipAI: true}); err != nil {
				log.Warn("delay notice not stored", "component", "queue", "account", addr, "err", err)
			}
		}
		if len(external) > 0 {
			hash, err := st.Blobs.Put(raw)
			if err != nil {
				return
			}
			if _, err := st.Enqueue(ctx, "", external, hash, int64(len(raw))); err == nil {
				q.Notify()
			}
		}
	}
}

// opsSources feeds the operational Prometheus gauges.
func opsSources(st *store.Store, tp *tlsutil.Dynamic) metrics.OpsSources {
	return metrics.OpsSources{
		CertNotAfter: func() (time.Time, bool) {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			cs, err := tp.Current(ctx)
			if err != nil || cs.Leaf == nil {
				return time.Time{}, false
			}
			return cs.Leaf.NotAfter, true
		},
		QueueOldest: func() (time.Time, bool) { return st.OldestQueued(context.Background()) },
		LastBackup: func() (time.Time, bool) {
			_, t, err := st.LastBackupRun(context.Background())
			return t, err == nil && !t.IsZero()
		},
	}
}

// dailyCleanup empties old Trash/Junk messages, forgets old vacation replies,
// removes orphaned blobs and stale queue markers.
func dailyCleanup(ctx context.Context, st *store.Store, ret store.RetentionSettings, log *slog.Logger) {
	for _, r := range []struct {
		role string
		days int
	}{{"trash", ret.TrashDays}, {"junk", ret.JunkDays}} {
		if r.days <= 0 {
			continue
		}
		total := 0
		for {
			n, err := st.PurgeOldMailboxMessages(ctx, r.role, time.Duration(r.days)*24*time.Hour, 1000)
			total += n
			if err != nil {
				log.Error("retention cleanup failed", "mailbox", r.role, "err", err)
				break
			}
			if n < 1000 || ctx.Err() != nil {
				break
			}
		}
		if total > 0 {
			log.Info("old messages removed", "mailbox", r.role, "days", r.days, "count", total)
		}
	}
	if n, err := st.PurgeVacationReplies(ctx, 30*24*time.Hour); err != nil {
		log.Error("vacation reply cleanup failed", "err", err)
	} else if n > 0 {
		log.Info("old vacation replies forgotten", "count", n)
	}
	if ids, err := st.QueueIDs(ctx); err == nil {
		keep := map[string]bool{}
		for id := range ids {
			keep[fmt.Sprint(id)] = true
		}
		_, _ = st.DeleteKVPrefix(ctx, "queue-delay:", keep)
	}
	files, size, err := st.PurgeOrphanBlobs(ctx, 24*time.Hour)
	if err != nil {
		log.Error("orphaned blob cleanup failed", "err", err)
	} else if files > 0 {
		log.Info("orphaned blobs removed", "files", files, "bytes", size)
	}
	cleanupDKIMAndDAV(ctx, st, ret, log)
}

// cleanupDKIMAndDAV deletes retired DKIM keys well after their grace period
// and forgets old CalDAV/CardDAV tombstones.
func cleanupDKIMAndDAV(ctx context.Context, st *store.Store, ret store.RetentionSettings, log *slog.Logger) {
	if ret.DKIMCleanup {
		keys, err := st.PurgeRetiredDKIMKeys(ctx, store.DKIMCleanupMargin)
		if err != nil {
			log.Error("retired dkim key cleanup failed", "err", err)
		}
		// DNS provider credentials are not stored, so the TXT record cannot
		// be removed from here: the automatic DNS setup offers its removal.
		for _, k := range keys {
			log.Info("retired dkim key removed", "domain", k.Domain, "selector", k.Selector,
				"dns_record", k.Selector+"._domainkey."+k.Domain, "hint", "remove the TXT record at the DNS provider or via the automatic DNS setup")
		}
	}
	if ret.DAVTombstoneDays > 0 {
		if n, err := st.PruneDAVTombstones(ctx, time.Duration(ret.DAVTombstoneDays)*24*time.Hour); err != nil {
			log.Error("dav tombstone cleanup failed", "err", err)
		} else if n > 0 {
			log.Info("old dav tombstones forgotten", "count", n, "days", ret.DAVTombstoneDays)
		}
	}
}
