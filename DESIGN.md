# Architecture Decision Records (DESIGN.md)

## Phase 1: Core Reverse Proxy, Config, and Lifecycle

### 1. `httputil.ReverseProxy.Rewrite` over `Director`

- **Decision**: Use the `Rewrite(*httputil.ProxyRequest)` hook introduced in Go 1.20 instead of the legacy `Director` function.
- **Rationale**: `Director` mutates the incoming request in-place and is prone to security issues where client-supplied `X-Forwarded-For` headers are trusted or hop-by-hop headers strip internal proxy headers. `Rewrite` cleanly separates `pr.In` (immutable incoming request) and `pr.Out` (outbound request), safely handling `X-Forwarded-*` headers via `pr.SetURL` and `pr.SetXForwarded`.

### 2. Longest-Prefix Linear Routing

- **Decision**: Sort configured routes by `len(PathPrefix)` in descending order at startup and match sequentially on clean `/` path boundaries.
- **Trade-offs**: $O(N)$ lookup per request where $N$ is the number of routes. For small-to-medium configs (<100 routes), slice iteration is cache-friendly and avoids complex tree allocations. If route counts scale into the thousands, we should replace this with a Radix Tree.

### 3. Fail-Fast Strict YAML Parsing

- **Decision**: Enable `decoder.KnownFields(true)` in `gopkg.in/yaml.v3`.
- **Rationale**: In production infrastructure, silent typos in configuration keys (e.g., `read_timout: 5s`) can lead to dangerous default behaviors. Rejecting unknown fields forces operators to fix configuration mistakes before startup.
