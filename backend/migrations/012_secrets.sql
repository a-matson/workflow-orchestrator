-- 012_secrets.sql
-- Workflow secrets, sealed with AES-256-GCM (internal/secrets); the key never
-- reaches the database. IF NOT EXISTS: see 004.

CREATE TABLE IF NOT EXISTS secrets (
  name       TEXT PRIMARY KEY,
  nonce      BYTEA NOT NULL,
  ciphertext BYTEA NOT NULL,
  created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
