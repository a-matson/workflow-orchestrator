package api

import "net/http"

// ChainMiddleware applies middleware in LIFO order (last applied = outermost wrapper).
// Usage: ChainMiddleware(handler, mw1, mw2, mw3) → mw1(mw2(mw3(handler)))
func ChainMiddleware(h http.Handler, middlewares ...func(http.Handler) http.Handler) http.Handler {
	for i := len(middlewares) - 1; i >= 0; i-- {
		h = middlewares[i](h)
	}
	return h
}
