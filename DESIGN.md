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

## Phase 3: Resilience and Protection (Rate Limiting, Circuit Breaker, Retries, IP Filter)

### 6. Lazy Token-Bucket Refill with Background TTL Eviction

- **Decision**: Compute token refills lazily on each incoming request using elapsed time ($\Delta t \times \text{rate}$) protected by a `sync.Mutex`, paired with a single background janitor goroutine that evicts idle client IPs after `client_ttl`.
- **Rationale**: Spawning a `time.Ticker` goroutine per client IP would allow an attacker to exhaust server memory and scheduler resources via IP spoofing. Lazy math calculation requires zero background timers per IP, while periodic TTL eviction prevents unbounded map growth.

### 7. Idempotent-Only Transport Retries with Exponential Backoff

- **Decision**: Implement retries inside a custom `http.RoundTripper` (`retryTransport`) that only retries requests when a network/transport error occurs AND the HTTP method is idempotent (`GET`, `HEAD`, `OPTIONS`, `TRACE`, `PUT`, `DELETE`) with an empty body.
- **Rationale**: Retrying non-idempotent requests like `POST` after a mid-flight network drop risks duplicate side effects (e.g., double-charging a payment). Exponential backoff ($50\text{ms} \times 2^{\text{attempt}-1}$) prevents retry storms against struggling backends.

### 8. Per-Route Three-State Circuit Breaker

- **Decision**: Wrap each route handler with a state machine (`Closed` $\rightarrow$ `Open` $\rightarrow$ `Half-Open` $\rightarrow$ `Closed`) that trips when consecutive `5xx` responses reach `failure_threshold`.
- **Rationale**: While passive health checks isolate individual bad nodes in a pool, a route-level circuit breaker protects the entire upstream service (e.g., when a shared database goes down across all nodes) by failing fast with `503 Service Unavailable` and bounding concurrent probe traffic during recovery (`half_open_max_requests`).

## Phase 4: Observability (Prometheus, Structured Logging, Request ID, Admin Server)

### 9. Isolated Prometheus Registry (`prometheus.NewRegistry()`)

- **Decision**: Instantiate a custom `*prometheus.Registry` in `internal/telemetry` instead of registering collectors on `prometheus.DefaultRegisterer`.
- **Rationale**: Using global mutable state causes panics (`duplicate metrics collector registration`) when multiple gateway instances are initialized across unit tests. A dedicated registry enforces clean dependency injection.

### 10. Dedicated Admin Listener (`:9091`)

- **Decision**: Serve `/healthz`, `/backends`, and `/metrics` on a separate `http.Server` (`admin.listen_addr`) rather than mounting them on the public proxy router (`:8080`).
- **Rationale**: Exposing internal backend topology (`/backends`) and Prometheus metrics (`/metrics`) on the public ingress port is an information-disclosure risk. Separating the port allows operators to restrict access via VPC security groups or Kubernetes NetworkPolicies.

### 11. Standard Library `log/slog` with Context-Propagated Request IDs

- **Decision**: Use Go's built-in `log/slog` package paired with a 16-byte `crypto/rand` hex `X-Request-ID` stored in `context.Context` via an unexported struct key (`ctxKeyRequestID{}`).
- **Rationale**: Provides structured JSON logs and correlation IDs out of the box with zero third-party logging or UUID dependencies.

## Phase 5: Delivery, Containerization, and Kubernetes Readiness

### 12. Static Binary Multi-Stage Build with Non-Root Execution

- **Decision**: Compile with `CGO_ENABLED=0` and `-ldflags="-s -w"` in a `golang:1.23-alpine` builder stage, copying only the binary into a minimal `alpine:3.20` runtime running as UID/GID `10001:10001`.
- **Rationale**: Eliminates compiler toolchains from the production image (reducing attack surface and image size to ~20 MB) and prevents container breakout privilege escalation by enforcing `runAsNonRoot` and `readOnlyRootFilesystem` in Kubernetes.
