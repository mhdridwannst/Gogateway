package telemetry

import (
	"strconv"
	"time"

	"github.com/prometheus/client_golang/prometheus"
)

// Metrics holds all Prometheus collectors for the gateway.
type Metrics struct {
	registry        *prometheus.Registry
	requestsTotal   *prometheus.CounterVec
	requestDuration *prometheus.HistogramVec
}

// NewMetrics initializes an isolated Prometheus registry and registers gateway collectors.
func NewMetrics() *Metrics {
	reg := prometheus.NewRegistry()

	requestsTotal := prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "gateway_http_requests_total",
			Help: "Total number of HTTP requests processed by the gateway.",
		},
		[]string{"route", "method", "status"},
	)

	requestDuration := prometheus.NewHistogramVec(
		prometheus.HistogramOpts{
			Name:    "gateway_http_request_duration_seconds",
			Help:    "Histogram of HTTP request latencies in seconds.",
			Buckets: prometheus.DefBuckets,
		},
		[]string{"route", "method"},
	)

	reg.MustRegister(requestsTotal, requestDuration)

	return &Metrics{
		registry:        reg,
		requestsTotal:   requestsTotal,
		requestDuration: requestDuration,
	}
}

// Registry returns the underlying Prometheus registry for exposing via promhttp.
func (m *Metrics) Registry() *prometheus.Registry {
	return m.registry
}

// ObserveRequest records the status code and duration of a completed HTTP request.
func (m *Metrics) ObserveRequest(route, method string, statusCode int, duration time.Duration) {
	if route == "" {
		route = "unmatched"
	}
	statusStr := strconv.Itoa(statusCode)
	m.requestsTotal.WithLabelValues(route, method, statusStr).Inc()
	m.requestDuration.WithLabelValues(route, method).Observe(duration.Seconds())
}
