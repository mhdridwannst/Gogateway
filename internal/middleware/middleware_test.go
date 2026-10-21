package middleware

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/mhdridwannst/Gogateway/internal/telemetry"
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

	rl := NewRateLimiter(ctx, 10, 2, time.Minute, time.Minute)
	handler := rl.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	for i := 0; i < 2; i++ {
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		req.RemoteAddr = "10.1.1.1:1000"
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("request %d: expected 200 OK, got %d", i+1, rec.Code)
		}
	}

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

	reqOther := httptest.NewRequest(http.MethodGet, "/", nil)
	reqOther.RemoteAddr = "10.1.1.2:1000"
	recOther := httptest.NewRecorder()
	handler.ServeHTTP(recOther, reqOther)
	if recOther.Code != http.StatusOK {
		t.Fatalf("expected 200 OK for different client IP, got %d", recOther.Code)
	}
}

func TestCircuitBreakerStateTransitions(t *testing.T) {
	cb := NewCircuitBreaker(2, 40*time.Millisecond, 1)

	shouldFail := true
	upstream := cb.Wrap(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if shouldFail {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))

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

	rec := httptest.NewRecorder()
	upstream.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503 fast-fail while circuit is Open, got %d", rec.Code)
	}

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

func TestRequestIDAndObserveMiddleware(t *testing.T) {
	var logBuf bytes.Buffer
	logger := telemetry.NewLogger(&logBuf, "info", "json")
	metrics := telemetry.NewMetrics()

	var capturedCtxReqID string
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedCtxReqID = RequestIDFromContext(r.Context())
		w.Header().Set("X-Gateway-Route", "payments")
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte("created"))
	})

	chain := RequestID(Observe(logger, metrics, inner))

	req := httptest.NewRequest(http.MethodPost, "/api/payments", nil)
	rec := httptest.NewRecorder()

	chain.ServeHTTP(rec, req)

	if rec.Code != http.StatusCreated {
		t.Fatalf("expected status 201, got %d", rec.Code)
	}

	respReqID := rec.Header().Get(RequestIDHeader)
	if len(respReqID) != 32 {
		t.Fatalf("expected 32-char hex X-Request-ID header on response, got %q", respReqID)
	}
	if capturedCtxReqID != respReqID {
		t.Errorf("expected context request ID %q to match response header %q", capturedCtxReqID, respReqID)
	}

	logOutput := logBuf.String()
	if !strings.Contains(logOutput, `"route":"payments"`) || !strings.Contains(logOutput, respReqID) {
		t.Errorf("expected JSON log to contain route and request_id, got: %s", logOutput)
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
