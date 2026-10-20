package balancer

import (
	"net/http"
	"net/url"
	"sync/atomic"
)

// Backend represents a single upstream server instance and its runtime state.
type Backend struct {
	url                  *url.URL
	handler              http.Handler
	alive                atomic.Bool
	activeConns          atomic.Int64
	consecutiveFailures  atomic.Int64
	consecutiveSuccesses atomic.Int64
	unhealthyThreshold   int64
	healthyThreshold     int64
}

// NewBackend creates a Backend initialized as healthy (alive = true).
func NewBackend(target *url.URL, handler http.Handler, unhealthyThreshold, healthyThreshold int) *Backend {
	if unhealthyThreshold < 1 {
		unhealthyThreshold = 3
	}
	if healthyThreshold < 1 {
		healthyThreshold = 2
	}

	b := &Backend{
		url:                target,
		handler:            handler,
		unhealthyThreshold: int64(unhealthyThreshold),
		healthyThreshold:   int64(healthyThreshold),
	}
	b.alive.Store(true)
	return b
}

// URL returns the target URL of the backend.
func (b *Backend) URL() *url.URL {
	return b.url
}

// IsAlive reports whether the backend is currently considered healthy.
func (b *Backend) IsAlive() bool {
	return b.alive.Load()
}

// SetAlive explicitly sets the backend health state and resets counters.
func (b *Backend) SetAlive(alive bool) {
	b.alive.Store(alive)
	b.consecutiveFailures.Store(0)
	b.consecutiveSuccesses.Store(0)
}

// ActiveConnections returns the current number of in-flight requests to this backend.
func (b *Backend) ActiveConnections() int64 {
	return b.activeConns.Load()
}

// IncConnections increments the in-flight request counter by 1.
func (b *Backend) IncConnections() {
	b.activeConns.Add(1)
}

// DecConnections decrements the in-flight request counter by 1.
func (b *Backend) DecConnections() {
	b.activeConns.Add(-1)
}

// RecordFailure increments consecutive failures and marks the backend unhealthy
// if it reaches the configured unhealthy threshold. Returns true if state changed to unhealthy.
func (b *Backend) RecordFailure() bool {
	b.consecutiveSuccesses.Store(0)
	failures := b.consecutiveFailures.Add(1)
	if failures >= b.unhealthyThreshold {
		// CompareAndSwap ensures only the goroutine that flips true -> false returns true.
		return b.alive.CompareAndSwap(true, false)
	}
	return false
}

// RecordSuccess increments consecutive successes. If the backend is currently unhealthy
// and reaches the healthy threshold, it transitions back to healthy. Returns true if state changed to healthy.
func (b *Backend) RecordSuccess() bool {
	b.consecutiveFailures.Store(0)
	if b.IsAlive() {
		return false
	}
	successes := b.consecutiveSuccesses.Add(1)
	if successes >= b.healthyThreshold {
		if b.alive.CompareAndSwap(false, true) {
			b.consecutiveSuccesses.Store(0)
			return true
		}
	}
	return false
}

// ServeHTTP forwards the HTTP request to the backend while tracking active connections.
func (b *Backend) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	b.IncConnections()
	defer b.DecConnections()
	b.handler.ServeHTTP(w, r)
}
