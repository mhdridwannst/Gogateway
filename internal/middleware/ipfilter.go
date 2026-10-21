package middleware

import (
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"strings"
)

// IPFilter enforces IP allowlist and denylist rules on incoming HTTP requests.
type IPFilter struct {
	allowPrefixes []netip.Prefix
	denyPrefixes  []netip.Prefix
}

// NewIPFilter parses raw IP or CIDR strings into netip.Prefix slices.
func NewIPFilter(allowList, denyList []string) (*IPFilter, error) {
	allow, err := parsePrefixes(allowList)
	if err != nil {
		return nil, fmt.Errorf("parsing allow list: %w", err)
	}
	deny, err := parsePrefixes(denyList)
	if err != nil {
		return nil, fmt.Errorf("parsing deny list: %w", err)
	}
	return &IPFilter{
		allowPrefixes: allow,
		denyPrefixes:  deny,
	}, nil
}

func parsePrefixes(rawList []string) ([]netip.Prefix, error) {
	prefixes := make([]netip.Prefix, 0, len(rawList))
	for _, raw := range rawList {
		raw = strings.TrimSpace(raw)
		if raw == "" {
			continue
		}
		if strings.Contains(raw, "/") {
			p, err := netip.ParsePrefix(raw)
			if err != nil {
				return nil, err
			}
			prefixes = append(prefixes, p.Masked())
		} else {
			addr, err := netip.ParseAddr(raw)
			if err != nil {
				return nil, err
			}
			bits := 32
			if addr.Is6() {
				bits = 128
			}
			prefixes = append(prefixes, netip.PrefixFrom(addr, bits))
		}
	}
	return prefixes, nil
}

// IsAllowed checks if the given IP address is permitted by the filter rules.
// Deny rules always take precedence over allow rules.
func (f *IPFilter) IsAllowed(addr netip.Addr) bool {
	addr = addr.Unmap() // Normalize IPv4-mapped IPv6 addresses (e.g., ::ffff:127.0.0.1 -> 127.0.0.1)

	for _, p := range f.denyPrefixes {
		if p.Contains(addr) {
			return false
		}
	}

	if len(f.allowPrefixes) > 0 {
		for _, p := range f.allowPrefixes {
			if p.Contains(addr) {
				return true
			}
		}
		return false
	}

	return true
}

// Middleware wraps an http.Handler and rejects disallowed client IPs with 403 Forbidden.
func (f *IPFilter) Middleware(next http.Handler) http.Handler {
	if len(f.allowPrefixes) == 0 && len(f.denyPrefixes) == 0 {
		return next
	}

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		addr, ok := extractClientAddr(r)
		if !ok || !f.IsAllowed(addr) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusForbidden)
			_, _ = w.Write([]byte(`{"error":"forbidden","message":"client IP is not allowed"}` + "\n"))
			return
		}
		next.ServeHTTP(w, r)
	})
}

func extractClientAddr(r *http.Request) (netip.Addr, bool) {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	addr, err := netip.ParseAddr(strings.TrimSpace(host))
	if err != nil {
		return netip.Addr{}, false
	}
	return addr.Unmap(), true
}
