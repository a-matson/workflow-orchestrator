#!/bin/sh
# One-shot local dev setup. The backend applies migrations on startup, so none run here.
set -eu
cd "$(dirname "$0")/.."

for cmd in go node npm docker; do
  command -v "$cmd" >/dev/null 2>&1 || { echo "ERROR: $cmd is required but not installed." >&2; exit 1; }
done

if [ ! -f .env ]; then
  cp .env.example .env
  echo "Created .env from .env.example. Review the secrets in it, then re-run this script."
  exit 1
fi

# --wait blocks on healthchecks; MinIO must be up or the backend falls back to in-process tasks.
docker compose up -d --wait postgres redis minio
(cd backend && go mod download)
(cd frontend && npm ci)

cat <<'MSG'

Setup complete. Start development:
  make dev                              # backend + frontend together
  cd backend && go run ./cmd/server     # or the backend alone
  cd frontend && npm run dev            # or the frontend alone
UI: http://localhost:5173   API: http://localhost:8080
MSG
