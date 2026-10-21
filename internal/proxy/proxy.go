package proxy

import (
	"context"
	"fmt"
	"log"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/mhdridwannst/Gogateway/internal/balancer"
	"github.com/mhdridwannst/Gogateway/internal/config"
	"github.com/mhdridwannst/Gogateway/internal/health"
	"github.com/mhdridwannst/Gogateway/internal/middleware"
)

type routeEntry struct {
	name        string
	pathPrefix  string
	stripPrefix bool
	timeout     time.Duration
	lb          balancer.Balancer
	handler     http.Handler
	healthCfg   config.HealthCheckConfig
}

// Gateway is the core Layer-7 HTTP reverse proxy and load balancer router.
type Gateway struct {
	routes []routeEntry
}

// New builds a new Gateway instance from the validated configuration.
func New(cfg *config.Config) (*Gateway, error) {
	entries := make([]routeEntry, 0, len(cfg.Routes))

	baseTransport := &http.Transport{
		Proxy: http.ProxyFromEnvironment,
		DialContext: (&net.Dialer{
			Timeout:   3 * time.Second,
			KeepAlive: 30 * time.Second,
		}).DialContext,
		MaxIdleConns:          100,
		MaxIdleConnsPerHost:   20,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   5 * time.Second,
		ExpectContinueTimeout: 1 * time.Second,
	}

	for _, rc := range cfg.Routes {
		rawURLs := rc.Backends
		if len(rawURLs) == 0 && rc.TargetURL != "" {
			rawURLs = []string{rc.TargetURL}
		}

		rtTransport := newRetryTransport(baseTransport, rc.Retries, rc.RetryBackoff)
		backends := make([]*balancer.Backend, 0, len(rawURLs))

		for _, rawURL := range rawURLs {
			targetURL, err := url.Parse(rawURL)
			if err != nil {
				return nil, fmt.Errorf("parsing backend url %q for route %q: %w", rawURL, rc.Name, err)
			}

			var b *balancer.Backend
			rp := newReverseProxy(rc, targetURL, rtTransport, func() *balancer.Backend {
				return b
			})
			b = balancer.NewBackend(
				targetURL,
				rp,
				rc.HealthCheck.UnhealthyThreshold,
				rc.HealthCheck.HealthyThreshold,
			)
			backends = append(backends, b)
		}

		var lb balancer.Balancer
		switch rc.Strategy {
		case config.StrategyLeastConn:
			lb = balancer.NewLeastConnections(backends)
		default:
			lb = balancer.NewRoundRobin(backends)
		}

		var routeHandler http.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			backend := lb.Next()
			if backend == nil {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusServiceUnavailable)
				_, _ = w.Write([]byte(`{"error":"service unavailable","message":"no healthy upstream backends available"}` + "\n"))
				return
			}
			backend.ServeHTTP(w, r)
		})

		if rc.CircuitBreaker.Enabled {
			cb := middleware.NewCircuitBreaker(
				rc.CircuitBreaker.FailureThreshold,
				rc.CircuitBreaker.ResetTimeout,
				rc.CircuitBreaker.HalfOpenMaxReqs,
			)
			routeHandler = cb.Wrap(routeHandler)
		}

		entries = append(entries, routeEntry{
			name:        rc.Name,
			pathPrefix:  rc.PathPrefix,
			stripPrefix: rc.StripPrefix,
			timeout:     rc.Timeout,
			lb:          lb,
			handler:     routeHandler,
			healthCfg:   rc.HealthCheck,
		})
	}

	sort.Slice(entries, func(i, j int) bool {
		return len(entries[i].pathPrefix) > len(entries[j].pathPrefix)
	})

	return &Gateway{routes: entries}, nil
}

// HealthTargetGroups exports all route backend pools for active health checking.
func (g *Gateway) HealthTargetGroups() []health.TargetGroup {
	groups := make([]health.TargetGroup, 0, len(g.routes))
	for _, rt := range g.routes {
		groups = append(groups, health.TargetGroup{
			RouteName:  rt.name,
			HealthPath: rt.healthCfg.Path,
			Interval:   rt.healthCfg.Interval,
			Timeout:    rt.healthCfg.Timeout,
			Backends:   rt.lb.Backends(),
		})
	}
	return groups
}

func newReverseProxy(rc config.RouteConfig, target *url.URL, transport http.RoundTripper, getBackend func() *balancer.Backend) *httputil.ReverseProxy {
	return &httputil.ReverseProxy{
		Transport: transport,
		Rewrite: func(pr *httputil.ProxyRequest) {
			pr.SetURL(target)
			pr.SetXForwarded()
			pr.Out.Host = target.Host

			if rc.StripPrefix {
				stripped := strings.TrimPrefix(pr.In.URL.Path, rc.PathPrefix)
				if !strings.HasPrefix(stripped, "/") {
					stripped = "/" + stripped
				}
				pr.Out.URL.Path = singleJoiningSlash(target.Path, stripped)
				pr.Out.URL.RawPath = ""
			}
		},
		ModifyResponse: func(resp *http.Response) error {
			resp.Header.Set("X-Gateway-Route", rc.Name)
			resp.Header.Set("X-Gateway-Backend", target.String())

			if b := getBackend(); b != nil {
				if resp.StatusCode >= http.StatusInternalServerError {
					if b.RecordFailure() {
						log.Printf("[WARN] passive health check: backend %s on route %q marked UNHEALTHY (status %d)",
							target.String(), rc.Name, resp.StatusCode)
					}
				} else {
					b.RecordSuccess()
				}
			}

			return nil
		},
		ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
			if b := getBackend(); b != nil {
				if b.RecordFailure() {
					log.Printf("[WARN] passive health check: backend %s on route %q marked UNHEALTHY (%v)",
						target.String(), rc.Name, err)
				}
			}

			log.Printf("[ERROR] proxy error on route %q -> %s (%s %s): %v",
				rc.Name, target.String(), r.Method, r.URL.Path, err)
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadGateway)
			_, _ = w.Write([]byte(`{"error":"bad gateway","message":"upstream server is unreachable"}` + "\n"))
		},
	}
}

// ServeHTTP implements the http.Handler interface.
func (g *Gateway) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	for _, rt := range g.routes {
		if matchesPrefix(r.URL.Path, rt.pathPrefix) {
			if rt.timeout > 0 {
				ctx, cancel := context.WithTimeout(r.Context(), rt.timeout)
				defer cancel()
				r = r.WithContext(ctx)
			}
			rt.handler.ServeHTTP(w, r)
			return
		}
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusNotFound)
	_, _ = w.Write([]byte(`{"error":"not found","message":"no matching route found"}` + "\n"))
}

func matchesPrefix(requestPath, prefix string) bool {
	if prefix == "/" {
		return true
	}
	normalizedPrefix := strings.TrimSuffix(prefix, "/")
	if requestPath == normalizedPrefix {
		return true
	}
	return strings.HasPrefix(requestPath, normalizedPrefix+"/")
}

func singleJoiningSlash(a, b string) string {
	aslash := strings.HasSuffix(a, "/")
	bslash := strings.HasPrefix(b, "/")
	switch {
	case aslash && bslash:
		return a + b[1:]
	case !aslash && !bslash:
		return a + "/" + b
	}
	return a + b
}
