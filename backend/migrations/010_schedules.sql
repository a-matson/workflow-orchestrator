-- 010_schedules.sql
-- A workflow's cron schedule and when it next fires. next_run_at is the
-- scheduler's compare-and-swap token: advancing it claims one run.
-- IF NOT EXISTS: see 004.

ALTER TABLE workflow_definitions ADD COLUMN IF NOT EXISTS schedule TEXT;
ALTER TABLE workflow_definitions ADD COLUMN IF NOT EXISTS next_run_at TIMESTAMPTZ;

CREATE INDEX IF NOT EXISTS idx_workflow_defs_next_run ON workflow_definitions (next_run_at) WHERE next_run_at IS NOT NULL;
