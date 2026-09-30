package api

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"math"
	"net/http"
	"strings"
	"time"

	"github.com/a-matson/workflow-orchestrator/backend/internal/persistence"
)

const (
	sessionCookie = "fluxor_session"
	sessionTTL    = 12 * time.Hour
	// sessionVersion leads the payload so a future format change can reject
	// old cookies instead of misparsing them.
	sessionVersion = 1
	// MinSessionSecret is the shortest FLUXOR_SESSION_SECRET accepted: the
	// HMAC key should be at least as long as the SHA-256 output.
	MinSessionSecret = 32
	// maxLoginBody bounds what the public, pre-auth login endpoint will read;
	// a real body is {"api_key":"flx_" + 43 chars}.
	maxLoginBody = 4 << 10
)

var errBadSession = errors.New("invalid session cookie")

// SessionConfig configures the browser session cookie.
type SessionConfig struct {
	// Secret keys the cookie's HMAC. Changing it ends every session.
	Secret []byte
	// Secure forces the Secure flag on requests that arrive over plain HTTP,
	// as they do behind a TLS-terminating proxy.
	Secure bool
}

// RandomSessionSecret returns a fresh secret for a server started without one.
func RandomSessionSecret() []byte {
	b := make([]byte, MinSessionSecret)
	// crypto/rand.Read never returns an error; it aborts the process instead.
	_, _ = rand.Read(b)
	return b
}

// signSession encodes base64url(payload).base64url(HMAC-SHA256(secret, payload)),
// payload = version || big-endian unix expiry || key id. The key id is not
// secret; the MAC only stops a client from forging or extending a session.
func signSession(secret []byte, keyID string, expires time.Time) string {
	payload := make([]byte, 9, 9+len(keyID))
	payload[0] = sessionVersion
	exp := expires.Unix()
	if exp < 0 {
		exp = 0 // a pre-1970 expiry is already expired; 0 keeps it so
	}
	binary.BigEndian.PutUint64(payload[1:9], uint64(exp))
	payload = append(payload, keyID...)
	return base64.RawURLEncoding.EncodeToString(payload) + "." +
		base64.RawURLEncoding.EncodeToString(sessionMAC(secret, payload))
}

// verifySession returns the key id and expiry of an authentic, unexpired cookie value.
func verifySession(secret []byte, value string, now time.Time) (string, time.Time, error) {
	p, m, ok := strings.Cut(value, ".")
	if !ok {
		return "", time.Time{}, errBadSession
	}
	payload, err := base64.RawURLEncoding.DecodeString(p)
	if err != nil {
		return "", time.Time{}, errBadSession
	}
	mac, err := base64.RawURLEncoding.DecodeString(m)
	if err != nil || !hmac.Equal(mac, sessionMAC(secret, payload)) {
		return "", time.Time{}, errBadSession
	}
	if len(payload) <= 9 || payload[0] != sessionVersion {
		return "", time.Time{}, errBadSession
	}
	raw := binary.BigEndian.Uint64(payload[1:9])
	if raw > math.MaxInt64 {
		return "", time.Time{}, errBadSession
	}
	expires := time.Unix(int64(raw), 0)
	if !now.Before(expires) {
		return "", time.Time{}, errBadSession
	}
	return string(payload[9:]), expires, nil
}

func sessionMAC(secret, payload []byte) []byte {
	h := hmac.New(sha256.New, secret)
	h.Write(payload)
	return h.Sum(nil)
}

// noStore keeps session responses, which name the principal, out of every cache.
func noStore(w http.ResponseWriter) {
	w.Header().Set("Cache-Control", "no-store")
}

// setSessionCookie is the only place the session cookie is written, for login
// and for clearing alike, so both always carry HttpOnly and SameSite=Strict.
func (h *Handler) setSessionCookie(w http.ResponseWriter, r *http.Request, value string, maxAge int) {
	http.SetCookie(w, &http.Cookie{ // #nosec G124 -- Secure is conditional on purpose: a plain-HTTP localhost dev setup would drop a Secure cookie
		Name:     sessionCookie,
		Value:    value,
		Path:     "/",
		MaxAge:   maxAge,
		HttpOnly: true,
		Secure:   r.TLS != nil || h.session.Secure,
		SameSite: http.SameSiteStrictMode,
	})
}

type sessionPrincipal struct {
	Name string `json:"name"`
	Role Role   `json:"role"`
}

// CreateSession exchanges an API key for a session cookie. Unknown, revoked
// and malformed keys get the same 401, so the answer reveals nothing.
func (h *Handler) CreateSession(w http.ResponseWriter, r *http.Request) {
	noStore(w)
	var req struct {
		APIKey string `json:"api_key"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxLoginBody)).Decode(&req); err != nil {
		var tooBig *http.MaxBytesError
		if errors.As(err, &tooBig) {
			writeError(w, r, http.StatusRequestEntityTooLarge, "request body too large", nil)
			return
		}
		writeError(w, r, http.StatusBadRequest, "invalid request body", nil)
		return
	}
	key, err := h.store.LookupAPIKey(r.Context(), strings.TrimSpace(req.APIKey))
	if errors.Is(err, persistence.ErrNotFound) {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "invalid API key"})
		return
	}
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, "internal server error", err)
		return
	}
	h.setSessionCookie(w, r, signSession(h.session.Secret, key.ID, time.Now().Add(sessionTTL)), int(sessionTTL.Seconds()))
	logFrom(r).Info().Str("api_key_id", key.ID).Str("key_name", key.Name).Msg("browser session started")
	writeJSON(w, http.StatusOK, sessionPrincipal{Name: key.Name, Role: Role(key.Role)})
}

// GetSession returns the caller's principal, whether it came from a cookie or a Bearer key.
func (h *Handler) GetSession(w http.ResponseWriter, r *http.Request) {
	noStore(w)
	p := PrincipalFrom(r.Context())
	writeJSON(w, http.StatusOK, sessionPrincipal{Name: p.Name, Role: p.Role})
}

// DeleteSession clears the cookie. The cookie stays cryptographically valid
// until it expires; revoke the key to end a copied cookie's life early.
func (h *Handler) DeleteSession(w http.ResponseWriter, r *http.Request) {
	noStore(w)
	h.setSessionCookie(w, r, "", -1)
	w.WriteHeader(http.StatusNoContent)
}
