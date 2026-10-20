package config

import (
	"errors"
	"fmt"
	"net/url"
	"os"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// Config represents the root configuration for the gateway.
type Config struct {
	Server ServerConfig  `yaml:"server"`
	Routes []RouteConfig `yaml:"routes"`
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

// RouteConfig defines how an incoming request path maps to an upstream backend.
type RouteConfig struct {
	Name        string `yaml:"name"`
	PathPrefix  string `yaml:"path_prefix"`
	TargetURL   string `yaml:"target_url"`
	StripPrefix bool   `yaml:"strip_prefix"`
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
}

// Validate checks that all required configuration fields are present and sane.
func (c *Config) Validate() error {
	if c.Server.ReadTimeout < 0 || c.Server.WriteTimeout < 0 || c.Server.IdleTimeout < 0 ||
		c.Server.ReadHeaderTimeout < 0 || c.Server.ShutdownTimeout < 0 {
		return errors.New("server timeouts must not be negative")
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

		parsedURL, err := url.Parse(r.TargetURL)
		if err != nil {
			return fmt.Errorf("route %q: invalid target_url: %w", r.Name, err)
		}
		if parsedURL.Scheme != "http" && parsedURL.Scheme != "https" {
			return fmt.Errorf("route %q: target_url scheme must be http or https", r.Name)
		}
		if parsedURL.Host == "" {
			return fmt.Errorf("route %q: target_url host is required", r.Name)
		}
	}

	return nil
}
