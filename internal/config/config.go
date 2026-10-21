package config

import (
	"errors"
	"fmt"
	"net/netip"
	"net/url"
	"os"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

const (
	StrategyRoundRobin = "round_robin"
	StrategyLeastConn  = "least_conn"
)

// Config represents the root configuration for the gateway.
type Config struct {
	Server    ServerConfig    `yaml:"server"`
	Admin     AdminConfig     `yaml:"admin"`
	Logging   LogConfig       `yaml:"logging"`
	IPFilter  IPFilterConfig  `yaml:"ip_filter"`
	RateLimit RateLimitConfig `yaml:"rate_limit"`
	Routes    []RouteConfig   `yaml:"routes"`
}

// ServerConfig holds HTTP server and lifecycle timeouts.
type ServerConfig struct {
	ListenAddr        string        `yaml:"listen_addr"`
	ReadTimeout       time.Duration `yaml:"read_timeout"`
	ReadHeaderTimeout time.Duration `yaml:"read_header_timeout"`
	WriteTimeout      time.Duration `yaml:"write_timeout"`
	IdleTimeout       time.Duration `yaml:"idle_timeout"`
	ShutdownTimeout   time.Duration `yaml:"shutdown_timeout"`
	MaxHeaderBytes    int           `yaml:"max_header_bytes"`
}

// AdminConfig holds configuration for the internal observability/admin HTTP server.
type AdminConfig struct {
	Enabled    bool   `yaml:"enabled"`
	ListenAddr string `yaml:"listen_addr"`
}

// LogConfig defines structured logging settings.
type LogConfig struct {
	Level  string `yaml:"level"`  // "debug", "info", "warn", "error"
	Format string `yaml:"format"` // "json" or "text"
}

// IPFilterConfig defines global IP allow and deny lists (supports single IPs or CIDR blocks).
type IPFilterConfig struct {
	AllowList []string `yaml:"allow_list"`
	DenyList  []string `yaml:"deny_list"`
}

// RateLimitConfig defines per-client-IP token bucket rate limiting parameters.
type RateLimitConfig struct {
	Enabled         bool          `yaml:"enabled"`
	RequestsPerSec  float64       `yaml:"requests_per_sec"`
	Burst           int           `yaml:"burst"`
	CleanupInterval time.Duration `yaml:"cleanup_interval"`
	ClientTTL       time.Duration `yaml:"client_ttl"`
}

// CircuitBreakerConfig defines per-route circuit breaker thresholds.
type CircuitBreakerConfig struct {
	Enabled          bool          `yaml:"enabled"`
	FailureThreshold int           `yaml:"failure_threshold"`
	ResetTimeout     time.Duration `yaml:"reset_timeout"`
	HalfOpenMaxReqs  int           `yaml:"half_open_max_requests"`
}

// HealthCheckConfig defines active and passive health check parameters for a route.
type HealthCheckConfig struct {
	Path               string        `yaml:"path"`
	Interval           time.Duration `yaml:"interval"`
	Timeout            time.Duration `yaml:"timeout"`
	UnhealthyThreshold int           `yaml:"unhealthy_threshold"`
	HealthyThreshold   int           `yaml:"healthy_threshold"`
}

// RouteConfig defines how an incoming request path maps to a pool of upstream backends.
type RouteConfig struct {
	Name           string               `yaml:"name"`
	PathPrefix     string               `yaml:"path_prefix"`
	TargetURL      string               `yaml:"target_url"`
	Backends       []string             `yaml:"backends"`
	Strategy       string               `yaml:"strategy"`
	StripPrefix    bool                 `yaml:"strip_prefix"`
	Timeout        time.Duration        `yaml:"timeout"`
	Retries        int                  `yaml:"retries"`
	RetryBackoff   time.Duration        `yaml:"retry_backoff"`
	HealthCheck    HealthCheckConfig    `yaml:"health_check"`
	CircuitBreaker CircuitBreakerConfig `yaml:"circuit_breaker"`
}

// Load reads a YAML configuration file from the given path, applies defaults, and validates it.
func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading config file %q: %w", path, err)
	}

	var cfg Config
	decoder := yaml.NewDecoder(strings.NewReader(string(data)))
	decoder.KnownFields(true)
	if err := decoder.Decode(&cfg); err != nil {
		return nil, fmt.Errorf("parsing yaml config: %w", err)
	}

	cfg.applyDefaults()

	if err := cfg.Validate(); err != nil {
		return nil, fmt.Errorf("validating config: %w", err)
	}

	return &cfg, nil
}

