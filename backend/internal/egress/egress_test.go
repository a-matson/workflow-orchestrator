package egress

import (
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestGuard_DeniesInternalAddresses(t *testing.T) {
	g, err := New("")
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		addr    string
		allowed bool
	}{
		{"127.0.0.1", false},
		{"127.8.9.10", false},
		{"::1", false},
		{"::ffff:127.0.0.1", false},
		{"10.0.0.1", false},
		{"172.16.5.4", false},
		{"192.168.1.1", false},
		{"::ffff:192.168.1.1", false},
		{"fc00::1", false},
		{"fd12:3456::1", false},
		{"169.254.169.254", false}, // cloud metadata
		{"::ffff:169.254.169.254", false},
		{"fe80::1", false},
		{"0.0.0.0", false},
		{"0.1.2.3", false},
		{"::", false},
		{"224.0.0.1", false},
		{"ff02::1", false},
		{"100.64.0.1", false}, // CGNAT
		{"100.127.255.254", false},
		{"::ffff:100.64.0.1", false},
		{"192.0.0.170", false},
		{"198.18.0.1", false},
		{"198.19.255.255", false},
		{"240.0.0.1", false},
		{"255.255.255.255", false},
		{"::a00:1", false},        // IPv4-compatible 10.0.0.1
		{"::ffff:0:a00:1", false}, // IPv4-translated 10.0.0.1
		{"64:ff9b::a00:1", false}, // NAT64 10.0.0.1
		{"64:ff9b::a00:1%eth0", false},
		{"64:ff9b:1::a00:1", false},
		{"2001:0:4136:e378::1", false}, // Teredo
		{"2002:a00:1::", false},        // 6to4 10.0.0.1
		{"fec0::1", false},
		{"fe80::1%eth0", false},
		{"93.184.216.34", true},
		{"::ffff:93.184.216.34", true},
		{"2606:2800:220:1:248:1893:25c8:1946", true},
		{"100.128.0.1", true},
	}
	for _, tt := range tests {
		t.Run(tt.addr, func(t *testing.T) {
			if got := g.allowed(netip.MustParseAddr(tt.addr)); got != tt.allowed {
				t.Errorf("allowed(%s) = %v, want %v", tt.addr, got, tt.allowed)
			}
		})
	}
}

func TestGuard_AllowlistOverridesDeniedCIDR(t *testing.T) {
	g, err := New("10.0.0.0/8, fd00::/8")
	if err != nil {
		t.Fatal(err)
	}
	for addr, want := range map[string]bool{
		"10.1.2.3":        true,
		"::ffff:10.1.2.3": true,
		"fd00::5":         true,
		"192.168.1.1":     false,
		"169.254.169.254": false,
		"93.184.216.34":   true,
	} {
		if got := g.allowed(netip.MustParseAddr(addr)); got != want {
			t.Errorf("allowed(%s) = %v, want %v", addr, got, want)
		}
	}
}

func TestNew_RejectsMalformedEntries(t *testing.T) {
	for _, spec := range []string{"not-a-cidr", "10.0.0.0/33", "example.com", "10.0.0.1", "host:notaport", "host:0", "10.0.0.1/8", "fd00::1/8"} {
		if _, err := New(spec); err == nil {
			t.Errorf("New(%q) = nil error, want error", spec)
		}
	}
}

func TestControl_ChecksTheDialledAddress(t *testing.T) {
	g, err := New("")
	if err != nil {
		t.Fatal(err)
	}
	for addr, wantDenied := range map[string]bool{
		"[::1]:80":          true,
		"127.0.0.1:443":     true,
		"[fe80::1%eth0]:80": true,
		"93.184.216.34:443": false,
		"localhost:80":      true, // unparseable as ip:port fails closed
		"garbage":           true,
	} {
		err := g.control("tcp", addr, nil)
		if denied := errors.Is(err, ErrEgressDenied); denied != wantDenied {
			t.Errorf("control(%q) = %v, want denied=%v", addr, err, wantDenied)
		}
	}
}

