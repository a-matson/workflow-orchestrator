-- 009_drop_unused_schema.sql
-- Objects no code reads or writes: the artifacts catalogue (artifacts live in
-- task_executions.artifacts_out), idempotency_keys, two reporting views, and
-- archive_old_executions, which could not run anyway (task_executions has no
-- ON DELETE CASCADE to workflow_executions). Retention is plan row F10.
-- IF EXISTS: databases once initialised by the initdb mount rerun every file.

DROP VIEW IF EXISTS execution_summaries;
DROP VIEW IF EXISTS worker_activity;
DROP FUNCTION IF EXISTS archive_old_executions(INTEGER);
DROP TABLE IF EXISTS artifacts;
DROP TABLE IF EXISTS idempotency_keys;
