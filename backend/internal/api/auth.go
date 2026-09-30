package api

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/rs/zerolog"

	"github.com/a-matson/workflow-orchestrator/backend/internal/persistence"
)

// Role is the minimum role a route requires. Roles are ordered: each one
// includes every permission of the roles below it.
type Role string

const (
	// RolePublic marks routes that need no credential.
	RolePublic   Role = ""
	RoleViewer   Role = "viewer"
	RoleOperator Role = "operator"
	RoleAdmin    Role = "admin"
)

var roleRank = map[Role]int{RolePublic: 0, RoleViewer: 1, RoleOperator: 2, RoleAdmin: 3}

// ValidRole reports whether r names a role a principal can hold.
func ValidRole(r string) bool {
	return roleRank[Role(r)] > 0
}

func (r Role) allows(required Role) bool {
	return roleRank[r] >= roleRank[required]
}

// routePolicy maps every pattern in Handler.routes to its minimum role.
// TestRoutePolicy_CoversEveryRoute fails when a route is missing, so a new
// endpoint cannot become public by omission.
var routePolicy = map[string]Role{
	"GET /api/health": RolePublic,
	"GET /api/ready":  RolePublic,

	"GET /api/workflows":                 RoleViewer,
	"GET /api/workflows/{id}":            RoleViewer,
	"GET /api/executions":                RoleViewer,
	"GET /api/executions/{id}":           RoleViewer,
	"GET /api/executions/{execID}/tasks": RoleViewer,
	"GET /api/tasks/{id}":                RoleViewer,
	"GET /api/tasks/{id}/logs":           RoleViewer,
	"GET /api/tasks/{id}/artifacts":      RoleViewer,
	"GET /api/artifacts/url":             RoleViewer,
	"GET /api/metrics":                   RoleViewer,
	"GET /ws":                            RoleViewer,

	"POST /api/workflows":              RoleOperator,
	"PUT /api/workflows/{id}":          RoleOperator,
	"POST /api/workflows/{id}/trigger": RoleOperator,
	"POST /api/executions/{id}/cancel": RoleOperator,
	"POST /api/executions/{id}/retry":  RoleOperator,

	"GET /api/keys":         RoleAdmin,
	"POST /api/keys":        RoleAdmin,
	"DELETE /api/keys/{id}": RoleAdmin,
}

// Principal is the authenticated caller of a request.
type Principal struct {
	KeyID string
	Name  string
	Role  Role
}

type principalKey struct{}

// PrincipalFrom returns the request's principal, or nil on a public route.
func PrincipalFrom(ctx context.Context) *Principal {
	p, _ := ctx.Value(principalKey{}).(*Principal)
	return p
}

// errUnauthenticated covers both a missing and a rejected credential, so a
// caller cannot tell a revoked key from one that never existed.
var errUnauthenticated = errors.New("authentication required")

// Authenticator resolves a request's credential to a Principal.
type Authenticator struct {
	store *persistence.Store
}

// NewAuthenticator returns an Authenticator backed by store's API keys.
func NewAuthenticator(store *persistence.Store) *Authenticator {
	return &Authenticator{store: store}
}

// Authenticate returns the request's principal. It returns an error wrapping
// errUnauthenticated when the request carries no valid credential, and any
// other error when the credential could not be checked.
func (a *Authenticator) Authenticate(r *http.Request) (*Principal, error) {
	scheme, token, ok := strings.Cut(r.Header.Get("Authorization"), " ")
	if !ok || !strings.EqualFold(scheme, "Bearer") {
		return nil, errUnauthenticated
	}
	key, err := a.store.LookupAPIKey(r.Context(), strings.TrimSpace(token))
	if errors.Is(err, persistence.ErrNotFound) {
		return nil, errUnauthenticated
	}
	if err != nil {
		return nil, fmt.Errorf("checking API key: %w", err)
	}
	return &Principal{KeyID: key.ID, Name: key.Name, Role: Role(key.Role)}, nil
}

// Middleware enforces routePolicy for requests served by mux. A registered
// pattern absent from the policy requires admin, so it fails closed.
func (a *Authenticator) Middleware(mux *http.ServeMux) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_, pattern := mux.Handler(r)
			required, ok := routePolicy[pattern]
			switch {
			case pattern == "":
				// Unmatched path or method: the mux only answers 404/405, but
				// anonymous callers still must not probe which routes exist.
				required = RoleViewer
			case !ok:
				required = RoleAdmin
			}
			if required == RolePublic {
				next.ServeHTTP(w, r)
				return
			}

			p, err := a.Authenticate(r)
			switch {
			case errors.Is(err, errUnauthenticated):
				w.Header().Set("WWW-Authenticate", "Bearer")
				writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "authentication required"})
				return
			case err != nil:
				writeError(w, r, http.StatusInternalServerError, "internal server error", err)
				return
			}

			// Mutating the request's own logger (never the global fallback)
			// lets LoggingMiddleware's access line carry the key too.
			if l := zerolog.Ctx(r.Context()); l.GetLevel() != zerolog.Disabled {
				l.UpdateContext(func(c zerolog.Context) zerolog.Context {
					return c.Str("api_key_id", p.KeyID).Str("key_name", p.Name)
				})
			}
			if !p.Role.allows(required) {
				writeJSON(w, http.StatusForbidden, map[string]string{"error": "insufficient role"})
				return
			}
			next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), principalKey{}, p)))
		})
	}
}
