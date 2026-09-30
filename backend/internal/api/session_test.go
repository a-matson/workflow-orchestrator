package api

import (
	"strings"
	"testing"
	"time"
)

func TestSessionCookie_SignAndVerify(t *testing.T) {
	secret := []byte("0123456789abcdef0123456789abcdef")
	now := time.Unix(1_700_000_000, 0)
	v := signSession(secret, "key-1", now.Add(time.Hour))

	if id, err := verifySession(secret, v, now); err != nil || id != "key-1" {
		t.Fatalf("verify(fresh) = %q, %v; want key-1, nil", id, err)
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
		if id, err := verifySession(secret, bad, now); err == nil {
			t.Errorf("%s: verify = %q, nil; want an error", name, id)
		}
	}
	if id, err := verifySession([]byte("another-secret-another-secret-xx"), v, now); err == nil {
		t.Errorf("wrong secret: verify = %q, nil; want an error", id)
	}
	if id, err := verifySession(secret, v, now.Add(time.Hour)); err == nil {
		t.Errorf("expired: verify = %q, nil; want an error", id)
	}
}
