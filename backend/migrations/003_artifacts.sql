-- 003_artifacts.sql
-- Adds artifact tracking and container spec columns to support
-- container-per-task isolation and MinIO artifact storage.

-- ── task_executions additions ─────────────────────────────────────────────────

ALTER TABLE task_executions
  ADD COLUMN IF NOT EXISTS artifacts_in  JSONB NOT NULL DEFAULT '[]',
  ADD COLUMN IF NOT EXISTS artifacts_out JSONB NOT NULL DEFAULT '[]';

COMMENT ON COLUMN task_executions.artifacts_in  IS
  'ResolvedArtifact[] — MinIO keys downloaded into the task container before execution';
COMMENT ON COLUMN task_executions.artifacts_out IS
  'ResolvedArtifact[] — MinIO keys uploaded from the task container after execution';

-- Index for artifact look-up by workflow (used when resolving downstream deps)
CREATE INDEX IF NOT EXISTS idx_task_execs_artifacts
  ON task_executions USING gin(artifacts_out)
  WHERE artifacts_out != '[]';

-- ── artifacts catalogue ───────────────────────────────────────────────────────
-- Optional dedicated table for fast listing without scanning JSONB columns.

CREATE TABLE IF NOT EXISTS artifacts (
  id               TEXT PRIMARY KEY DEFAULT gen_random_uuid()::text,
  workflow_exec_id TEXT NOT NULL REFERENCES workflow_executions(id) ON DELETE CASCADE,
  task_exec_id     TEXT NOT NULL REFERENCES task_executions(id)     ON DELETE CASCADE,
  task_def_id      TEXT NOT NULL,
  minio_key        TEXT NOT NULL UNIQUE,
  path             TEXT NOT NULL,   -- relative workspace path
  size_bytes       BIGINT NOT NULL DEFAULT 0,
  created_at       TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_artifacts_workflow ON artifacts(workflow_exec_id);
CREATE INDEX IF NOT EXISTS idx_artifacts_task     ON artifacts(task_exec_id);
CREATE INDEX IF NOT EXISTS idx_artifacts_key      ON artifacts(minio_key);

COMMENT ON TABLE artifacts IS
  'Catalogue of all files produced by task executions and stored in MinIO.';