func (c *Config) applyDefaults() {
	if c.Server.ListenAddr == "" {
		c.Server.ListenAddr = ":8080"
	}
	if c.Server.ReadTimeout == 0 {
		c.Server.ReadTimeout = 5 * time.Second
	}
	if c.Server.ReadHeaderTimeout == 0 {
		c.Server.ReadHeaderTimeout = 2 * time.Second
	}
	if c.Server.WriteTimeout == 0 {
		c.Server.WriteTimeout = 10 * time.Second
	}
	if c.Server.IdleTimeout == 0 {
		c.Server.IdleTimeout = 60 * time.Second
	}
	if c.Server.ShutdownTimeout == 0 {
		c.Server.ShutdownTimeout = 10 * time.Second
	}
	if c.Server.MaxHeaderBytes == 0 {
		c.Server.MaxHeaderBytes = 1 << 20 // 1 MB
	}

	if c.Admin.ListenAddr == "" {
		c.Admin.ListenAddr = ":9091"
	}

	if c.Logging.Level == "" {
		c.Logging.Level = "info"
	}
	if c.Logging.Format == "" {
		c.Logging.Format = "json"
	}

	if c.RateLimit.Enabled {
		if c.RateLimit.RequestsPerSec == 0 {
			c.RateLimit.RequestsPerSec = 20
		}
		if c.RateLimit.Burst == 0 {
			c.RateLimit.Burst = 40
		}
		if c.RateLimit.CleanupInterval == 0 {
			c.RateLimit.CleanupInterval = 1 * time.Minute
		}
		if c.RateLimit.ClientTTL == 0 {
			c.RateLimit.ClientTTL = 3 * time.Minute
		}
	}

	for i := range c.Routes {
		r := &c.Routes[i]
		if len(r.Backends) == 0 && r.TargetURL != "" {
			r.Backends = []string{r.TargetURL}
		}
		if r.Strategy == "" {
			r.Strategy = StrategyRoundRobin
		}
		if r.Timeout == 0 {
			r.Timeout = 5 * time.Second
		}
		if r.RetryBackoff == 0 {
			r.RetryBackoff = 50 * time.Millisecond
		}
		if r.HealthCheck.Interval == 0 {
			r.HealthCheck.Interval = 10 * time.Second
		}
		if r.HealthCheck.Timeout == 0 {
			r.HealthCheck.Timeout = 2 * time.Second
		}
		if r.HealthCheck.UnhealthyThreshold == 0 {
			r.HealthCheck.UnhealthyThreshold = 3
		}
		if r.HealthCheck.HealthyThreshold == 0 {
			r.HealthCheck.HealthyThreshold = 2
		}
		if r.CircuitBreaker.Enabled {
			if r.CircuitBreaker.FailureThreshold == 0 {
				r.CircuitBreaker.FailureThreshold = 5
			}
			if r.CircuitBreaker.ResetTimeout == 0 {
				r.CircuitBreaker.ResetTimeout = 10 * time.Second
			}
			if r.CircuitBreaker.HalfOpenMaxReqs == 0 {
				r.CircuitBreaker.HalfOpenMaxReqs = 2
			}
		}
	}
}

