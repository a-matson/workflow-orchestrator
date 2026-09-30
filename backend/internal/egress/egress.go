// Package egress restricts the network addresses that in-process tasks may
// reach. The backend shares a network with Postgres, Redis and MinIO, and may
// run next to a cloud metadata endpoint, so task URLs chosen by a workflow
// author must not reach internal addresses unless an operator allows them.
package egress

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// ErrEgressDenied is wrapped by every error for a connection the guard refuses.
var ErrEgressDenied = errors.New("egress denied")

// maxRedirects is the number of redirects followed; the request after the last one is refused.
const maxRedirects = 5

// Prefixes that net/netip has no predicate for. The IPv6 ranges that embed an
// IPv4 address are denied whole rather than decoded: nothing this deployment
// calls needs them, and a gateway could translate them to an internal IPv4.
var denyPrefixes = []netip.Prefix{
	netip.MustParsePrefix("0.0.0.0/8"),          // "this network"; Linux dials 0.x as the local host
	netip.MustParsePrefix("100.64.0.0/10"),      // CGNAT, often the cloud provider's internal range
	netip.MustParsePrefix("192.0.0.0/24"),       // IETF protocol assignments, incl. DS-Lite and 192.0.0.170
	netip.MustParsePrefix("198.18.0.0/15"),      // benchmarking
	netip.MustParsePrefix("240.0.0.0/4"),        // reserved
	netip.MustParsePrefix("255.255.255.255/32"), // broadcast, which IsMulticast misses
	netip.MustParsePrefix("::/96"),              // IPv4-compatible
	netip.MustParsePrefix("::ffff:0:0:0/96"),    // IPv4-translated (RFC 6145), which Unmap misses
	netip.MustParsePrefix("64:ff9b::/96"),       // NAT64 well-known prefix
	netip.MustParsePrefix("64:ff9b:1::/48"),     // NAT64 local-use
	netip.MustParsePrefix("2001::/32"),          // Teredo
	netip.MustParsePrefix("2002::/16"),          // 6to4
	netip.MustParsePrefix("fec0::/10"),          // deprecated site-local, which IsPrivate misses
}

// Guard decides which addresses outbound task traffic may connect to.
type Guard struct {
	allowPrefixes  []netip.Prefix
	allowHostPorts map[string]bool // normalised "host:port", as the transport passes it to DialContext
	dialer         net.Dialer
}

// New builds a Guard from a comma-separated allowlist (FLUXOR_EGRESS_ALLOW).
// Each entry is a CIDR, which allows any port, or an exact host:port, which
// allows that host name or IP literal as written in the task URL.
func New(allow string) (*Guard, error) {
	g := &Guard{allowHostPorts: map[string]bool{}}
	for _, entry := range strings.Split(allow, ",") {
		entry = strings.TrimSpace(entry)
		if entry == "" {
			continue
		}
		if strings.Contains(entry, "/") {
			p, err := netip.ParsePrefix(entry)
			if err != nil {
				return nil, fmt.Errorf("egress allowlist entry %q: %w", entry, err)
			}
			// Masking silently would turn a typo like 10.0.0.1/8 into a whole /8.
			if p != p.Masked() {
				return nil, fmt.Errorf("egress allowlist entry %q: host bits set, did you mean %s?", entry, p.Masked())
			}
			g.allowPrefixes = append(g.allowPrefixes, unmapPrefix(p))
			continue
		}
		host, port, err := net.SplitHostPort(entry)
		if err != nil {
			return nil, fmt.Errorf("egress allowlist entry %q: want a CIDR or host:port: %w", entry, err)
		}
		if n, err := strconv.ParseUint(port, 10, 16); err != nil || n == 0 || host == "" {
			return nil, fmt.Errorf("egress allowlist entry %q: want a CIDR or host:port", entry)
		}
		g.allowHostPorts[normaliseHostPort(host, port)] = true
	}
	g.dialer = net.Dialer{Timeout: 30 * time.Second, KeepAlive: 30 * time.Second, Control: g.control}
	return g, nil
}

// An allowlisted 10.0.0.0/8 must also match ::ffff:10.x, which allowed unmaps.
func unmapPrefix(p netip.Prefix) netip.Prefix {
	if !p.Addr().Is4In6() || p.Bits() < 96 {
		return p
	}
	return netip.PrefixFrom(p.Addr().Unmap(), p.Bits()-96)
}

func normaliseHostPort(host, port string) string {
	if ip, err := netip.ParseAddr(host); err == nil {
		host = ip.Unmap().String()
	}
	return net.JoinHostPort(strings.ToLower(strings.TrimSuffix(host, ".")), port)
}

func (g *Guard) allowed(ip netip.Addr) bool {
	// Unmapping first makes every IPv4 rule cover its ::ffff: form too.
	// Prefix.Contains never matches a zoned address, so a zone in the URL
	// ("[64:ff9b::a00:1%25eth0]") would slip past every IPv6 prefix; Linux
	// ignores the zone for non-link-local destinations.
	ip = ip.Unmap().WithZone("")
	for _, p := range g.allowPrefixes {
		if p.Contains(ip) {
			return true
		}
	}
	if ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() ||
		ip.IsInterfaceLocalMulticast() || ip.IsMulticast() || ip.IsUnspecified() {
		return false
	}
	for _, p := range denyPrefixes {
		if p.Contains(ip) {
			return false
		}
	}
	return true
}

// control runs after name resolution on the exact IP being dialled, so DNS
// rebinding and redirects to internal hosts are caught on every connection.
func (g *Guard) control(_, address string, _ syscall.RawConn) error {
	ap, err := netip.ParseAddrPort(address)
	if err != nil {
		return fmt.Errorf("%w: unparseable address %q", ErrEgressDenied, address)
	}
	if !g.allowed(ap.Addr()) {
		return fmt.Errorf("%w: %s", ErrEgressDenied, ap.Addr().Unmap())
	}
	return nil
}

// CheckHostPort stub.
func (g *Guard) CheckHostPort(ctx context.Context, host string, port uint16) error { return nil }

// DialContext dials addr unless the guard denies the resolved IP. A host:port
// on the allowlist is dialled without the IP check: the operator trusts that
// name, whatever it resolves to.
func (g *Guard) DialContext(ctx context.Context, network, addr string) (net.Conn, error) {
	if host, port, err := net.SplitHostPort(addr); err == nil && g.allowHostPorts[normaliseHostPort(host, port)] {
		plain := g.dialer
		plain.Control = nil
		return plain.DialContext(ctx, network, addr)
	}
	return g.dialer.DialContext(ctx, network, addr)
}

// HTTPClient returns a client whose every connection, redirects included,
// passes the guard. It ignores proxy environment variables: through a proxy
// the guard would only ever see the proxy's address.
func (g *Guard) HTTPClient(timeout time.Duration) *http.Client {
	return &http.Client{
		Timeout: timeout,
		Transport: &http.Transport{
			Proxy:               nil,
			DialContext:         g.DialContext,
			ForceAttemptHTTP2:   true,
			TLSHandshakeTimeout: 10 * time.Second,
			MaxIdleConnsPerHost: 10,
			IdleConnTimeout:     60 * time.Second,
		},
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) > maxRedirects {
				return fmt.Errorf("stopped after %d redirects", maxRedirects)
			}
			if req.URL.Scheme != "http" && req.URL.Scheme != "https" {
				return fmt.Errorf("%w: redirect to scheme %q", ErrEgressDenied, req.URL.Scheme)
			}
			return nil
		},
	}
}
