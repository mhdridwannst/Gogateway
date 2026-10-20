package balancer

import (
	"math"
	"sync/atomic"
)

// Balancer selects an available healthy Backend from a pool.
type Balancer interface {
	Next() *Backend
	Backends() []*Backend
}

// RoundRobin implements lock-free round-robin load balancing across healthy backends.
type RoundRobin struct {
	backends []*Backend
	counter  atomic.Uint64
}

// NewRoundRobin creates a new RoundRobin balancer.
func NewRoundRobin(backends []*Backend) *RoundRobin {
	return &RoundRobin{backends: backends}
}

// Next returns the next healthy backend in circular order, or nil if all are unhealthy.
func (rr *RoundRobin) Next() *Backend {
	n := uint64(len(rr.backends))
	if n == 0 {
		return nil
	}

	// Add(1) - 1 gives a 0-based index for the first request.
	start := rr.counter.Add(1) - 1
	for i := uint64(0); i < n; i++ {
		idx := (start + i) % n
		candidate := rr.backends[idx]
		if candidate.IsAlive() {
			return candidate
		}
	}
	return nil
}

// Backends returns all backends managed by this balancer.
func (rr *RoundRobin) Backends() []*Backend {
	return rr.backends
}

// LeastConnections routes requests to the healthy backend with the fewest active connections.
// Ties are broken using an atomic round-robin offset to prevent herd effects on index 0.
type LeastConnections struct {
	backends []*Backend
	offset   atomic.Uint64
}

// NewLeastConnections creates a new LeastConnections balancer.
func NewLeastConnections(backends []*Backend) *LeastConnections {
	return &LeastConnections{backends: backends}
}

// Next returns the healthy backend with the lowest active connection count, or nil if none are healthy.
func (lc *LeastConnections) Next() *Backend {
	n := uint64(len(lc.backends))
	if n == 0 {
		return nil
	}

	start := lc.offset.Add(1) - 1
	var best *Backend
	minConns := int64(math.MaxInt64)

	for i := uint64(0); i < n; i++ {
		idx := (start + i) % n
		candidate := lc.backends[idx]
		if !candidate.IsAlive() {
			continue
		}

		conns := candidate.ActiveConnections()
		if conns < minConns {
			minConns = conns
			best = candidate
		}
	}

	return best
}

// Backends returns all backends managed by this balancer.
func (lc *LeastConnections) Backends() []*Backend {
	return lc.backends
}
