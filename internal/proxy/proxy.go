package proxy

import (
	"fmt"
	"log"
	"net/http"
	"net/http/httputil"
	"net/url"
	"sort"
	"strings"

	"github.com/mhdridwannst/Gogateway/internal/config"
)

type routeEntry struct {
	name        string
	pathPrefix  string
	stripPrefix bool
	proxy       *httputil.ReverseProxy
}

// Gateway is the core Layer-7 HTTP reverse proxy router.
type Gateway struct {
	routes []routeEntry
}

// New builds a new Gateway instance from the validated configuration.
func New(cfg *config.Config) (*Gateway, error) {
	entries := make([]routeEntry, 0, len(cfg.Routes))

	for _, rc := range cfg.Routes {
		targetURL, err := url.Parse(rc.TargetURL)
		if err != nil {
			return nil, fmt.Errorf("parsing target url for route %q: %w", rc.Name, err)
		}

		rp := newReverseProxy(rc, targetURL)

		entries = append(entries, routeEntry{
			name:        rc.Name,
			pathPrefix:  rc.PathPrefix,
			stripPrefix: rc.StripPrefix,
			proxy:       rp,
		})
	}

	sort.Slice(entries, func(i, j int) bool {
		return len(entries[i].pathPrefix) > len(entries[j].pathPrefix)
	})

	return &Gateway{routes: entries}, nil
}

func newReverseProxy(rc config.RouteConfig, target *url.URL) *httputil.ReverseProxy {
	return &httputil.ReverseProxy{
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
			return nil
		},
		ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
			log.Printf("[ERROR] proxy error on route %q (%s %s): %v", rc.Name, r.Method, r.URL.Path, err)
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
			rt.proxy.ServeHTTP(w, r)
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
