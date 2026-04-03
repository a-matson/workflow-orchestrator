-- 001_initial_schema.sql
-- Workflow Orchestration Platform — PostgreSQL schema

CREATE EXTENSION IF NOT EXISTS "uuid-ossp";

-- ==================== Workflow Definitions ====================
CREATE TABLE IF NOT EXISTS workflow_definitions (
    id            TEXT PRIMARY KEY,
    name          TEXT NOT NULL,
    description   TEXT,
    version       TEXT NOT NULL DEFAULT '1.0.0',
    tasks         JSONB NOT NULL DEFAULT '[]',
    max_parallel  INTEGER NOT NULL DEFAULT 10,
    tags          JSONB NOT NULL DEFAULT '{}',
    created_at    TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at    TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_workflow_defs_name ON workflow_definitions(name);
CREATE INDEX IF NOT EXISTS idx_workflow_defs_created ON workflow_definitions(created_at DESC);

-- ==================== Workflow Executions ====================
CREATE TABLE IF NOT EXISTS workflow_executions (
    id               TEXT PRIMARY KEY,
    workflow_id      TEXT NOT NULL REFERENCES workflow_definitions(id),
    workflow_name    TEXT NOT NULL,
    status           TEXT NOT NULL DEFAULT 'pending',
    trigger_payload  JSONB,
    metadata         JSONB,
    error            TEXT,
    started_at       TIMESTAMPTZ,
    completed_at     TIMESTAMPTZ,
    created_at       TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at       TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_workflow_execs_workflow_id ON workflow_executions(workflow_id);
CREATE INDEX IF NOT EXISTS idx_workflow_execs_status ON workflow_executions(status);
CREATE INDEX IF NOT EXISTS idx_workflow_execs_created ON workflow_executions(created_at DESC);

-- ==================== Task Executions ====================
CREATE TABLE IF NOT EXISTS task_executions (
    id                   TEXT PRIMARY KEY,
    workflow_exec_id     TEXT NOT NULL REFERENCES workflow_executions(id),
    task_definition_id   TEXT NOT NULL,
    task_name            TEXT NOT NULL,
    task_type            TEXT NOT NULL,
    status               TEXT NOT NULL DEFAULT 'pending',
    retry_count          INTEGER NOT NULL DEFAULT 0,
    max_retries          INTEGER NOT NULL DEFAULT 0,
    worker_id            TEXT,
    queued_at            TIMESTAMPTZ,
    started_at           TIMESTAMPTZ,
    completed_at         TIMESTAMPTZ,
    next_retry_at        TIMESTAMPTZ,
    output               JSONB,
    error                TEXT,
    logs                 JSONB DEFAULT '[]',
    metadata             JSONB DEFAULT '{}',
    created_at           TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at           TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_task_execs_workflow_exec ON task_executions(workflow_exec_id);
CREATE INDEX IF NOT EXISTS idx_task_execs_status ON task_executions(status);
CREATE INDEX IF NOT EXISTS idx_task_execs_retry ON task_executions(status, next_retry_at) WHERE status = 'retrying';
CREATE INDEX IF NOT EXISTS idx_task_execs_worker ON task_executions(worker_id) WHERE worker_id IS NOT NULL;

-- ==================== Idempotency Keys ====================
CREATE TABLE IF NOT EXISTS idempotency_keys (
    key        TEXT PRIMARY KEY,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    expires_at TIMESTAMPTZ NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_idempotency_expires ON idempotency_keys(expires_at);

-- ==================== Auto-update triggers ====================
CREATE OR REPLACE FUNCTION update_updated_at()
RETURNS TRIGGER AS $$
BEGIN
    NEW.updated_at = NOW();
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER workflow_definitions_updated_at
    BEFORE UPDATE ON workflow_definitions
    FOR EACH ROW EXECUTE FUNCTION update_updated_at();

CREATE TRIGGER workflow_executions_updated_at
    BEFORE UPDATE ON workflow_executions
    FOR EACH ROW EXECUTE FUNCTION update_updated_at();

CREATE TRIGGER task_executions_updated_at
    BEFORE UPDATE ON task_executions
    FOR EACH ROW EXECUTE FUNCTION update_updated_at();
