package health

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync/atomic"
	"testing"
	"time"

	"github.com/mhdridwannst/Gogateway/internal/balancer"
)

func TestActiveHealthCheckerTransitions(t *testing.T) {
	var shouldFail atomic.Bool
	shouldFail.Store(true) // Start failing

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/healthz" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		if shouldFail.Load() {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer upstream.Close()

	parsedURL, err := url.Parse(upstream.URL)
	if err != nil {
		t.Fatalf("parse url: %v", err)
	}

	// UnhealthyThreshold = 2, HealthyThreshold = 2
	backend := balancer.NewBackend(parsedURL, http.NotFoundHandler(), 2, 2)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	checker := NewChecker([]TargetGroup{
		{
			RouteName:  "test-route",
			HealthPath: "/healthz",
			Interval:   15 * time.Millisecond,
			Timeout:    100 * time.Millisecond,
			Backends:   []*balancer.Backend{backend},
		},
	})
	checker.Start(ctx)

	// Wait for backend to be marked unhealthy after 2 failed probes
	deadline := time.Now().Add(500 * time.Millisecond)
	for time.Now().Before(deadline) && backend.IsAlive() {
		time.Sleep(10 * time.Millisecond)
	}
	if backend.IsAlive() {
		t.Fatal("expected backend to be marked unhealthy by active checker")
	}

	// Flip upstream to healthy and wait for recovery
	shouldFail.Store(false)
	deadline = time.Now().Add(500 * time.Millisecond)
	for time.Now().Before(deadline) && !backend.IsAlive() {
		time.Sleep(10 * time.Millisecond)
	}
	if !backend.IsAlive() {
		t.Fatal("expected backend to recover and be marked healthy by active checker")
	}
}
