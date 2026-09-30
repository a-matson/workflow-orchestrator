package persistence

import "context"

// APIKey is an API key's metadata; the key itself is never stored.
type APIKey struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Role string `json:"role"`
}

// CreateAPIKey is a stub for the red commit.
func (s *Store) CreateAPIKey(_ context.Context, name, role string) (string, *APIKey, error) {
	return "flx_stub", &APIKey{ID: "stub", Name: name, Role: role}, nil
}

// RevokeAPIKey is a stub for the red commit.
func (s *Store) RevokeAPIKey(_ context.Context, _ string) error { return nil }
