package admin

import (
	"encoding/json"
	"net/http"

	"github.com/mhdridwannst/Gogateway/internal/proxy"
	"github.com/mhdridwannst/Gogateway/internal/telemetry"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// SnapshotProvider defines the interface required to inspect gateway backend states.
type SnapshotProvider interface {
	RouteSnapshots() []proxy.RouteStatus
}

// NewHandler builds an HTTP handler serving /healthz, /backends, and /metrics.
func NewHandler(provider SnapshotProvider, metrics *telemetry.Metrics) http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"status":"ok"}` + "\n"))
	})

	mux.HandleFunc("GET /backends", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"routes": provider.RouteSnapshots(),
		})
	})

	if metrics != nil {
		mux.Handle("GET /metrics", promhttp.HandlerFor(metrics.Registry(), promhttp.HandlerOpts{}))
	}

	return mux
}
