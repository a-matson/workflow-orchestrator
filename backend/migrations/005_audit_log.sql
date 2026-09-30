-- 005_audit_log.sql
-- Who did what. Rows carry identifiers only, never request payloads, which
-- may hold secrets. IF NOT EXISTS: see 004.

CREATE TABLE IF NOT EXISTS audit_log (
  id          BIGSERIAL PRIMARY KEY,
  at          TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  actor_key_id UUID,
  actor_name  TEXT NOT NULL,
  action      TEXT NOT NULL,
  target_type TEXT NOT NULL,
  target_id   TEXT NOT NULL,
  request_id  TEXT NOT NULL,
  outcome     TEXT NOT NULL CHECK (outcome IN ('success', 'denied', 'error'))
);

-- Serves GET /api/audit, which always reads newest first. There is no
-- retention yet (planned in F10), so the table only grows.
CREATE INDEX IF NOT EXISTS idx_audit_log_at ON audit_log (at DESC);
