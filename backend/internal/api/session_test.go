package api

import (
	"encoding/base64"
	"strings"
	"testing"
	"time"
)

func TestSessionCookie_SignAndVerify(t *testing.T) {
	secret := []byte("0123456789abcdef0123456789abcdef")
	now := time.Unix(1_700_000_000, 0)
	v := signSession(secret, "key-1", now.Add(time.Hour))

	if id, exp, err := verifySession(secret, v, now); err != nil || id != "key-1" || !exp.Equal(now.Add(time.Hour)) {
		t.Fatalf("verify(fresh) = %q, %v, %v; want key-1, %v, nil", id, exp, err, now.Add(time.Hour))
	}

	payload, mac, _ := strings.Cut(v, ".")
	flip := func(s string) string {
		if s == "" {
			return "A"
		}
		c := byte('A')
		if s[0] == 'A' {
			c = 'B'
		}
		return string(c) + s[1:]
	}
	for name, bad := range map[string]string{
		"empty":            "",
		"no separator":     payload + mac,
		"tampered payload": flip(payload) + "." + mac,
		"tampered mac":     payload + "." + flip(mac),
		"other key id":     signSession(secret, "key-2", now.Add(time.Hour))[:len(payload)] + "." + mac,
	} {
		if id, _, err := verifySession(secret, bad, now); err == nil {
			t.Errorf("%s: verify = %q, nil; want an error", name, id)
		}
	}
	if id, _, err := verifySession([]byte("another-secret-another-secret-xx"), v, now); err == nil {
		t.Errorf("wrong secret: verify = %q, nil; want an error", id)
	}
	if id, _, err := verifySession(secret, v, now.Add(time.Hour)); err == nil {
		t.Errorf("expired: verify = %q, nil; want an error", id)
	}
}

// Bearer principals have no expiry; a cookie principal is invalid from its
// expiry on, decided before the key lookup (a nil store would panic).
func TestStillValid_ExpiredSession(t *testing.T) {
	a := &Authenticator{}
	ok, err := a.StillValid(t.Context(), &Principal{KeyID: "k", Expires: time.Now().Add(-time.Second)})
	if ok || err != nil {
		t.Errorf("expired session: StillValid = %v, %v; want false, nil", ok, err)
	}
}

// The expiry crosses int64/uint64 in both directions; out-of-range values must
// come out expired or rejected, never wrapped into the far future.
func TestSessionCookie_ExpiryBounds(t *testing.T) {
	secret := []byte("0123456789abcdef0123456789abcdef")
	now := time.Unix(1_700_000_000, 0)
	if id, _, err := verifySession(secret, signSession(secret, "k", time.Unix(-5, 0)), now); err == nil {
		t.Errorf("pre-1970 expiry: verify = %q, nil; want an error", id)
	}

	payload := append([]byte{sessionVersion, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF}, 'k')
	forged := base64.RawURLEncoding.EncodeToString(payload) + "." +
		base64.RawURLEncoding.EncodeToString(sessionMAC(secret, payload))
	if id, _, err := verifySession(secret, forged, now); err == nil {
		t.Errorf("expiry above MaxInt64: verify = %q, nil; want an error", id)
	}
}
