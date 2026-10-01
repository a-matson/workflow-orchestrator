-- 015_webhook_trigger.sql
-- The secret that signs a definition's webhook triggers
-- (models.WebhookTrigger). NULL means webhooks cannot start it.
-- IF NOT EXISTS: see 004.

ALTER TABLE workflow_definitions ADD COLUMN IF NOT EXISTS webhook JSONB;
