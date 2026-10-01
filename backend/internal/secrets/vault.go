package secrets

import (
	"context"
	"fmt"
	"regexp"

	"github.com/a-matson/workflow-orchestrator/backend/internal/persistence"
)

// Names are referenced from templates as {{ secret "name" }}, so keep them plain.
var namePattern = regexp.MustCompile(`^[A-Za-z0-9_.-]{1,64}$`)

// MaxValueSize bounds one secret's value.
const MaxValueSize = 64 << 10

// Vault stores secrets sealed and opens them for the worker. A nil Vault
// means secrets are not configured; every method then returns ErrNotConfigured.
type Vault struct {
	store *persistence.Store
	box   *Box
}

func NewVault(store *persistence.Store, box *Box) *Vault { return &Vault{store: store, box: box} }

// ValidName reports whether name is a valid secret name.
func ValidName(name string) bool { return namePattern.MatchString(name) }

func (v *Vault) Set(ctx context.Context, name, value string) error {
	if v == nil {
		return ErrNotConfigured
	}
	if !ValidName(name) {
		return fmt.Errorf("secret name %q must match [A-Za-z0-9_.-], 1-64 characters", name)
	}
	if len(value) > MaxValueSize {
		return fmt.Errorf("secret %q is larger than %d bytes", name, MaxValueSize)
	}
	nonce, ct, err := v.box.Seal(name, []byte(value))
	if err != nil {
		return err
	}
	return v.store.PutSecret(ctx, name, nonce, ct)
}

// Secret returns a secret's value. Only the worker calls it, when it renders
// a task's config.
func (v *Vault) Secret(ctx context.Context, name string) (string, error) {
	if v == nil {
		return "", ErrNotConfigured
	}
	nonce, ct, err := v.store.GetSecret(ctx, name)
	if err != nil {
		return "", fmt.Errorf("secret %q: %w", name, err)
	}
	b, err := v.box.Open(name, nonce, ct)
	return string(b), err
}

func (v *Vault) Names(ctx context.Context) ([]string, error) {
	if v == nil {
		return nil, ErrNotConfigured
	}
	return v.store.ListSecretNames(ctx)
}

func (v *Vault) Delete(ctx context.Context, name string) error {
	if v == nil {
		return ErrNotConfigured
	}
	return v.store.DeleteSecret(ctx, name)
}
