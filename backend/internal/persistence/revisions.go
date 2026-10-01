package persistence

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/a-matson/workflow-orchestrator/backend/internal/models"
)

// ListWorkflowRevisions returns a workflow's revisions, newest first.
func (s *Store) ListWorkflowRevisions(ctx context.Context, workflowID string, limit, offset int) ([]models.WorkflowRevision, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT revision, created_at FROM workflow_revisions
		WHERE workflow_id = $1 ORDER BY revision DESC LIMIT $2 OFFSET $3
	`, workflowID, limit, offset)
	if err != nil {
		return nil, err
	}
	revs, err := pgx.CollectRows(rows, pgx.RowToStructByPos[models.WorkflowRevision])
	if err != nil {
		return nil, fmt.Errorf("scanning revisions: %w", err)
	}
	return revs, nil
}

// GetWorkflowRevision returns the definition as it was saved at revision, or
// ErrNotFound.
func (s *Store) GetWorkflowRevision(ctx context.Context, workflowID string, revision int) (*models.WorkflowDefinition, error) {
	var raw []byte
	err := s.pool.QueryRow(ctx, `
		SELECT definition FROM workflow_revisions WHERE workflow_id = $1 AND revision = $2
	`, workflowID, revision).Scan(&raw)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("loading revision: %w", err)
	}
	var def models.WorkflowDefinition
	if err := json.Unmarshal(raw, &def); err != nil {
		return nil, fmt.Errorf("decoding revision: %w", err)
	}
	return &def, nil
}
