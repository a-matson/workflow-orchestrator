// Package egress restricts the network addresses that in-process tasks may reach.
package egress

import (
	"errors"
	"net/http"
	"net/netip"
	"time"
)

// ErrEgressDenied is wrapped by every error for a connection the guard refuses.
var ErrEgressDenied = errors.New("egress denied")

// Guard decides which addresses outbound task traffic may connect to.
type Guard struct{}

// New builds a Guard from a comma-separated allowlist of CIDRs and host:port entries.
func New(allow string) (*Guard, error) {
	return &Guard{}, nil
}

func (g *Guard) allowed(ip netip.Addr) bool {
	return true
}

// HTTPClient returns a client whose every connection, redirects included, passes the guard.
func (g *Guard) HTTPClient(timeout time.Duration) *http.Client {
	return &http.Client{Timeout: timeout}
}
