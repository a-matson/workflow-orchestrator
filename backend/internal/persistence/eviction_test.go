package persistence

import (
	"strings"
	"testing"
)

func TestCheckEvictionPolicy(t *testing.T) {
	if err := CheckEvictionPolicy("noeviction"); err != nil {
		t.Fatalf("noeviction: unexpected error %v", err)
	}
	for _, p := range []string{
		"allkeys-lru", "volatile-lru", "allkeys-random", "volatile-ttl",
		"allkeys-lfu", "volatile-lfu", "volatile-random",
	} {
		t.Run(p, func(t *testing.T) {
			err := CheckEvictionPolicy(p)
			if err == nil {
				t.Fatalf("%s: expected error", p)
			}
			if !strings.Contains(err.Error(), p) || !strings.Contains(err.Error(), "noeviction") {
				t.Errorf("%s: error %q must mention policy and noeviction", p, err)
			}
		})
	}
}
