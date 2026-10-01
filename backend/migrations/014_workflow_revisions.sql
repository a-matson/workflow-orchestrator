-- 014_workflow_revisions.sql
-- Every saved state of a workflow definition, append-only. "Revision" rather
-- than "version": the definition already has a user-set version label.
-- IF NOT EXISTS / ON CONFLICT: see 004.

CREATE TABLE IF NOT EXISTS workflow_revisions (
  workflow_id TEXT NOT NULL REFERENCES workflow_definitions(id) ON DELETE CASCADE,
  revision    INTEGER NOT NULL CHECK (revision > 0),
  definition  JSONB NOT NULL,
  created_at  TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  PRIMARY KEY (workflow_id, revision)
);

-- Existing workflows start at revision 1, their current state. The keys match
-- models.WorkflowDefinition's JSON names; stripped nulls are omitted fields.
INSERT INTO workflow_revisions (workflow_id, revision, definition, created_at)
SELECT id, 1, jsonb_strip_nulls(jsonb_build_object(
    'id', id, 'name', name, 'description', description, 'version', version,
    'tasks', tasks, 'max_parallel', max_parallel, 'tags', tags,
    'global_retry', global_retry, 'schedule', schedule, 'next_run_at', next_run_at, 'alerts', alerts,
    'created_at', created_at, 'updated_at', updated_at)), updated_at
FROM workflow_definitions
ON CONFLICT DO NOTHING;
