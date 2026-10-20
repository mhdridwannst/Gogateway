package balancer

import (
	"net/http"
	"net/url"
	"sync"
	"testing"
)

func mustURL(t testing.TB, raw string) *url.URL {
	t.Helper()
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatalf("invalid url %q: %v", raw, err)
	}
	return u
}

func TestRoundRobinSelectionAndHealthSkip(t *testing.T) {
	noop := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {})
	b1 := NewBackend(mustURL(t, "http://127.0.0.1:8001"), noop, 2, 2)
	b2 := NewBackend(mustURL(t, "http://127.0.0.1:8002"), noop, 2, 2)
	b3 := NewBackend(mustURL(t, "http://127.0.0.1:8003"), noop, 2, 2)

	rr := NewRoundRobin([]*Backend{b1, b2, b3})

	// 1. Standard cycle: b1 -> b2 -> b3 -> b1
	wantOrder := []*Backend{b1, b2, b3, b1}
	for i, want := range wantOrder {
		if got := rr.Next(); got != want {
			t.Fatalf("step %d: expected %s, got %s", i, want.URL(), got.URL())
		}
	}

	// 2. Mark b2 unhealthy via passive failures (threshold = 2)
	b2.RecordFailure()
	if !b2.IsAlive() {
		t.Fatal("expected b2 to remain alive after 1 failure (threshold 2)")
	}
	b2.RecordFailure()
	if b2.IsAlive() {
		t.Fatal("expected b2 to transition to unhealthy after 2 failures")
	}

	// 3. Cycle should now skip b2: b3 -> b1 -> b3
	for i := 0; i < 4; i++ {
		got := rr.Next()
		if got == b2 {
			t.Fatalf("unhealthy backend b2 should not be selected")
		}
	}

	// 4. Mark all unhealthy -> Next() should return nil
	b1.SetAlive(false)
	b3.SetAlive(false)
	if got := rr.Next(); got != nil {
		t.Fatalf("expected nil when all backends are unhealthy, got %v", got)
	}
}

func TestLeastConnectionsSelectionAndConcurrency(t *testing.T) {
	noop := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {})
	b1 := NewBackend(mustURL(t, "http://127.0.0.1:8001"), noop, 3, 2)
	b2 := NewBackend(mustURL(t, "http://127.0.0.1:8002"), noop, 3, 2)
	b3 := NewBackend(mustURL(t, "http://127.0.0.1:8003"), noop, 3, 2)

	// Simulate in-flight load: b1 has 5 active, b2 has 1 active, b3 has 3 active
	for i := 0; i < 5; i++ {
		b1.IncConnections()
	}
	b2.IncConnections()
	for i := 0; i < 3; i++ {
		b3.IncConnections()
	}

	lc := NewLeastConnections([]*Backend{b1, b2, b3})
	if got := lc.Next(); got != b2 {
		t.Fatalf("expected b2 (1 active conn), got %s (%d conns)", got.URL(), got.ActiveConnections())
	}

	// Concurrent stress test for race conditions
	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 100; j++ {
				chosen := lc.Next()
				if chosen != nil {
					chosen.IncConnections()
					chosen.DecConnections()
				}
			}
		}()
	}
	wg.Wait()
}

func BenchmarkRoundRobinNext(b *testing.B) {
	noop := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {})
	backends := []*Backend{
		NewBackend(mustURL(b, "http://127.0.0.1:8001"), noop, 3, 2),
		NewBackend(mustURL(b, "http://127.0.0.1:8002"), noop, 3, 2),
		NewBackend(mustURL(b, "http://127.0.0.1:8003"), noop, 3, 2),
	}
	rr := NewRoundRobin(backends)

	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			_ = rr.Next()
		}
	})
}
