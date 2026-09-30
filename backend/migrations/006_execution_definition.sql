-- 006_execution_definition.sql
-- The definition an execution started with, so recovery resumes it unchanged
-- after the stored definition is edited. NULL on executions created before
-- this column; recovery falls back to the stored definition for those.
-- IF NOT EXISTS: see 004.

ALTER TABLE workflow_executions ADD COLUMN IF NOT EXISTS definition JSONB;
