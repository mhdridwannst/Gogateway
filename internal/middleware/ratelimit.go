package middleware

import (
	"context"
	"net/http"
	"sync"
	"time"
)

type clientBucket struct {
	tokens     float64
	lastRefill time.Time
}

// RateLimiter implements a thread-safe per-client-IP token bucket rate limiter.
type RateLimiter struct {
	mu      sync.Mutex
	buckets map[string]*clientBucket
	rate    float64
	burst   float64
	ttl     time.Duration
}

// NewRateLimiter creates a new RateLimiter and starts a background cleanup goroutine
// that exits cleanly when ctx is canceled.
func NewRateLimiter(ctx context.Context, requestsPerSec float64, burst int, cleanupInterval, clientTTL time.Duration) *RateLimiter {
	if cleanupInterval <= 0 {
		cleanupInterval = time.Minute
	}
	if clientTTL <= 0 {
		clientTTL = 3 * time.Minute
	}

	rl := &RateLimiter{
		buckets: make(map[string]*clientBucket),
		rate:    requestsPerSec,
		burst:   float64(burst),
		ttl:     clientTTL,
	}

	go rl.cleanupLoop(ctx, cleanupInterval)
	return rl
}

// Allow checks if a request from clientIP has an available token in its bucket.
func (rl *RateLimiter) Allow(clientIP string) bool {
	now := time.Now()

	rl.mu.Lock()
	defer rl.mu.Unlock()

	b, exists := rl.buckets[clientIP]
	if !exists {
		rl.buckets[clientIP] = &clientBucket{
			tokens:     rl.burst - 1,
			lastRefill: now,
		}
		return true
	}

	// Refill tokens based on elapsed time since last request.
	elapsed := now.Sub(b.lastRefill).Seconds()
	if elapsed > 0 {
		b.tokens += elapsed * rl.rate
		if b.tokens > rl.burst {
			b.tokens = rl.burst
		}
		b.lastRefill = now
	}

	if b.tokens >= 1.0 {
		b.tokens -= 1.0
		return true
	}

	return false
}

func (rl *RateLimiter) cleanupLoop(ctx context.Context, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case now := <-ticker.C:
			rl.evictExpired(now)
		}
	}
}

func (rl *RateLimiter) evictExpired(now time.Time) {
	rl.mu.Lock()
	defer rl.mu.Unlock()

	for ip, b := range rl.buckets {
		if now.Sub(b.lastRefill) > rl.ttl {
			delete(rl.buckets, ip)
		}
	}
}

// Middleware wraps an http.Handler to enforce per-IP rate limiting.
func (rl *RateLimiter) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		clientIP := "unknown"
		if addr, ok := extractClientAddr(r); ok {
			clientIP = addr.String()
		}

		if !rl.Allow(clientIP) {
			w.Header().Set("Content-Type", "application/json")
			w.Header().Set("Retry-After", "1")
			w.WriteHeader(http.StatusTooManyRequests)

			_, _ = w.Write([]byte(`{"error":"too many requests","message":"rate limit exceeded"}` + "\n"))

			return
		}

		next.ServeHTTP(w, r)
	})
}
