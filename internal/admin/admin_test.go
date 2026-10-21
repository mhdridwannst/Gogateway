package admin

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/mhdridwannst/Gogateway/internal/config"
	"github.com/mhdridwannst/Gogateway/internal/proxy"
	"github.com/mhdridwannst/Gogateway/internal/telemetry"
)

func TestAdminEndpoints(t *testing.T) {
	cfg := &config.Config{
		Routes: []config.RouteConfig{
			{
				Name:       "users-service",
				PathPrefix: "/users",
				Backends:   []string{"http://127.0.0.1:9001", "http://127.0.0.1:9002"},
			},
		},
	}

	gw, err := proxy.New(cfg)
	if err != nil {
		t.Fatalf("failed to create gateway: %v", err)
	}

	metrics := telemetry.NewMetrics()
	metrics.ObserveRequest("users-service", http.MethodGet, http.StatusOK, 15*time.Millisecond)

	handler := NewHandler(gw, metrics)
	srv := httptest.NewServer(handler)
	defer srv.Close()

	// 1. Verify GET /healthz
	resp, err := http.Get(srv.URL + "/healthz")
	if err != nil {
		t.Fatalf("GET /healthz failed: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected 200 OK from /healthz, got %d", resp.StatusCode)
	}

	// 2. Verify GET /backends
	respBackends, err := http.Get(srv.URL + "/backends")
	if err != nil {
		t.Fatalf("GET /backends failed: %v", err)
	}
	defer respBackends.Body.Close()
	if respBackends.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 OK from /backends, got %d", respBackends.StatusCode)
	}

	var payload struct {
		Routes []proxy.RouteStatus `json:"routes"`
	}
	if err := json.NewDecoder(respBackends.Body).Decode(&payload); err != nil {
		t.Fatalf("decoding /backends JSON: %v", err)
	}
	if len(payload.Routes) != 1 || len(payload.Routes[0].Backends) != 2 {
		t.Fatalf("expected 1 route with 2 backends, got %+v", payload.Routes)
	}
	if !payload.Routes[0].Backends[0].Alive {
		t.Errorf("expected backend 0 to be reported alive")
	}

	// 3. Verify GET /metrics exposes Prometheus metrics
	respMetrics, err := http.Get(srv.URL + "/metrics")
	if err != nil {
		t.Fatalf("GET /metrics failed: %v", err)
	}
	defer respMetrics.Body.Close()
	bodyBytes, err := io.ReadAll(respMetrics.Body)
	if err != nil {
		t.Fatalf("reading /metrics body: %v", err)
	}
	metricsText := string(bodyBytes)
	wantMetric := `gateway_http_requests_total{method="GET",route="users-service",status="200"} 1`
	if !strings.Contains(metricsText, wantMetric) {
		t.Errorf("expected /metrics output to contain %q, got:\n%s", wantMetric, metricsText)
	}
}
