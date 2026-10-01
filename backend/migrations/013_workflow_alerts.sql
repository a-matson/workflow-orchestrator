-- 013_workflow_alerts.sql
-- Where a definition posts its runs' outcomes (models.WorkflowAlerts). NULL
-- means no alerts. Numbered after the unmerged 010-012 so the branches do not
-- collide. IF NOT EXISTS: see 004.

ALTER TABLE workflow_definitions ADD COLUMN IF NOT EXISTS alerts JSONB;
