package persistence

import (
	"context"
	"fmt"
	"time"
)

// AuditEntry records one attempted mutation. It has no payload field on
// purpose: request bodies may carry secrets.
type AuditEntry struct {
	ID         int64     `json:"id"`
	At         time.Time `json:"at"`
	ActorKeyID *string   `json:"actor_key_id,omitempty"`
	ActorName  string    `json:"actor_name"`
	Action     string    `json:"action"`
	TargetType string    `json:"target_type"`
	TargetID   string    `json:"target_id"`
	RequestID  string    `json:"request_id"`
	Outcome    string    `json:"outcome"`
}

// RecordAudit appends e; ID and At are assigned by the database.
func (s *Store) RecordAudit(ctx context.Context, e AuditEntry) error {
	_, err := s.pool.Exec(ctx,
		`INSERT INTO audit_log (actor_key_id, actor_name, action, target_type, target_id, request_id, outcome)
		 VALUES ($1, $2, $3, $4, $5, $6, $7)`,
		e.ActorKeyID, e.ActorName, e.Action, e.TargetType, e.TargetID, e.RequestID, e.Outcome)
	if err != nil {
		return fmt.Errorf("recording audit entry: %w", err)
	}
	return nil
}

// ListAudit returns entries newest first.
func (s *Store) ListAudit(ctx context.Context, limit, offset int) ([]AuditEntry, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT id, at, actor_key_id::text, actor_name, action, target_type, target_id, request_id, outcome
		 FROM audit_log ORDER BY at DESC, id DESC LIMIT $1 OFFSET $2`, limit, offset)
	if err != nil {
		return nil, fmt.Errorf("listing audit log: %w", err)
	}
	defer rows.Close()
	entries := []AuditEntry{}
	for rows.Next() {
		var e AuditEntry
		if err := rows.Scan(&e.ID, &e.At, &e.ActorKeyID, &e.ActorName, &e.Action, &e.TargetType, &e.TargetID, &e.RequestID, &e.Outcome); err != nil {
			return nil, fmt.Errorf("scanning audit entry: %w", err)
		}
		entries = append(entries, e)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("listing audit log: %w", err)
	}
	return entries, nil
}

// DeleteAuditBefore deletes audit entries older than cutoff and returns how many.
func (s *Store) DeleteAuditBefore(ctx context.Context, cutoff time.Time) (int64, error) {
	tag, err := s.pool.Exec(ctx, `DELETE FROM audit_log WHERE at < $1`, cutoff)
	if err != nil {
		return 0, fmt.Errorf("deleting old audit entries: %w", err)
	}
	return tag.RowsAffected(), nil
}
