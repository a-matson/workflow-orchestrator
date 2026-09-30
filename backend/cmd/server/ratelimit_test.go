package main

import "testing"

func TestRateLimitFromEnv(t *testing.T) {
	const k = "FLUXOR_TEST_LIMIT"
	for v, want := range map[string]int{"": 7, "5000": 5000} {
		t.Setenv(k, v)
		if got, err := rateLimitFromEnv(k, 7); err != nil || got != want {
			t.Fatalf("%q: got %d, %v", v, got, err)
		}
	}
	for _, v := range []string{"0", "-1", "abc", "10x", " 5"} {
		t.Setenv(k, v)
		if _, err := rateLimitFromEnv(k, 7); err == nil {
			t.Fatalf("%q must be rejected", v)
		}
	}
}

func TestMaxBodyFromEnv(t *testing.T) {
	const k = "FLUXOR_TEST_BODY"
	for v, want := range map[string]int64{"": 7, "5000000000": 5000000000} {
		t.Setenv(k, v)
		if got, err := maxBodyFromEnv(k, 7); err != nil || got != want {
			t.Fatalf("%q: got %d, %v", v, got, err)
		}
	}
	for _, v := range []string{"0", "-1", "1MiB", " 5"} {
		t.Setenv(k, v)
		if _, err := maxBodyFromEnv(k, 7); err == nil {
			t.Fatalf("%q must be rejected", v)
		}
	}
}
