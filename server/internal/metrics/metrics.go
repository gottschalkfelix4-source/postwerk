// Package metrics exposes Prometheus metrics.
package metrics

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
)

var (
	Registry = prometheus.NewRegistry()

	Connections = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "postwerk_connections_total", Help: "Accepted connections by protocol.",
	}, []string{"protocol"})

	Messages = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "postwerk_messages_total", Help: "Processed messages by direction and result.",
	}, []string{"direction", "result"}) // direction: inbound|outbound; result: delivered|junk|rejected|virus|greylisted|sent|deferred|bounced

	AuthAttempts = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "postwerk_auth_attempts_total", Help: "Authentication attempts by protocol and result.",
	}, []string{"protocol", "result"})

	QueueSize = prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "postwerk_queue_size", Help: "Messages waiting in the outbound queue.",
	})

	SpamScore = prometheus.NewHistogram(prometheus.HistogramOpts{
		Name: "postwerk_spam_score", Help: "Distribution of inbound spam scores.",
		Buckets: []float64{-5, -2, 0, 2, 5, 8, 10, 15, 20},
	})

	AIRequests = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "postwerk_ai_requests_total", Help: "AI endpoint requests by feature and result.",
	}, []string{"feature", "result"})
)

func init() {
	Registry.MustRegister(Connections, Messages, AuthAttempts, QueueSize, SpamScore, AIRequests,
		collectors.NewGoCollector(), collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}))
}
