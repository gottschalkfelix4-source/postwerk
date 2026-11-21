package metrics

import "github.com/prometheus/client_golang/prometheus"

var (
	// SendLimitHits counts rejected sends because an account hit a limit.
	SendLimitHits = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "postwerk_send_limit_hits_total", Help: "Outbound sends refused because an account reached a send limit.",
	}, []string{"kind"}) // kind: recipients|hourly|daily

	// Notifications counts operational notifications by channel and result.
	Notifications = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "postwerk_notifications_total", Help: "Operational notifications by channel and result.",
	}, []string{"channel", "result"}) // channel: mail|webhook; result: ok|failed
)

func init() {
	Registry.MustRegister(SendLimitHits, Notifications)
}