func counting(t *testing.T, h http.HandlerFunc) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		h(rw, r)
	}))
	t.Cleanup(srv.Close)
	return srv, &hits
}

func get(t *testing.T, allow, target string) error {
	t.Helper()
	g, err := New(allow)
	if err != nil {
		t.Fatal(err)
	}
	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, target, nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := g.HTTPClient(5 * time.Second).Do(req)
	if err != nil {
		return err
	}
	return resp.Body.Close()
}

func hostPort(srv *httptest.Server) string { return strings.TrimPrefix(srv.URL, "http://") }

func TestHTTPClient_RejectsRedirectToNonHTTPScheme(t *testing.T) {
	srv, _ := counting(t, func(rw http.ResponseWriter, r *http.Request) {
		http.Redirect(rw, r, "ftp://files.example.com/x", http.StatusFound)
	})

	if err := get(t, hostPort(srv), srv.URL); !errors.Is(err, ErrEgressDenied) {
		t.Errorf("err = %v, want ErrEgressDenied", err)
	}
}

func TestHTTPClient_FollowsAtMostFiveRedirects(t *testing.T) {
	srv, hits := counting(t, func(rw http.ResponseWriter, r *http.Request) {
		http.Redirect(rw, r, "/again", http.StatusFound)
	})

	err := get(t, hostPort(srv), srv.URL)

	if err == nil || !strings.Contains(err.Error(), "stopped after 5 redirects") {
		t.Errorf("err = %v, want stopped after 5 redirects", err)
	}
	if n := hits.Load(); n != 6 {
		t.Errorf("server saw %d requests, want 6 (the original and 5 redirects)", n)
	}
}

// The target is private but not loopback: net/http never proxies loopback, so
// a loopback target could not show whether the proxy was bypassed.
func TestHTTPClient_IgnoresProxyEnvironment(t *testing.T) {
	proxy, proxyHits := counting(t, func(http.ResponseWriter, *http.Request) {})
	t.Setenv("HTTP_PROXY", proxy.URL)
	t.Setenv("http_proxy", proxy.URL)
	target := "http://10.255.255.1:81/"
	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, target, nil)
	if err != nil {
		t.Fatal(err)
	}
	// ProxyFromEnvironment caches the environment on first use; if it ran
	// before Setenv, this test would pass without proving anything.
	if u, err := http.ProxyFromEnvironment(req); err != nil || u == nil || u.String() != proxy.URL {
		t.Fatalf("ProxyFromEnvironment = %v, %v; want %s, so the test can detect a proxied request", u, err, proxy.URL)
	}

	err = get(t, hostPort(proxy), target)

	if !errors.Is(err, ErrEgressDenied) {
		t.Errorf("err = %v, want ErrEgressDenied", err)
	}
	if n := proxyHits.Load(); n != 0 {
		t.Errorf("proxy saw %d requests, want 0", n)
	}
}

func TestHTTPClient_AllowlistedHostPortIsExact(t *testing.T) {
	srv, hits := counting(t, func(http.ResponseWriter, *http.Request) {})
	other, otherHits := counting(t, func(http.ResponseWriter, *http.Request) {})
	port := func(s *httptest.Server) string { return strconv.Itoa(s.Listener.Addr().(*net.TCPAddr).Port) }

	if err := get(t, "localhost:"+port(srv), "http://localhost:"+port(srv)+"/"); err != nil {
		t.Errorf("allowlisted name: err = %v, want nil", err)
	}
	if err := get(t, "localhost:"+port(srv), "http://localhost:"+port(other)+"/"); !errors.Is(err, ErrEgressDenied) {
		t.Errorf("same name, other port: err = %v, want ErrEgressDenied", err)
	}
	if hits.Load() != 1 || otherHits.Load() != 0 {
		t.Errorf("hits = %d, %d; want 1, 0", hits.Load(), otherHits.Load())
	}
}
