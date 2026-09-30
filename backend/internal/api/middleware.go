package api

import (
	"bufio"
	"context"
	"fmt"
	"math"
	"mime"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
)

type contextKey string

// logFrom returns the request-scoped logger; outside the middleware chain it
// falls back to the global logger rather than zerolog's disabled default.
func logFrom(r *http.Request) *zerolog.Logger {
	if l := zerolog.Ctx(r.Context()); l.GetLevel() != zerolog.Disabled {
		return l
	}
	return &log.Logger
}

const RequestIDKey contextKey = "request_id"

// validRequestID keeps client-supplied ids to a short, log-safe charset so a
// caller cannot forge log fields or bloat log lines through the header.
var validRequestID = regexp.MustCompile(`^[A-Za-z0-9._-]{1,64}$`)

// RequestIDMiddleware injects a request ID into every request context, sets it
// in the response header, and attaches a logger carrying it. A well-formed
// inbound X-Request-ID is kept for cross-service correlation; anything else is
// replaced. Must be outermost so every later layer logs with the ID.
func RequestIDMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := r.Header.Get("X-Request-ID")
		if !validRequestID.MatchString(id) {
			id = uuid.New().String()
		}
		w.Header().Set("X-Request-ID", id)
		logger := log.Logger.With().
			Str("request_id", id).
			Str("method", r.Method).
			Str("path", r.URL.Path).
			Logger()
		ctx := context.WithValue(r.Context(), RequestIDKey, id)
		next.ServeHTTP(w, r.WithContext(logger.WithContext(ctx)))
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

		logFrom(r).Info().
			Int("status", rec.status).
			Int("bytes", rec.bytes).
			Dur("latency", time.Since(start)).
			Str("remote", r.RemoteAddr).
			Msg("http")
	})
}

// RecoveryMiddleware catches panics in HTTP handlers and returns 500.
func RecoveryMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if err := recover(); err != nil {
				logFrom(r).Error().
					Interface("panic", err).
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

// trustedHost rejects any Host we do not serve: a rebound DNS name reaches the
// loopback listener with its own Host, even from a request that sends no Origin.
func (c originChecker) trustedHost(r *http.Request) bool {
	u, err := url.Parse("//" + r.Host)
	return err == nil && c.hosts[strings.ToLower(u.Hostname())]
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
			if !checker.trustedHost(r) {
				writeJSON(w, http.StatusMisdirectedRequest, map[string]string{"error": "host not allowed"})
				return
			}
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
	trusted []netip.Prefix
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

// Allow reports whether ip may proceed; when it may not, wait is how long
// until its window resets.
func (rl *RateLimiter) Allow(ip string) (ok bool, wait time.Duration) {
	rl.mu.Lock()
	defer rl.mu.Unlock()

	bucket, found := rl.clients[ip]
	if !found {
		rl.clients[ip] = &tokenBucket{tokens: rl.rate - 1, lastReset: time.Now()}
		return true, 0
	}

	if time.Since(bucket.lastReset) >= rl.window {
		bucket.tokens = rl.rate
		bucket.lastReset = time.Now()
	}

	if bucket.tokens <= 0 {
		return false, rl.window - time.Since(bucket.lastReset)
	}
	bucket.tokens--
	return true, 0
}

// WithTrustedProxies makes the limiter believe X-Forwarded-For from peers
// inside these prefixes; from anyone else the header is client-controlled.
func (rl *RateLimiter) WithTrustedProxies(p []netip.Prefix) *RateLimiter {
	rl.trusted = p
	return rl
}

// ParseTrustedProxies parses a comma-separated CIDR list; empty means none.
func ParseTrustedProxies(s string) ([]netip.Prefix, error) {
	var out []netip.Prefix
	for _, f := range strings.Split(s, ",") {
		if f = strings.TrimSpace(f); f == "" {
			continue
		}
		p, err := netip.ParsePrefix(f)
		if err != nil {
			return nil, fmt.Errorf("trusted proxy %q: %w", f, err)
		}
		out = append(out, p.Masked())
	}
	return out, nil
}

func (rl *RateLimiter) trusts(a netip.Addr) bool {
	for _, p := range rl.trusted {
		if p.Contains(a) {
			return true
		}
	}
	return false
}

// clientIP keys on the peer's IP, never ip:port, or every new connection
// would get a fresh budget. XFF is walked right to left past trusted proxies:
// entries left of the first untrusted hop are supplied by the client.
func (rl *RateLimiter) clientIP(r *http.Request) string {
	peer, err := netip.ParseAddrPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	client := peer.Addr().Unmap()
	if !rl.trusts(client) {
		return bucketKey(client)
	}
	hops := strings.Split(strings.Join(r.Header.Values("X-Forwarded-For"), ","), ",")
	for i := len(hops) - 1; i >= 0; i-- {
		a, err := netip.ParseAddr(strings.TrimSpace(hops[i]))
		if err != nil {
			break // a malformed hop cannot be attributed; keep the nearest trusted hop seen so far
		}
		client = a.Unmap()
		if !rl.trusts(client) {
			break
		}
	}
	return bucketKey(client)
}

// bucketKey collapses IPv6 to its /64, the smallest unit an ISP hands a
// subscriber; per-/128 keys would give one host 2^64 budgets.
func bucketKey(a netip.Addr) string {
	if a.Is6() {
		return netip.PrefixFrom(a, 64).Masked().String()
	}
	return a.String()
}

func (rl *RateLimiter) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if ok, wait := rl.Allow(rl.clientIP(r)); !ok {
			w.Header().Set("Retry-After", strconv.Itoa(int(math.Ceil(wait.Seconds()))))
			http.Error(w, `{"error":"rate limit exceeded"}`, http.StatusTooManyRequests)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// LoginLimit applies rl to POST /api/session only, which is public and the
// password-guessing surface, so it gets a tighter budget than the general API.
func LoginLimit(rl *RateLimiter) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		limited := rl.Middleware(next)
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method == http.MethodPost && r.URL.Path == "/api/session" {
				limited.ServeHTTP(w, r)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
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