// Validate checks that all required configuration fields are present and sane.
func (c *Config) Validate() error {
	if c.Server.ReadTimeout < 0 || c.Server.WriteTimeout < 0 || c.Server.IdleTimeout < 0 ||
		c.Server.ReadHeaderTimeout < 0 || c.Server.ShutdownTimeout < 0 {
		return errors.New("server timeouts must not be negative")
	}

	switch strings.ToLower(c.Logging.Format) {
	case "json", "text":
	default:
		return fmt.Errorf("logging.format must be 'json' or 'text', got %q", c.Logging.Format)
	}

	for _, entry := range c.IPFilter.AllowList {
		if err := validateIPOrCIDR(entry); err != nil {
			return fmt.Errorf("ip_filter.allow_list: %w", err)
		}
	}
	for _, entry := range c.IPFilter.DenyList {
		if err := validateIPOrCIDR(entry); err != nil {
			return fmt.Errorf("ip_filter.deny_list: %w", err)
		}
	}

	if c.RateLimit.Enabled {
		if c.RateLimit.RequestsPerSec <= 0 {
			return errors.New("rate_limit.requests_per_sec must be > 0 when enabled")
		}
		if c.RateLimit.Burst < 1 {
			return errors.New("rate_limit.burst must be >= 1 when enabled")
		}
	}

	if len(c.Routes) == 0 {
		return errors.New("at least one route must be configured")
	}

	seenPrefixes := make(map[string]struct{}, len(c.Routes))
	for i, r := range c.Routes {
		if strings.TrimSpace(r.Name) == "" {
			return fmt.Errorf("route[%d]: name is required", i)
		}
		if !strings.HasPrefix(r.PathPrefix, "/") {
			return fmt.Errorf("route %q: path_prefix must start with '/'", r.Name)
		}
		if _, exists := seenPrefixes[r.PathPrefix]; exists {
			return fmt.Errorf("route %q: duplicate path_prefix %q", r.Name, r.PathPrefix)
		}
		seenPrefixes[r.PathPrefix] = struct{}{}

		if r.Strategy != StrategyRoundRobin && r.Strategy != StrategyLeastConn {
			return fmt.Errorf("route %q: unsupported strategy %q (must be %q or %q)",
				r.Name, r.Strategy, StrategyRoundRobin, StrategyLeastConn)
		}

		if len(r.Backends) == 0 {
			return fmt.Errorf("route %q: at least one backend url is required", r.Name)
		}

		for _, rawURL := range r.Backends {
			parsedURL, err := url.Parse(rawURL)
			if err != nil {
				return fmt.Errorf("route %q: invalid backend url %q: %w", r.Name, rawURL, err)
			}
			if parsedURL.Scheme != "http" && parsedURL.Scheme != "https" {
				return fmt.Errorf("route %q: backend url scheme must be http or https", r.Name)
			}
			if parsedURL.Host == "" {
				return fmt.Errorf("route %q: backend url host is required", r.Name)
			}
		}

		if r.Timeout < 0 || r.Retries < 0 || r.RetryBackoff < 0 {
			return fmt.Errorf("route %q: timeout, retries, and retry_backoff must not be negative", r.Name)
		}

		if r.HealthCheck.Path != "" && !strings.HasPrefix(r.HealthCheck.Path, "/") {
			return fmt.Errorf("route %q: health_check.path must start with '/'", r.Name)
		}
		if r.HealthCheck.UnhealthyThreshold < 1 || r.HealthCheck.HealthyThreshold < 1 {
			return fmt.Errorf("route %q: health_check thresholds must be >= 1", r.Name)
		}
	}

	return nil
}

func validateIPOrCIDR(raw string) error {
	if strings.Contains(raw, "/") {
		if _, err := netip.ParsePrefix(raw); err != nil {
			return fmt.Errorf("invalid CIDR %q: %w", raw, err)
		}
		return nil
	}
	if _, err := netip.ParseAddr(raw); err != nil {
		return fmt.Errorf("invalid IP address %q: %w", raw, err)
	}
	return nil
}
