package proxy

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/mhdridwannst/Gogateway/internal/config"
)

func TestGatewayRoutingAndProxy(t *testing.T) {
	usersBackend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Upstream", "users")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("users-path:" + r.URL.Path))
	}))
	defer usersBackend.Close()

	apiBackend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Upstream", "general-api")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("api-path:" + r.URL.Path))
	}))
	defer apiBackend.Close()

	cfg := &config.Config{
		Routes: []config.RouteConfig{
			{
				Name:        "general-api",
				PathPrefix:  "/api",
				TargetURL:   apiBackend.URL,
				StripPrefix: false,
			},
			{
				Name:        "users-service",
				PathPrefix:  "/api/v1/users",
				TargetURL:   usersBackend.URL,
				StripPrefix: true,
			},
			{
				Name:        "dead-service",
				PathPrefix:  "/dead",
				TargetURL:   "http://127.0.0.1:59999",
				StripPrefix: true,
			},
		},
	}

	gw, err := New(cfg)
	if err != nil {
		t.Fatalf("failed to create gateway: %v", err)
	}

	gatewayServer := httptest.NewServer(gw)
	defer gatewayServer.Close()

	tests := []struct {
		name           string
		path           string
		wantStatus     int
		wantBodySubstr string
		wantUpstream   string
		wantRoute      string
	}{
		{
			name:           "matches more specific prefix (/api/v1/users) and strips prefix",
			path:           "/api/v1/users/42/profile",
			wantStatus:     http.StatusOK,
			wantBodySubstr: "users-path:/42/profile",
			wantUpstream:   "users",
			wantRoute:      "users-service",
		},
		{
			name:           "matches general prefix (/api) without stripping prefix",
			path:           "/api/v1/orders",
			wantStatus:     http.StatusOK,
			wantBodySubstr: "api-path:/api/v1/orders",
			wantUpstream:   "general-api",
			wantRoute:      "general-api",
		},
		{
			name:           "does not match partial segment boundary (/apikeys != /api)",
			path:           "/apikeys",
			wantStatus:     http.StatusNotFound,
			wantBodySubstr: `"error":"not found"`,
			wantUpstream:   "",
			wantRoute:      "",
		},
		{
			name:           "returns 502 Bad Gateway when upstream is unreachable",
			path:           "/dead/check",
			wantStatus:     http.StatusBadGateway,
			wantBodySubstr: `"error":"bad gateway"`,
			wantUpstream:   "",
			wantRoute:      "",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			resp, err := http.Get(gatewayServer.URL + tc.path)
			if err != nil {
				t.Fatalf("request failed: %v", err)
			}
			defer resp.Body.Close()

			if resp.StatusCode != tc.wantStatus {
				t.Errorf("expected status %d, got %d", tc.wantStatus, resp.StatusCode)
			}

			if tc.wantUpstream != "" {
				if got := resp.Header.Get("X-Upstream"); got != tc.wantUpstream {
					t.Errorf("expected X-Upstream %q, got %q", tc.wantUpstream, got)
				}
			}
			if tc.wantRoute != "" {
				gotRoute := resp.Header.Get("X-Gateway-Route")
				if gotRoute != tc.wantRoute {
					t.Errorf("expected X-Gateway-Route %q, got %q", tc.wantRoute, gotRoute)
				}
			}

			bodyBytes, err := io.ReadAll(resp.Body)
			if err != nil {
				t.Fatalf("reading response body: %v", err)
			}
			if !strings.Contains(string(bodyBytes), tc.wantBodySubstr) {
				t.Errorf("expected body to contain %q, got %q", tc.wantBodySubstr, string(bodyBytes))
			}
		})
	}
}
