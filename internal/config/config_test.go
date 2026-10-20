package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadConfig(t *testing.T) {
	tests := []struct {
		name        string
		yamlContent string
		wantErrMsg  string
	}{
		{
			name: "valid configuration with defaults applied",
			yamlContent: `
server:
  listen_addr: ":9090"
routes:
  - name: "auth"
    path_prefix: "/auth"
    target_url: "http://localhost:8001"
    strip_prefix: true
`,
			wantErrMsg: "",
		},
		{
			name: "valid multi-backend configuration with least_conn",
			yamlContent: `
routes:
  - name: "orders"
    path_prefix: "/orders"
    strategy: "least_conn"
    backends:
      - "http://localhost:8001"
      - "http://localhost:8002"
    health_check:
      path: "/healthz"
      interval: 5s
      timeout: 1s
      unhealthy_threshold: 2
      healthy_threshold: 2
`,
			wantErrMsg: "",
		},
		{
			name: "reject unsupported load balancing strategy",
			yamlContent: `
routes:
  - name: "orders"
    path_prefix: "/orders"
    strategy: "random_magic"
    backends:
      - "http://localhost:8001"
`,
			wantErrMsg: "unsupported strategy",
		},
		{
			name: "reject empty routes",
			yamlContent: `
server:
  listen_addr: ":8080"
routes: []
`,
			wantErrMsg: "at least one route must be configured",
		},
		{
			name: "reject path prefix without leading slash",
			yamlContent: `
routes:
  - name: "bad-prefix"
    path_prefix: "api/v1"
    target_url: "http://localhost:8001"
`,
			wantErrMsg: "path_prefix must start with '/'",
		},
		{
			name: "reject duplicate path prefix",
			yamlContent: `
routes:
  - name: "svc-1"
    path_prefix: "/api"
    target_url: "http://localhost:8001"
  - name: "svc-2"
    path_prefix: "/api"
    target_url: "http://localhost:8002"
`,
			wantErrMsg: "duplicate path_prefix",
		},
		{
			name: "reject invalid target scheme",
			yamlContent: `
routes:
  - name: "ftp-svc"
    path_prefix: "/files"
    target_url: "ftp://localhost:21"
`,
			wantErrMsg: "backend url scheme must be http or https",
		},
		{
			name: "reject unknown yaml field",
			yamlContent: `
server:
  unknown_setting: 123
routes:
  - name: "svc"
    path_prefix: "/api"
    target_url: "http://localhost:8001"
`,
			wantErrMsg: "field unknown_setting not found",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			tmpDir := t.TempDir()
			cfgFile := filepath.Join(tmpDir, "gateway.yaml")

			if err := os.WriteFile(cfgFile, []byte(tc.yamlContent), 0600); err != nil {
				t.Fatalf("failed to write temp config: %v", err)
			}

			cfg, err := Load(cfgFile)
			if tc.wantErrMsg != "" {
				if err == nil {
					t.Fatalf("expected error containing %q, got nil", tc.wantErrMsg)
				}
				if !strings.Contains(err.Error(), tc.wantErrMsg) {
					t.Fatalf("expected error containing %q, got %q", tc.wantErrMsg, err.Error())
				}
				return
			}

			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if cfg.Server.ReadTimeout == 0 {
				t.Errorf("expected default ReadTimeout to be set, got 0")
			}
		})
	}
}
