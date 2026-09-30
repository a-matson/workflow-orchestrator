package persistence

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/rs/zerolog/log"
)

// ErrMalformedAPIKey means a string cannot be an API key this server issued.
var ErrMalformedAPIKey = errors.New("malformed API key")

const (
	apiKeyPrefix = "flx_"
	apiKeyBytes  = 32
	// lastUsedGranularity bounds last_used_at writes to one per key per
	// minute, so authentication does not cost a row update per request.
	lastUsedGranularity = time.Minute
)

// APIKey is an API key's metadata. It has no field for the key or its hash,
// so neither can leak through a listing or a JSON response.
type APIKey struct {
	ID         string     `json:"id"`
	Name       string     `json:"name"`
	Role       string     `json:"role"`
	CreatedAt  time.Time  `json:"created_at"`
	LastUsedAt *time.Time `json:"last_used_at,omitempty"`
	RevokedAt  *time.Time `json:"revoked_at,omitempty"`
}

// ValidAPIKey reports whether plaintext has the shape of an issued key.
func ValidAPIKey(plaintext string) bool {
	raw, ok := strings.CutPrefix(plaintext, apiKeyPrefix)
	if !ok {
		return false
	}
	b, err := base64.RawURLEncoding.DecodeString(raw)
	return err == nil && len(b) == apiKeyBytes
}

// hashAPIKey is a plain SHA-256: keys carry 256 random bits, so a slow KDF
// adds no protection against guessing and would only slow every request.
func hashAPIKey(plaintext string) string {
	sum := sha256.Sum256([]byte(plaintext))
	return hex.EncodeToString(sum[:])
}

const apiKeyColumns = `id, name, role, created_at, last_used_at, revoked_at`

func scanAPIKey(row pgx.Row) (*APIKey, error) {
	var k APIKey
	if err := row.Scan(&k.ID, &k.Name, &k.Role, &k.CreatedAt, &k.LastUsedAt, &k.RevokedAt); err != nil {
		return nil, err
	}
	return &k, nil
}

// CreateAPIKey issues a new key. The plaintext is returned once and never stored.
func (s *Store) CreateAPIKey(ctx context.Context, name, role string) (string, *APIKey, error) {
	b := make([]byte, apiKeyBytes)
	if _, err := rand.Read(b); err != nil {
		return "", nil, fmt.Errorf("generating API key: %w", err)
	}
	plaintext := apiKeyPrefix + base64.RawURLEncoding.EncodeToString(b)
	k, err := scanAPIKey(s.pool.QueryRow(ctx,
		`INSERT INTO api_keys (name, role, key_hash) VALUES ($1, $2, $3) RETURNING `+apiKeyColumns,
		name, role, hashAPIKey(plaintext)))
	if err != nil {
		return "", nil, fmt.Errorf("inserting API key: %w", err)
	}
	return plaintext, k, nil
}

// EnsureAPIKey stores plaintext as a key unless its hash already exists; a
// revoked key stays revoked. It is for operator-supplied bootstrap keys.
func (s *Store) EnsureAPIKey(ctx context.Context, name, role, plaintext string) error {
	if !ValidAPIKey(plaintext) {
		return ErrMalformedAPIKey
	}
	if _, err := s.pool.Exec(ctx,
		`INSERT INTO api_keys (name, role, key_hash) VALUES ($1, $2, $3) ON CONFLICT (key_hash) DO NOTHING`,
		name, role, hashAPIKey(plaintext)); err != nil {
		return fmt.Errorf("inserting API key: %w", err)
	}
	return nil
}

// LookupAPIKey returns the unrevoked key matching plaintext, or ErrNotFound.
// The lookup is by hash through the unique index, so there is no byte-wise
// comparison of secrets whose timing could leak a prefix.
func (s *Store) LookupAPIKey(ctx context.Context, plaintext string) (*APIKey, error) {
	if !ValidAPIKey(plaintext) {
		return nil, ErrNotFound
	}
	k, err := scanAPIKey(s.pool.QueryRow(ctx,
		`SELECT `+apiKeyColumns+` FROM api_keys WHERE key_hash = $1 AND revoked_at IS NULL`,
		hashAPIKey(plaintext)))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("looking up API key: %w", err)
	}
	if k.LastUsedAt == nil || time.Since(*k.LastUsedAt) >= lastUsedGranularity {
		// Usage telemetry must not turn a valid key into a failed request.
		if _, err := s.pool.Exec(ctx, `UPDATE api_keys SET last_used_at = NOW() WHERE id = $1`, k.ID); err != nil {
			log.Warn().Err(err).Str("api_key_id", k.ID).Msg("recording API key use failed")
		}
	}
	return k, nil
}

// APIKeyActive reports whether the key with id exists and is not revoked.
func (s *Store) APIKeyActive(ctx context.Context, id string) (bool, error) {
	uid, err := uuid.Parse(id)
	if err != nil {
		return false, nil
	}
	var active bool
	if err := s.pool.QueryRow(ctx,
		`SELECT EXISTS (SELECT 1 FROM api_keys WHERE id = $1 AND revoked_at IS NULL)`, uid,
	).Scan(&active); err != nil {
		return false, fmt.Errorf("checking API key: %w", err)
	}
	return active, nil
}

// RevokeAPIKey revokes the key with id, or returns ErrNotFound if there is no
// such unrevoked key.
func (s *Store) RevokeAPIKey(ctx context.Context, id string) error {
	uid, err := uuid.Parse(id)
	if err != nil {
		return ErrNotFound
	}
	tag, err := s.pool.Exec(ctx,
		`UPDATE api_keys SET revoked_at = NOW() WHERE id = $1 AND revoked_at IS NULL`, uid)
	if err != nil {
		return fmt.Errorf("revoking API key: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// ListAPIKeys returns every key, revoked ones included, oldest first.
func (s *Store) ListAPIKeys(ctx context.Context) ([]*APIKey, error) {
	rows, err := s.pool.Query(ctx, `SELECT `+apiKeyColumns+` FROM api_keys ORDER BY created_at, id`)
	if err != nil {
		return nil, fmt.Errorf("listing API keys: %w", err)
	}
	defer rows.Close()
	keys := []*APIKey{}
	for rows.Next() {
		k, err := scanAPIKey(rows)
		if err != nil {
			return nil, fmt.Errorf("scanning API key: %w", err)
		}
		keys = append(keys, k)
	}
	return keys, rows.Err()
}

// CountActiveAPIKeys counts unrevoked keys.
func (s *Store) CountActiveAPIKeys(ctx context.Context) (int, error) {
	var n int
	if err := s.pool.QueryRow(ctx, `SELECT COUNT(*) FROM api_keys WHERE revoked_at IS NULL`).Scan(&n); err != nil {
		return 0, fmt.Errorf("counting API keys: %w", err)
	}
	return n, nil
}
