package persistence

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
)

// PutSecret stores a sealed secret, replacing one with the same name.
func (s *Store) PutSecret(ctx context.Context, name string, nonce, ciphertext []byte) error {
	_, err := s.pool.Exec(ctx, `
		INSERT INTO secrets (name, nonce, ciphertext) VALUES ($1, $2, $3)
		ON CONFLICT (name) DO UPDATE SET nonce = EXCLUDED.nonce, ciphertext = EXCLUDED.ciphertext, updated_at = NOW()
	`, name, nonce, ciphertext)
	if err != nil {
		return fmt.Errorf("storing secret %q: %w", name, err)
	}
	return nil
}

// GetSecret returns a sealed secret, or ErrNotFound.
func (s *Store) GetSecret(ctx context.Context, name string) (nonce, ciphertext []byte, err error) {
	err = s.pool.QueryRow(ctx, `SELECT nonce, ciphertext FROM secrets WHERE name = $1`, name).Scan(&nonce, &ciphertext)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil, ErrNotFound
	}
	if err != nil {
		return nil, nil, fmt.Errorf("reading secret %q: %w", name, err)
	}
	return nonce, ciphertext, nil
}

// ListSecretNames returns every secret's name, sorted; never a value.
func (s *Store) ListSecretNames(ctx context.Context) ([]string, error) {
	rows, err := s.pool.Query(ctx, `SELECT name FROM secrets ORDER BY name`)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowTo[string])
}

// DeleteSecret removes a secret, returning ErrNotFound if there was none.
func (s *Store) DeleteSecret(ctx context.Context, name string) error {
	tag, err := s.pool.Exec(ctx, `DELETE FROM secrets WHERE name = $1`, name)
	if err != nil {
		return fmt.Errorf("deleting secret %q: %w", name, err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}
