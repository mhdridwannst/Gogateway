package proxy

import (
	"net/http"
	"time"
)

// retryTransport wraps an http.RoundTripper to retry idempotent requests on network failure.
type retryTransport struct {
	base       http.RoundTripper
	maxRetries int
	backoff    time.Duration
}

func newRetryTransport(base http.RoundTripper, maxRetries int, backoff time.Duration) http.RoundTripper {
	if base == nil {
		base = http.DefaultTransport
	}
	if maxRetries <= 0 {
		return base
	}
	if backoff <= 0 {
		backoff = 50 * time.Millisecond
	}
	return &retryTransport{
		base:       base,
		maxRetries: maxRetries,
		backoff:    backoff,
	}
}

func (rt *retryTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	attempts := 1
	if isIdempotent(req.Method) && (req.Body == nil || req.Body == http.NoBody) {
		attempts += rt.maxRetries
	}

	var lastErr error
	for attempt := 0; attempt < attempts; attempt++ {
		if attempt > 0 {
			// Exponential backoff: backoff * 2^(attempt-1)
			wait := rt.backoff * time.Duration(1<<uint(attempt-1))
			timer := time.NewTimer(wait)
			select {
			case <-req.Context().Done():
				timer.Stop()
				return nil, req.Context().Err()
			case <-timer.C:
			}
		}

		resp, err := rt.base.RoundTrip(req)
		if err == nil {
			return resp, nil
		}
		lastErr = err

		// Do not retry if the request context itself was canceled or timed out.
		if req.Context().Err() != nil {
			return nil, req.Context().Err()
		}
	}

	return nil, lastErr
}

func isIdempotent(method string) bool {
	switch method {
	case http.MethodGet, http.MethodHead, http.MethodOptions, http.MethodTrace, http.MethodPut, http.MethodDelete:
		return true
	default:
		return false
	}
}
