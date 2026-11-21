package metrics

import (
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus"
)

// Operational metrics: certificate lifetime, queue age, backups and the TLS
// policy used for outbound connections.

var OutboundTLS = prometheus.NewCounterVec(prometheus.CounterOpts{
	Name: "postwerk_outbound_tls_total", Help: "Outbound SMTP connections by TLS policy and result.",
}, []string{"policy", "result"}) // policy: dane|mta-sts|mta-sts-testing|opportunistic|relay; result: ok|plaintext|failed

func init() {
	Registry.MustRegister(OutboundTLS)
}

// OpsSources provides values that are read on every scrape.
type OpsSources struct {
	// CertNotAfter returns the expiry of the served certificate.
	CertNotAfter func() (time.Time, bool)
	// QueueOldest returns the creation time of the oldest queued message.
	QueueOldest func() (time.Time, bool)
	// LastBackup returns the finish time of the latest successful backup.
	LastBackup func() (time.Time, bool)
}

var opsOnce sync.Once

// RegisterOps adds the gauges backed by src. Only the first call has an effect.
func RegisterOps(src OpsSources) {
	opsOnce.Do(func() {
		gauge := func(name, help string, f func() float64) prometheus.Collector {
			return prometheus.NewGaugeFunc(prometheus.GaugeOpts{Name: name, Help: help}, f)
		}
		Registry.MustRegister(
			gauge("postwerk_tls_certificate_expiry_seconds", "Seconds until the served certificate expires (negative when expired).",
				func() float64 {
					if src.CertNotAfter == nil {
						return 0
					}
					t, ok := src.CertNotAfter()
					if !ok {
						return 0
					}
					return time.Until(t).Seconds()
				}),
			gauge("postwerk_queue_oldest_message_age_seconds", "Age of the oldest message in the outbound queue (0 when empty).",
				func() float64 {
					if src.QueueOldest == nil {
						return 0
					}
					t, ok := src.QueueOldest()
					if !ok {
						return 0
					}
					return time.Since(t).Seconds()
				}),
			gauge("postwerk_backup_last_success_timestamp_seconds", "Unix time of the latest successful backup (0 if none).",
				func() float64 {
					if src.LastBackup == nil {
						return 0
					}
					t, ok := src.LastBackup()
					if !ok {
						return 0
					}
					return float64(t.Unix())
				}),
		)
	})
}
