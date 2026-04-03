-- 002_performance_indexes.sql
-- Additional indexes for high-throughput query patterns

-- Fast lookup of running tasks by worker (for heartbeat/timeout detection)
CREATE INDEX IF NOT EXISTS idx_task_execs_running_worker
  ON task_executions(worker_id, started_at)
  WHERE status = 'running';

-- Efficient dead-letter queue queries
CREATE INDEX IF NOT EXISTS idx_task_execs_dead_letter
  ON task_executions(workflow_exec_id, updated_at)
  WHERE status = 'dead_letter';

-- Fast count of active executions per workflow (for rate limiting)
CREATE INDEX IF NOT EXISTS idx_workflow_execs_active
  ON workflow_executions(workflow_id, status, created_at)
  WHERE status IN ('running', 'pending');

-- Log retrieval by task (for streaming)
CREATE INDEX IF NOT EXISTS idx_task_execs_logs
  ON task_executions USING gin(logs)
  WHERE logs IS NOT NULL;

-- Tag-based workflow search
CREATE INDEX IF NOT EXISTS idx_workflow_defs_tags
  ON workflow_definitions USING gin(tags);

-- Full-text search on workflow names
CREATE INDEX IF NOT EXISTS idx_workflow_defs_name_trgm
  ON workflow_definitions USING gin(name gin_trgm_ops);

-- ==================== Views ====================

-- Execution summary view (used by list endpoints)
CREATE OR REPLACE VIEW execution_summaries AS
SELECT
  we.id,
  we.workflow_id,
  we.workflow_name,
  we.status,
  we.started_at,
  we.completed_at,
  we.error,
  we.created_at,
  we.updated_at,
  COUNT(te.id) AS total_tasks,
  COUNT(te.id) FILTER (WHERE te.status = 'completed') AS completed_tasks,
  COUNT(te.id) FILTER (WHERE te.status = 'failed' OR te.status = 'dead_letter') AS failed_tasks,
  COUNT(te.id) FILTER (WHERE te.status = 'running') AS running_tasks,
  COUNT(te.id) FILTER (WHERE te.status = 'retrying') AS retrying_tasks,
  EXTRACT(EPOCH FROM (COALESCE(we.completed_at, NOW()) - we.started_at)) AS duration_seconds
FROM workflow_executions we
LEFT JOIN task_executions te ON te.workflow_exec_id = we.id
GROUP BY we.id;

-- Worker activity view
CREATE OR REPLACE VIEW worker_activity AS
SELECT
  worker_id,
  COUNT(*) AS tasks_processed,
  COUNT(*) FILTER (WHERE status = 'completed') AS tasks_completed,
  COUNT(*) FILTER (WHERE status IN ('failed', 'dead_letter')) AS tasks_failed,
  AVG(EXTRACT(EPOCH FROM (completed_at - started_at))) FILTER (WHERE completed_at IS NOT NULL AND started_at IS NOT NULL) AS avg_duration_seconds,
  MAX(updated_at) AS last_seen
FROM task_executions
WHERE worker_id IS NOT NULL
GROUP BY worker_id;

-- ==================== Functions ====================

-- Cleanup function: archive old completed executions
CREATE OR REPLACE FUNCTION archive_old_executions(retention_days INTEGER DEFAULT 30)
RETURNS INTEGER AS $$
DECLARE
  deleted_count INTEGER;
BEGIN
  DELETE FROM workflow_executions
  WHERE status IN ('completed', 'failed', 'cancelled')
    AND completed_at < NOW() - (retention_days || ' days')::INTERVAL;
  GET DIAGNOSTICS deleted_count = ROW_COUNT;
  RETURN deleted_count;
END;
$$ LANGUAGE plpgsql;
