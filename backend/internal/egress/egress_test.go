package egress

import (
	"net/netip"
	"testing"
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
	for _, spec := range []string{"not-a-cidr", "10.0.0.0/33", "example.com", "10.0.0.1", "host:notaport"} {
		if _, err := New(spec); err == nil {
			t.Errorf("New(%q) = nil error, want error", spec)
		}
	}
}
