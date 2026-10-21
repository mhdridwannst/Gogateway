package middleware

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"log/slog"
	"net/http"
	"time"

	"github.com/mhdridwannst/Gogateway/internal/telemetry"
)

type ctxKeyRequestID struct{}

// RequestIDHeader is the canonical HTTP header name for request correlation IDs.
const RequestIDHeader = "X-Request-ID"

// RequestIDFromContext retrieves the request ID string from ctx, if present.
func RequestIDFromContext(ctx context.Context) string {
	if v, ok := ctx.Value(ctxKeyRequestID{}).(string); ok {
		return v
	}
	return ""
}

// RequestID ensures every request has an X-Request-ID header and stores it in the request context.
func RequestID(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reqID := r.Header.Get(RequestIDHeader)
		if reqID == "" {
			reqID = generateRequestID()
			r.Header.Set(RequestIDHeader, reqID)
		}

		w.Header().Set(RequestIDHeader, reqID)
		ctx := context.WithValue(r.Context(), ctxKeyRequestID{}, reqID)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func generateRequestID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "fallback-req-id"
	}
	return hex.EncodeToString(b[:])
}

type obsResponseRecorder struct {
	http.ResponseWriter
	statusCode   int
	bytesWritten int
	written      bool
}

func (r *obsResponseRecorder) WriteHeader(code int) {
	if !r.written {
		r.statusCode = code
		r.written = true
	}
	r.ResponseWriter.WriteHeader(code)
}

func (r *obsResponseRecorder) Write(b []byte) (int, error) {
	if !r.written {
		r.statusCode = http.StatusOK
		r.written = true
	}
	n, err := r.ResponseWriter.Write(b)
	r.bytesWritten += n
	return n, err
}

// Observe records structured access logs via slog and Prometheus request metrics.
func Observe(logger *slog.Logger, metrics *telemetry.Metrics, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rec := &obsResponseRecorder{
			ResponseWriter: w,
			statusCode:     http.StatusOK,
		}

		next.ServeHTTP(rec, r)

		duration := time.Since(start)
		route := rec.Header().Get("X-Gateway-Route")
		if route == "" {
			route = "unmatched"
		}

		if metrics != nil {
			metrics.ObserveRequest(route, r.Method, rec.statusCode, duration)
		}

		if logger != nil {
			clientIP := r.RemoteAddr
			if addr, ok := extractClientAddr(r); ok {
				clientIP = addr.String()
			}

			logger.Info("http_request",
				slog.String("request_id", RequestIDFromContext(r.Context())),
				slog.String("method", r.Method),
				slog.String("path", r.URL.Path),
				slog.String("route", route),
				slog.Int("status", rec.statusCode),
				slog.Int("bytes", rec.bytesWritten),
				slog.Float64("duration_ms", float64(duration.Microseconds())/1000.0),
				slog.String("client_ip", clientIP),
			)
		}
	})
}
