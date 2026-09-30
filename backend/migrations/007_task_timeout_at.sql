-- 007_task_timeout_at.sql
-- When a running attempt is given up on if no result has arrived: set at
-- pickup, read by the timeout reaper. Stale on rows that left running; the
-- reaper only reads running rows. IF NOT EXISTS: see 004.

ALTER TABLE task_executions ADD COLUMN IF NOT EXISTS timeout_at TIMESTAMPTZ;

CREATE INDEX IF NOT EXISTS idx_task_execs_timeout ON task_executions (timeout_at) WHERE status = 'running';
