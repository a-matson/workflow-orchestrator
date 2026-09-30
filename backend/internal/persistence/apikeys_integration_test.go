//go:build integration

package persistence_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/a-matson/workflow-orchestrator/backend/internal/persistence"
)

func TestAPIKeys_Lifecycle(t *testing.T) {
	store := setupStore(t)
	ctx := context.Background()

	plain, key, err := store.CreateAPIKey(ctx, "ci", "operator")
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if !strings.HasPrefix(plain, "flx_") || !persistence.ValidAPIKey(plain) {
		t.Fatalf("issued key %q is not well-formed", plain)
	}

	first, err := store.LookupAPIKey(ctx, plain)
	if err != nil || first.ID != key.ID || first.Role != "operator" {
		t.Fatalf("lookup = %+v, %v; want key %s", first, err, key.ID)
	}
	// The first lookup records use; a second within the minute must not rewrite it.
	second, err := store.LookupAPIKey(ctx, plain)
	if err != nil || second.LastUsedAt == nil {
		t.Fatalf("second lookup = %+v, %v; want last_used_at set", second, err)
	}
	third, err := store.LookupAPIKey(ctx, plain)
	if err != nil || !third.LastUsedAt.Equal(*second.LastUsedAt) {
		t.Fatalf("last_used_at moved within a minute: %v -> %v (%v)", second.LastUsedAt, third.LastUsedAt, err)
	}

	if _, err := store.LookupAPIKey(ctx, plain+"x"); !errors.Is(err, persistence.ErrNotFound) {
		t.Fatalf("lookup of unknown key: %v, want ErrNotFound", err)
	}

	boot := "flx_" + strings.Repeat("A", 43)
	for range 2 {
		if err := store.EnsureAPIKey(ctx, "boot", "admin", boot); err != nil {
			t.Fatalf("ensure: %v", err)
		}
	}
	if err := store.EnsureAPIKey(ctx, "boot", "admin", "not-a-key"); !errors.Is(err, persistence.ErrMalformedAPIKey) {
		t.Fatalf("ensure malformed: %v, want ErrMalformedAPIKey", err)
	}
	if n, err := store.CountActiveAPIKeys(ctx); err != nil || n != 2 {
		t.Fatalf("active keys = %d, %v; want 2 (ensure must be idempotent)", n, err)
	}

	if err := store.RevokeAPIKey(ctx, key.ID); err != nil {
		t.Fatalf("revoke: %v", err)
	}
	if err := store.RevokeAPIKey(ctx, key.ID); !errors.Is(err, persistence.ErrNotFound) {
		t.Fatalf("second revoke: %v, want ErrNotFound", err)
	}
	if err := store.RevokeAPIKey(ctx, "not-a-uuid"); !errors.Is(err, persistence.ErrNotFound) {
		t.Fatalf("revoke bad id: %v, want ErrNotFound", err)
	}
	if _, err := store.LookupAPIKey(ctx, plain); !errors.Is(err, persistence.ErrNotFound) {
		t.Fatalf("lookup of revoked key: %v, want ErrNotFound", err)
	}

	keys, err := store.ListAPIKeys(ctx)
	if err != nil || len(keys) != 2 || keys[0].RevokedAt == nil {
		t.Fatalf("list = %+v, %v; want 2 keys, the first revoked", keys, err)
	}
}
