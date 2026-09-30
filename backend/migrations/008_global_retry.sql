-- 008_global_retry.sql
-- The definition's workflow-wide retry policy; it was accepted by the API but
-- never stored. NULL means none, so tasks without their own policy get
-- retry.DefaultPolicy. IF NOT EXISTS: see 004.

ALTER TABLE workflow_definitions ADD COLUMN IF NOT EXISTS global_retry JSONB;
