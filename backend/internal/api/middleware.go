package api

import (
	"bufio"
	"context"
	"mime"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/rs/zerolog/log"
)

type contextKey string

const RequestIDKey contextKey = "request_id"

// RequestIDMiddleware injects a unique request ID into every request context
// and sets it in the response header for distributed tracing.
func RequestIDMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := r.Header.Get("X-Request-ID")
		if id == "" {
			id = uuid.New().String()
		}
		w.Header().Set("X-Request-ID", id)
		ctx := context.WithValue(r.Context(), RequestIDKey, id)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// LoggingMiddleware records method, path, status, and latency for every request.
type statusRecorder struct {
	http.ResponseWriter
	status int
	bytes  int
}

func (r *statusRecorder) WriteHeader(status int) {
	r.status = status
	r.ResponseWriter.WriteHeader(status)
}

func (r *statusRecorder) Write(b []byte) (int, error) {
	n, err := r.ResponseWriter.Write(b)
	r.bytes += n
	return n, err
}

// Forward the Hijacker interface; Hijack (required for WebSockets)
func (r *statusRecorder) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	hj, ok := r.ResponseWriter.(http.Hijacker)
	if !ok {
		return nil, nil, http.ErrNotSupported
	}
	return hj.Hijack()
}

// Flush implements http.Flusher for streaming
func (r *statusRecorder) Flush() {
	if f, ok := r.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// HTTP/2 server push
func (r *statusRecorder) Push(target string, opts *http.PushOptions) error {
	if p, ok := r.ResponseWriter.(http.Pusher); ok {
		return p.Push(target, opts)
	}
	return http.ErrNotSupported
}

func LoggingMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()

		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}

		next.ServeHTTP(rec, r)

		requestID, _ := r.Context().Value(RequestIDKey).(string)

		log.Info().
			Str("method", r.Method).
			Str("path", r.URL.Path).
			Int("status", rec.status).
			Int("bytes", rec.bytes).
			Dur("latency", time.Since(start)).
			Str("request_id", requestID).
			Str("remote", r.RemoteAddr).
			Msg("http")
	})
}

// RecoveryMiddleware catches panics in HTTP handlers and returns 500.
func RecoveryMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if err := recover(); err != nil {
				requestID, _ := r.Context().Value(RequestIDKey).(string)
				log.Error().
					Interface("panic", err).
					Str("path", r.URL.Path).
					Str("request_id", requestID).
					Msg("handler panic recovered")
				http.Error(w, `{"error":"internal server error"}`, http.StatusInternalServerError)
			}
		}()
		next.ServeHTTP(w, r)
	})
}

// originChecker decides whether a browser Origin may talk to the API: it must be
// allowlisted, or match the request's own Host on a trusted hostname. Host alone
// is attacker-controlled under DNS rebinding, so same-origin also needs a trusted host.
type originChecker struct {
	origins map[string]bool
	hosts   map[string]bool
}

func newOriginChecker(allowed []string) originChecker {
	c := originChecker{
		origins: map[string]bool{},
		hosts:   map[string]bool{"localhost": true, "127.0.0.1": true, "::1": true},
	}
	for _, o := range allowed {
		if o = strings.TrimSpace(o); o == "" {
			continue
		}
		c.origins[o] = true
		if u, err := url.Parse(o); err == nil && u.Hostname() != "" {
			c.hosts[strings.ToLower(u.Hostname())] = true
		}
	}
	return c
}

func (c originChecker) allows(r *http.Request) bool {
	origin := r.Header.Get("Origin")
	if origin == "" || c.origins[origin] {
		return true
	}
	u, err := url.Parse(origin)
	if err != nil || u.Host == "" || !strings.EqualFold(u.Host, r.Host) {
		return false
	}
	return c.hosts[strings.ToLower(u.Hostname())]
}

// OriginPolicy enforces the origin allowlist (plus same-origin) and requires a
// JSON Content-Type on mutating requests, so cross-site pages cannot drive the API.
func OriginPolicy(allowed []string) func(http.Handler) http.Handler {
	checker := newOriginChecker(allowed)
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			origin := r.Header.Get("Origin")
			ok := checker.allows(r)
			mutating := r.Method == http.MethodPost || r.Method == http.MethodPut ||
				r.Method == http.MethodPatch || r.Method == http.MethodDelete

			switch {
			case !ok && (r.Method == http.MethodOptions || mutating):
				writeJSON(w, http.StatusForbidden, map[string]string{"error": "origin not allowed"})
				return
			case ok && origin != "":
				h := w.Header()
				h.Set("Access-Control-Allow-Origin", origin)
				h.Add("Vary", "Origin")
				h.Set("Access-Control-Allow-Methods", "GET, POST, PUT, PATCH, DELETE, OPTIONS")
				h.Set("Access-Control-Allow-Headers", "Content-Type, Authorization, X-Request-ID")
				h.Set("Access-Control-Expose-Headers", "X-Request-ID")
				h.Set("Access-Control-Max-Age", "86400")
			}

			if ok && r.Method == http.MethodOptions {
				w.WriteHeader(http.StatusNoContent)
				return
			}
			if mutating {
				mt, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
				if err != nil || mt != "application/json" {
					writeJSON(w, http.StatusUnsupportedMediaType, map[string]string{"error": "content type must be application/json"})
					return
				}
			}
			next.ServeHTTP(w, r)
		})
	}
}

// RateLimiter provides a simple token bucket per IP for DOS protection.
type RateLimiter struct {
	mu      sync.Mutex
	clients map[string]*tokenBucket
	rate    int           // requests per window
	window  time.Duration // time window
	cleanup time.Duration
}

type tokenBucket struct {
	tokens    int
	lastReset time.Time
}

func NewRateLimiter(rate int, window time.Duration) *RateLimiter {
	rl := &RateLimiter{
		clients: make(map[string]*tokenBucket),
		rate:    rate,
		window:  window,
		cleanup: 5 * time.Minute,
	}
	go rl.cleanupLoop()
	return rl
}

func (rl *RateLimiter) Allow(ip string) bool {
	rl.mu.Lock()
	defer rl.mu.Unlock()

	bucket, ok := rl.clients[ip]
	if !ok {
		rl.clients[ip] = &tokenBucket{tokens: rl.rate - 1, lastReset: time.Now()}
		return true
	}

	if time.Since(bucket.lastReset) >= rl.window {
		bucket.tokens = rl.rate
		bucket.lastReset = time.Now()
	}

	if bucket.tokens <= 0 {
		return false
	}
	bucket.tokens--
	return true
}

func (rl *RateLimiter) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ip := r.RemoteAddr
		if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
			ip = xff
		}

		if !rl.Allow(ip) {
			w.Header().Set("Retry-After", "1")
			http.Error(w, `{"error":"rate limit exceeded"}`, http.StatusTooManyRequests)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (rl *RateLimiter) cleanupLoop() {
	ticker := time.NewTicker(rl.cleanup)
	defer ticker.Stop()
	for range ticker.C {
		rl.mu.Lock()
		for ip, bucket := range rl.clients {
			if time.Since(bucket.lastReset) > rl.cleanup {
				delete(rl.clients, ip)
			}
		}
		rl.mu.Unlock()
	}
}
