package middleware

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestIPFilterMiddleware(t *testing.T) {
	filter, err := NewIPFilter(
		[]string{"10.0.0.0/8", "192.168.1.10"},
		[]string{"10.0.0.99"},
	)
	if err != nil {
		t.Fatalf("unexpected error creating IP filter: %v", err)
	}

	okHandler := filter.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	tests := []struct {
		name       string
		remoteAddr string
		wantStatus int
	}{
		{
			name:       "allowed IP inside CIDR 10.0.0.0/8",
			remoteAddr: "10.0.0.5:54321",
			wantStatus: http.StatusOK,
		},
		{
			name:       "allowed exact IP 192.168.1.10",
			remoteAddr: "192.168.1.10:12345",
			wantStatus: http.StatusOK,
		},
		{
			name:       "denied IP inside allowed CIDR (deny takes precedence)",
			remoteAddr: "10.0.0.99:54321",
			wantStatus: http.StatusForbidden,
		},
		{
			name:       "denied IP outside allow list",
			remoteAddr: "172.16.0.1:54321",
			wantStatus: http.StatusForbidden,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/test", nil)
			req.RemoteAddr = tc.remoteAddr
			rec := httptest.NewRecorder()

			okHandler.ServeHTTP(rec, req)
			if rec.Code != tc.wantStatus {
				t.Errorf("expected status %d, got %d", tc.wantStatus, rec.Code)
			}
		})
	}
}

func TestRateLimiterMiddleware(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// 10 tokens/sec, burst of 2
	rl := NewRateLimiter(ctx, 10, 2, time.Minute, time.Minute)
	handler := rl.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	// First 2 requests from 10.1.1.1 should succeed (burst = 2)
	for i := 0; i < 2; i++ {
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		req.RemoteAddr = "10.1.1.1:1000"
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("request %d: expected 200 OK, got %d", i+1, rec.Code)
		}
	}

	// 3rd immediate request from 10.1.1.1 should be rate-limited (429)
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.RemoteAddr = "10.1.1.1:1000"
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("expected 429 Too Many Requests, got %d", rec.Code)
	}
	if got := rec.Header().Get("Retry-After"); got != "1" {
		t.Errorf("expected Retry-After header '1', got %q", got)
	}

	// Request from a DIFFERENT IP (10.1.1.2) should still succeed
	reqOther := httptest.NewRequest(http.MethodGet, "/", nil)
	reqOther.RemoteAddr = "10.1.1.2:1000"
	recOther := httptest.NewRecorder()
	handler.ServeHTTP(recOther, reqOther)
	if recOther.Code != http.StatusOK {
		t.Fatalf("expected 200 OK for different client IP, got %d", recOther.Code)
	}
}

func TestCircuitBreakerStateTransitions(t *testing.T) {
	// Trip after 2 failures, reset after 40ms, require 1 success in half-open
	cb := NewCircuitBreaker(2, 40*time.Millisecond, 1)

	shouldFail := true
	upstream := cb.Wrap(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if shouldFail {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))

	// 1. Trigger 2 failures -> state transitions from Closed to Open
	for i := 0; i < 2; i++ {
		rec := httptest.NewRecorder()
		upstream.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
		if rec.Code != http.StatusInternalServerError {
			t.Fatalf("expected 500 from upstream, got %d", rec.Code)
		}
	}

	if cb.State() != StateOpen {
		t.Fatalf("expected circuit state Open, got %s", cb.State())
	}

	// 2. While Open, requests fast-fail with 503 Service Unavailable
	rec := httptest.NewRecorder()
	upstream.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503 fast-fail while circuit is Open, got %d", rec.Code)
	}

	// 3. Wait for resetTimeout (40ms) -> transitions to Half-Open, then heals on 200 OK
	time.Sleep(50 * time.Millisecond)
	shouldFail = false

	rec = httptest.NewRecorder()
	upstream.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 OK in Half-Open probe, got %d", rec.Code)
	}

	if cb.State() != StateClosed {
		t.Fatalf("expected circuit state to return to Closed, got %s", cb.State())
	}
}

func BenchmarkRateLimiterAllow(b *testing.B) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	rl := NewRateLimiter(ctx, 100000, 100000, time.Minute, time.Minute)
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			_ = rl.Allow("192.168.1.1")
		}
	})
}
