package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"testing"
	"time"
)

var okHandler = http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })

func send(h http.Handler, method, path, remote, xff string) *httptest.ResponseRecorder {
	req := httptest.NewRequestWithContext(context.Background(), method, path, nil)
	req.RemoteAddr = remote
	if xff != "" {
		req.Header.Set("X-Forwarded-For", xff)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestRateLimit_IgnoresPortAndSpoofedXFF(t *testing.T) {
	trusted := []netip.Prefix{netip.MustParsePrefix("172.18.0.0/16")}
	newH := func() http.Handler {
		return NewRateLimiter(3, time.Minute).WithTrustedProxies(trusted).Middleware(okHandler)
	}

	t.Run("new connection does not get a fresh budget", func(t *testing.T) {
		h := newH()
		for i, port := range []string{"1000", "1001", "1002", "1003"} {
			want := http.StatusOK
			if i == 3 {
				want = http.StatusTooManyRequests
			}
			if got := send(h, "GET", "/api/x", "192.0.2.1:"+port, "").Code; got != want {
				t.Fatalf("request %d: got %d, want %d", i+1, got, want)
			}
		}
	})

	t.Run("IPv6 and IPv4-mapped peers normalise", func(t *testing.T) {
		h := newH()
		remotes := []string{"[::ffff:192.0.2.1]:1", "192.0.2.1:2", "[::ffff:192.0.2.1]:3", "192.0.2.1:4"}
		var last int
		for _, r := range remotes {
			last = send(h, "GET", "/api/x", r, "").Code
		}
		if last != http.StatusTooManyRequests {
			t.Fatalf("mapped and plain IPv4 should share a bucket, got %d", last)
		}
	})

	t.Run("XFF from an untrusted peer is ignored", func(t *testing.T) {
		h := newH()
		var last int
		for _, x := range []string{"10.0.0.1", "10.0.0.2", "10.0.0.3", "10.0.0.4"} {
			last = send(h, "GET", "/api/x", "192.0.2.1:5000", x).Code
		}
		if last != http.StatusTooManyRequests {
			t.Fatalf("spoofed XFF minted a new bucket: got %d, want 429", last)
		}
	})

	t.Run("trusted proxy: bucket is the forwarded client", func(t *testing.T) {
		h := newH()
		var last int
		for i := 0; i < 4; i++ {
			last = send(h, "GET", "/api/x", "172.18.0.2:5000", "198.51.100.7").Code
		}
		if last != http.StatusTooManyRequests {
			t.Fatalf("got %d, want 429 for the 4th request from 198.51.100.7", last)
		}
		if got := send(h, "GET", "/api/x", "172.18.0.2:5000", "198.51.100.8").Code; got != http.StatusOK {
			t.Fatalf("a different forwarded client must have its own bucket, got %d", got)
		}
	})

	t.Run("trusted proxy: right-most untrusted hop wins", func(t *testing.T) {
		h := newH()
		var last int
		for i := 0; i < 4; i++ {
			// Left entry is client-supplied and varies; the right one was appended by the proxy.
			last = send(h, "GET", "/api/x", "172.18.0.2:5000", "10.9.9."+string(rune('1'+i))+", 198.51.100.7, 172.18.0.9").Code
		}
		if last != http.StatusTooManyRequests {
			t.Fatalf("got %d, want 429: left-most XFF entry must not pick the bucket", last)
		}
	})

	t.Run("429 carries Retry-After", func(t *testing.T) {
		h := newH()
		var rec *httptest.ResponseRecorder
		for i := 0; i < 4; i++ {
			rec = send(h, "GET", "/api/x", "192.0.2.1:1", "")
		}
		if rec.Code != http.StatusTooManyRequests || rec.Header().Get("Retry-After") == "" || rec.Header().Get("Retry-After") == "1" {
			t.Fatalf("code=%d Retry-After=%q, want 429 with the seconds until the window resets", rec.Code, rec.Header().Get("Retry-After"))
		}
	})
}

func TestLoginLimit_StricterBucketOnlyForLogin(t *testing.T) {
	h := LoginLimit(NewRateLimiter(2, time.Minute))(okHandler)

	for i, want := range []int{200, 200, 429} {
		if got := send(h, "POST", "/api/session", "192.0.2.1:"+string(rune('1'+i)), "").Code; got != want {
			t.Fatalf("login %d: got %d, want %d", i+1, got, want)
		}
	}
	if got := send(h, "POST", "/api/session", "192.0.2.2:1", "").Code; got != http.StatusOK {
		t.Fatalf("another IP has its own login bucket, got %d", got)
	}
	for i := 0; i < 5; i++ {
		if got := send(h, "GET", "/api/workflows", "192.0.2.1:9", "").Code; got != http.StatusOK {
			t.Fatalf("non-login request %d was limited by the login bucket: %d", i+1, got)
		}
	}
}
