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
		NewRateLimiter(200, time.Minute).Middleware,
		(&Authenticator{store: h.store, sessionSecret: h.session.Secret}).Middleware(mux),
	)
}
