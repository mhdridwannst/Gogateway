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

## Phase 2: Load Balancing and Active/Passive Health Checks

### 4. Lock-Free Backend State with `sync/atomic`

- **Decision**: Use `atomic.Bool`, `atomic.Int64`, and `atomic.Uint64` for backend health states, active connection counters, and round-robin indices instead of `sync.Mutex` or `sync.RWMutex`.
- **Rationale**: Load balancer selection (`Next()`) sits on the critical hot path of every HTTP request. Lock-free atomic operations avoid mutex contention across CPU cores, achieving ~18.5 ns/op under parallel benchmarks.
- **Trade-offs**: In `LeastConnections.Next()`, reading `ActiveConnections()` across multiple backends is a point-in-time snapshot rather than a globally locked transaction. Under extreme concurrency, two goroutines might briefly pick the same backend, which we mitigate by starting each scan from an atomically incremented round-robin offset.

### 5. Dual Health Checking (Active Probing + Passive Observation)

- **Decision**: Combine periodic background HTTP `GET` probes (`internal/health`) with real-time passive failure detection in `ReverseProxy.ModifyResponse` (HTTP `5xx`) and `ReverseProxy.ErrorHandler` (network/dial failures).
- **Rationale**: Active checks alone have a detection blind spot between probe intervals (e.g., up to 5-10 seconds of dropped traffic if a backend crashes right after a probe). Passive checks trip the backend to unhealthy immediately after `unhealthy_threshold` consecutive live failures, while active probes allow the backend to automatically recover once it starts returning `2xx/3xx` responses again.
