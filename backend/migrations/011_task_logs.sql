-- 011_task_logs.sql
-- Task logs move from a JSONB array on each task row into their own table, so
-- a long log no longer rewrites the row it belongs to and old runs can be
-- deleted with their logs. task_executions.logs stays, emptied and unused:
-- 002 reruns on databases once initialised by the initdb mount and indexes it.
-- IF NOT EXISTS / NULL-guarded copy: see 004; a rerun copies nothing twice.

CREATE TABLE IF NOT EXISTS task_logs (
  id           BIGSERIAL PRIMARY KEY,
  task_exec_id TEXT NOT NULL REFERENCES task_executions(id) ON DELETE CASCADE,
  attempt      INTEGER NOT NULL DEFAULT 0,
  logged_at    TIMESTAMPTZ NOT NULL,
  level        TEXT NOT NULL,
  message      TEXT NOT NULL,
  fields       JSONB
);
CREATE INDEX IF NOT EXISTS idx_task_logs_task ON task_logs (task_exec_id, id);

INSERT INTO task_logs (task_exec_id, attempt, logged_at, level, message, fields)
SELECT t.id, COALESCE((e->>'attempt')::int, 0), COALESCE((e->>'timestamp')::timestamptz, t.updated_at),
       COALESCE(e->>'level', 'info'), COALESCE(e->>'message', ''), e->'fields'
FROM task_executions t, jsonb_array_elements(t.logs) WITH ORDINALITY AS x(e, n)
WHERE jsonb_typeof(t.logs) = 'array'
ORDER BY t.id, x.n;

UPDATE task_executions SET logs = NULL WHERE logs IS NOT NULL;
ALTER TABLE task_executions ALTER COLUMN logs DROP DEFAULT;
DROP INDEX IF EXISTS idx_task_execs_logs;

-- A run owns its tasks, so deleting an old run (retention) deletes them too.
ALTER TABLE task_executions DROP CONSTRAINT IF EXISTS task_executions_workflow_exec_id_fkey;
ALTER TABLE task_executions ADD CONSTRAINT task_executions_workflow_exec_id_fkey
  FOREIGN KEY (workflow_exec_id) REFERENCES workflow_executions(id) ON DELETE CASCADE;
