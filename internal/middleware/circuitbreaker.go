package middleware

import (
	"net/http"
	"sync"
	"time"
)

// CircuitState represents the current state of a CircuitBreaker.
type CircuitState int

const (
	StateClosed CircuitState = iota
	StateOpen
	StateHalfOpen
)

// String returns a human-readable representation of the circuit state.
func (s CircuitState) String() string {
	switch s {
	case StateClosed:
		return "closed"
	case StateOpen:
		return "open"
	case StateHalfOpen:
		return "half-open"
	default:
		return "unknown"
	}
}

// CircuitBreaker protects upstream services from cascading failures.
type CircuitBreaker struct {
	mu               sync.Mutex
	state            CircuitState
	failures         int
	successes        int
	halfOpenInFlight int
	openedAt         time.Time
	failureThreshold int
	resetTimeout     time.Duration
	halfOpenMaxReqs  int
}

// NewCircuitBreaker creates a CircuitBreaker initialized in the Closed state.
func NewCircuitBreaker(failureThreshold int, resetTimeout time.Duration, halfOpenMaxReqs int) *CircuitBreaker {
	if failureThreshold < 1 {
		failureThreshold = 5
	}
	if resetTimeout <= 0 {
		resetTimeout = 10 * time.Second
	}
	if halfOpenMaxReqs < 1 {
		halfOpenMaxReqs = 1
	}
	return &CircuitBreaker{
		state:            StateClosed,
		failureThreshold: failureThreshold,
		resetTimeout:     resetTimeout,
		halfOpenMaxReqs:  halfOpenMaxReqs,
	}
}

// State returns the current state of the circuit breaker.
func (cb *CircuitBreaker) State() CircuitState {
	cb.mu.Lock()
	defer cb.mu.Unlock()

	if cb.state == StateOpen && time.Since(cb.openedAt) >= cb.resetTimeout {
		return StateHalfOpen
	}
	return cb.state
}

// Allow checks whether a request is permitted to proceed to the upstream backend.
func (cb *CircuitBreaker) Allow() bool {
	cb.mu.Lock()
	defer cb.mu.Unlock()

	switch cb.state {
	case StateClosed:
		return true
	case StateOpen:
		if time.Since(cb.openedAt) >= cb.resetTimeout {
			cb.state = StateHalfOpen
			cb.halfOpenInFlight = 1
			cb.successes = 0
			cb.failures = 0
			return true
		}
		return false
	case StateHalfOpen:
		if cb.halfOpenInFlight < cb.halfOpenMaxReqs {
			cb.halfOpenInFlight++
			return true
		}
		return false
	default:
		return false
	}
}

// RecordResult updates the circuit breaker state based on the upstream HTTP status code.
func (cb *CircuitBreaker) RecordResult(statusCode int) {
	cb.mu.Lock()
	defer cb.mu.Unlock()

	isFailure := statusCode >= http.StatusInternalServerError

	switch cb.state {
	case StateClosed:
		if isFailure {
			cb.failures++
			if cb.failures >= cb.failureThreshold {
				cb.state = StateOpen
				cb.openedAt = time.Now()
			}
		} else {
			cb.failures = 0
		}

	case StateHalfOpen:
		if cb.halfOpenInFlight > 0 {
			cb.halfOpenInFlight--
		}
		if isFailure {
			cb.state = StateOpen
			cb.openedAt = time.Now()
			cb.successes = 0
		} else {
			cb.successes++
			if cb.successes >= cb.halfOpenMaxReqs {
				cb.state = StateClosed
				cb.failures = 0
				cb.successes = 0
			}
		}
	}
}

type statusRecorder struct {
	http.ResponseWriter
	statusCode int
	written    bool
}

func (sr *statusRecorder) WriteHeader(code int) {
	if !sr.written {
		sr.statusCode = code
		sr.written = true
	}
	sr.ResponseWriter.WriteHeader(code)
}

func (sr *statusRecorder) Write(b []byte) (int, error) {
	if !sr.written {
		sr.statusCode = http.StatusOK
		sr.written = true
	}
	return sr.ResponseWriter.Write(b)
}

// Wrap wraps an http.Handler with circuit breaker protection.
func (cb *CircuitBreaker) Wrap(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !cb.Allow() {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = w.Write([]byte(`{"error":"service unavailable","message":"circuit breaker is open"}` + "\n"))
			return
		}

		rec := &statusRecorder{ResponseWriter: w, statusCode: http.StatusOK}
		next.ServeHTTP(rec, r)
		cb.RecordResult(rec.statusCode)
	})
}
