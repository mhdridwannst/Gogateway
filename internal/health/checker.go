package health

import (
	"context"
	"io"
	"log"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/mhdridwannst/Gogateway/internal/balancer"
)

// TargetGroup groups a route's backends with its health check settings.
type TargetGroup struct {
	RouteName  string
	HealthPath string
	Interval   time.Duration
	Timeout    time.Duration
	Backends   []*balancer.Backend
}

// Checker manages background active health probing for all configured route groups.
type Checker struct {
	groups []TargetGroup
}

// NewChecker creates an active health checker for the given target groups.
func NewChecker(groups []TargetGroup) *Checker {
	return &Checker{groups: groups}
}

// Start launches background health check loops for all groups that have a HealthPath configured.
// All spawned goroutines exit cleanly when ctx is canceled.
func (c *Checker) Start(ctx context.Context) {
	for _, group := range c.groups {
		if group.HealthPath == "" {
			continue
		}
		g := group
		go c.runGroupLoop(ctx, g)
	}
}

func (c *Checker) runGroupLoop(ctx context.Context, group TargetGroup) {
	client := &http.Client{
		Timeout: group.Timeout,
	}

	ticker := time.NewTicker(group.Interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			c.probeGroup(ctx, client, group)
		}
	}
}

func (c *Checker) probeGroup(ctx context.Context, client *http.Client, group TargetGroup) {
	var wg sync.WaitGroup
	for _, b := range group.Backends {
		wg.Add(1)
		go func(backend *balancer.Backend) {
			defer wg.Done()
			c.probeBackend(ctx, client, group.RouteName, group.HealthPath, backend)
		}(b)
	}
	wg.Wait()
}

func (c *Checker) probeBackend(ctx context.Context, client *http.Client, routeName, healthPath string, b *balancer.Backend) {
	probeURL := joinHealthURL(b.URL(), healthPath)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, probeURL, nil)
	if err != nil {
		return
	}

	resp, err := client.Do(req)
	if err != nil {
		if b.RecordFailure() {
			log.Printf("[WARN] active health check: backend %s on route %q marked UNHEALTHY (%v)",
				b.URL().String(), routeName, err)
		}
		return
	}
	defer func() {
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
	}()

	if resp.StatusCode >= 200 && resp.StatusCode < 400 {
		if b.RecordSuccess() {
			log.Printf("[INFO] active health check: backend %s on route %q recovered and marked HEALTHY",
				b.URL().String(), routeName)
		}
		return
	}

	if b.RecordFailure() {
		log.Printf("[WARN] active health check: backend %s on route %q marked UNHEALTHY (status %d)",
			b.URL().String(), routeName, resp.StatusCode)
	}
}

func joinHealthURL(base *url.URL, healthPath string) string {
	u := *base
	if !strings.HasPrefix(healthPath, "/") {
		healthPath = "/" + healthPath
	}
	u.Path = strings.TrimSuffix(base.Path, "/") + healthPath
	return u.String()
}
