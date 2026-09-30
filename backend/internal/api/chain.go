package api

import (
	"net/http"
	"time"
)

// ChainMiddleware applies middleware in LIFO order (last applied = outermost wrapper).
// Usage: ChainMiddleware(handler, mw1, mw2, mw3) → mw1(mw2(mw3(handler)))
func ChainMiddleware(h http.Handler, middlewares ...func(http.Handler) http.Handler) http.Handler {
	for i := len(middlewares) - 1; i >= 0; i-- {
		h = middlewares[i](h)
	}
	return h
}

// DefaultMaxBody is the request body cap when FLUXOR_MAX_BODY_BYTES is unset.
const DefaultMaxBody = 1 << 20

// MaxBody caps every request body at n bytes so a client cannot make the
// server buffer an arbitrarily large JSON document. Handlers turn the
// resulting *http.MaxBytesError into a 413.
func MaxBody(n int64) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			r.Body = http.MaxBytesReader(w, r.Body, n)
			next.ServeHTTP(w, r)
		})
	}
}

// Server returns the routes behind the production middleware chain.
// Authentication runs after the rate limiter so a flood of bad keys is
// throttled before it reaches Postgres.
func (h *Handler) Server(allowedOrigins []string) http.Handler {
	mux := h.Routes()
	return ChainMiddleware(
		mux,
		RequestIDMiddleware,
		RecoveryMiddleware,
		LoggingMiddleware,
		OriginPolicy(allowedOrigins),
		MaxBody(h.maxBody),
		NewRateLimiter(h.generalLimit, time.Minute).WithTrustedProxies(h.trusted).Middleware,
		LoginLimit(NewRateLimiter(h.loginLimit, time.Minute).WithTrustedProxies(h.trusted)),
		(&Authenticator{store: h.store, sessionSecret: h.session.Secret}).Middleware(mux),
	)
}
