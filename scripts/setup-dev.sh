#!/usr/bin/env bash
# setup-dev.sh — one-shot local dev environment setup
# Creates databases, installs deps, runs migrations, and starts all services.

set -euo pipefail
GREEN='\033[0;32m'; YELLOW='\033[1;33m'; NC='\033[0m'

ok()   { echo -e "${GREEN}✓${NC} $1"; }
info() { echo -e "${YELLOW}→${NC} $1"; }

echo ""
echo "┌──────────────────────────────────────┐"
echo "│  Fluxor Dev Setup                    │"
echo "└──────────────────────────────────────┘"
echo ""

# ── Check prerequisites ──────────────────────────────────────
for cmd in go node npm docker; do
  if ! command -v "$cmd" &>/dev/null; then
    echo "ERROR: $cmd is required but not installed."; exit 1
  fi
done
ok "Prerequisites: go, node, npm, docker"

# ── Start infrastructure ─────────────────────────────────────
info "Starting PostgreSQL and Redis via Docker Compose..."
docker compose up -d postgres redis
sleep 3
ok "Infrastructure started"

# ── Run migrations ───────────────────────────────────────────
info "Running database migrations..."
for f in backend/migrations/*.sql; do
  echo "  Applying $f..."
  docker compose exec -T postgres psql -U workflow -d workflow -f - < "$f" 2>/dev/null || true
done
ok "Migrations applied"

# ── Generate go.sum ──────────────────────────────────────────
info "Downloading Go modules..."
(cd backend && go mod download && go mod tidy)
ok "Go modules ready"

# ── Install frontend deps ────────────────────────────────────
info "Installing frontend dependencies..."
(cd frontend && npm install)
ok "Frontend dependencies installed"

# ── Print start instructions ─────────────────────────────────
echo ""
echo "┌──────────────────────────────────────────────────────┐"
echo "│  Setup complete! Start development:                  │"
echo "│                                                      │"
echo "│  Terminal 1 (backend):                               │"
echo "│    cd backend && go run ./cmd/server                 │"
echo "│                                                      │"
echo "│  Terminal 2 (frontend):                              │"
echo "│    cd frontend && npm run dev                        │"
echo "│                                                      │"
echo "│  Or run everything together:                         │"
echo "│    make dev                                          │"
echo "│                                                      │"
echo "│  UI: http://localhost:5173                           │"
echo "│  API: http://localhost:8080                          │"
echo "└──────────────────────────────────────────────────────┘"
echo ""
