-- 004_api_keys.sql
-- API keys for bearer authentication. Only the SHA-256 of each key is stored.
-- IF NOT EXISTS: databases once initialised by the initdb mount rerun every file.

CREATE TABLE IF NOT EXISTS api_keys (
  id           UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  name         TEXT NOT NULL,
  role         TEXT NOT NULL CHECK (role IN ('admin', 'operator', 'viewer')),
  key_hash     TEXT NOT NULL UNIQUE,
  created_at   TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  last_used_at TIMESTAMPTZ,
  revoked_at   TIMESTAMPTZ
);
