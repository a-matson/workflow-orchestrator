package api

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// A route missing from routePolicy would otherwise ship with whatever the
// fallback is, so every registered pattern must be listed explicitly.
func TestRoutePolicy_CoversEveryRoute(t *testing.T) {
	routes := (&Handler{}).routes()
	for pattern := range routes {
		if _, ok := routePolicy[pattern]; !ok {
			t.Errorf("route %q has no routePolicy entry", pattern)
		}
	}
	for pattern := range routePolicy {
		if _, ok := routes[pattern]; !ok {
			t.Errorf("routePolicy entry %q matches no route", pattern)
		}
	}
}

// publicRoutes is the complete list of routes reachable without a key.
var publicRoutes = map[string]bool{
	"GET /api/health": true,
	"GET /api/ready":  true,
	// Login: it takes the key in its body and exchanges it for a cookie, so
	// requiring a credential first would make browser sign-in impossible.
	"POST /api/session": true,
}

// ownSessionRoutes mutate only the caller's own browser session, never shared
// state, so they are open to every role, and to anyone for login.
var ownSessionRoutes = map[string]bool{"POST /api/session": true, "DELETE /api/session": true}

// The role model as invariants, so an entry that is present but too lax
// (a viewer on a mutating route, say) fails here and not in production.
func TestRoutePolicy_Levels(t *testing.T) {
	for pattern, role := range routePolicy {
		method, path, _ := strings.Cut(pattern, " ")
		if publicRoutes[pattern] != (role == RolePublic) {
			t.Errorf("%s: role %q, but public = %v", pattern, role, publicRoutes[pattern])
		}
		if method != http.MethodGet && method != http.MethodHead && !ownSessionRoutes[pattern] && !role.allows(RoleOperator) {
			t.Errorf("%s mutates but requires only %q", pattern, role)
		}
		if strings.HasPrefix(path, "/api/keys") && role != RoleAdmin {
			t.Errorf("%s manages keys but requires %q, want admin", pattern, role)
		}
	}
}

func TestRequireRoles(t *testing.T) {
	viewer := &Principal{KeyID: "k1", Name: "v", Role: RoleViewer}
	mux := (&Handler{}).Routes()

	for _, tc := range []struct {
		name, method, path string
		principal          *Principal
		authErr            error
		want               int
	}{
		{"no credential", http.MethodGet, "/api/workflows", nil, errUnauthenticated, http.StatusUnauthorized},
		{"no credential on unmatched path", http.MethodGet, "/api/nope", nil, errUnauthenticated, http.StatusUnauthorized},
		{"check failed", http.MethodGet, "/api/workflows", nil, errors.New("db down"), http.StatusInternalServerError},
		{"viewer mutates", http.MethodPost, "/api/workflows", viewer, nil, http.StatusForbidden},
		{"viewer manages keys", http.MethodGet, "/api/keys", viewer, nil, http.StatusForbidden},
		{"viewer reads", http.MethodGet, "/api/workflows/x", viewer, nil, http.StatusTeapot},
		{"viewer on unmatched path", http.MethodGet, "/api/nope", viewer, nil, http.StatusTeapot},
		{"public route skips auth", http.MethodGet, "/api/health", nil, errors.New("must not be called"), http.StatusTeapot},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var got *Principal
			next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				got = PrincipalFrom(r.Context())
				w.WriteHeader(http.StatusTeapot)
			})
			auth := func(*http.Request) (*Principal, error) { return tc.principal, tc.authErr }
			rr := httptest.NewRecorder()
			requireRoles(mux, auth, nil)(next).ServeHTTP(rr, httptest.NewRequestWithContext(t.Context(), tc.method, tc.path, nil))

			if rr.Code != tc.want {
				t.Fatalf("status %d, want %d: %s", rr.Code, tc.want, rr.Body)
			}
			if tc.want == http.StatusUnauthorized && rr.Header().Get("WWW-Authenticate") != "Bearer" {
				t.Errorf("401 without WWW-Authenticate: Bearer")
			}
			if tc.want == http.StatusTeapot && got != tc.principal {
				t.Errorf("principal in context = %+v, want %+v", got, tc.principal)
			}
		})
	}
}

func TestRequireRoles_UnlistedRouteNeedsAdmin(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /unlisted", func(http.ResponseWriter, *http.Request) {})
	next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusTeapot) })
	for role, want := range map[Role]int{RoleOperator: http.StatusForbidden, RoleAdmin: http.StatusTeapot} {
		auth := func(*http.Request) (*Principal, error) { return &Principal{Role: role}, nil }
		rr := httptest.NewRecorder()
		requireRoles(mux, auth, nil)(next).ServeHTTP(rr, httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/unlisted", nil))
		if rr.Code != want {
			t.Errorf("%s on unlisted route: status %d, want %d", role, rr.Code, want)
		}
	}
}

// Denied attempts are audited on mutating routes only, so probing GETs
// cannot flood the log.
func TestRequireRoles_AuditsDeniedMutationsOnly(t *testing.T) {
	mux := (&Handler{}).Routes()
	next := http.HandlerFunc(func(http.ResponseWriter, *http.Request) {})
	for _, tc := range []struct {
		method, path, want string
		principal          *Principal
		authErr            error
	}{
		{http.MethodPost, "/api/workflows/x/trigger", "workflow.trigger", &Principal{Role: RoleViewer}, nil},
		{http.MethodPost, "/api/workflows/x/trigger", "workflow.trigger", nil, errUnauthenticated},
		{http.MethodGet, "/api/workflows", "", nil, errUnauthenticated},
		{http.MethodGet, "/api/audit", "", &Principal{Role: RoleViewer}, nil},
		{http.MethodPost, "/api/workflows/x/trigger", "", &Principal{Role: RoleOperator}, nil},
	} {
		var got string
		auth := func(*http.Request) (*Principal, error) { return tc.principal, tc.authErr }
		hook := func(_ *http.Request, _ *Principal, action string) { got = action }
		requireRoles(mux, auth, hook)(next).ServeHTTP(httptest.NewRecorder(), httptest.NewRequestWithContext(t.Context(), tc.method, tc.path, nil))
		if got != tc.want {
			t.Errorf("%s %s: audited %q, want %q", tc.method, tc.path, got, tc.want)
		}
	}
}
