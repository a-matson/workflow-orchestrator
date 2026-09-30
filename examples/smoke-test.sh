#!/usr/bin/env bash
# smoke-test.sh — end-to-end API smoke tests against a running Fluxor instance
#
# Usage:
#   chmod +x examples/smoke-test.sh
#   FLUXOR_API_KEY=flx_... BASE_URL=http://localhost:8080 ./examples/smoke-test.sh
#
# FLUXOR_API_KEY needs the operator role (see README "Authentication").

set -euo pipefail

BASE="${BASE_URL:-http://localhost:8080}"
: "${FLUXOR_API_KEY:?set FLUXOR_API_KEY to an operator or admin key}"
# curl reads the header from a 0600 file (-H @file), so the key never appears
# in a command line that `ps` would show. printf is a builtin, so it is safe too.
AUTH_FILE=$(mktemp)
trap 'rm -f "$AUTH_FILE"' EXIT
printf 'Authorization: Bearer %s\n' "$FLUXOR_API_KEY" > "$AUTH_FILE"
GREEN='\033[0;32m'
RED='\033[0;31m'
YELLOW='\033[1;33m'
NC='\033[0m'

ok()   { echo -e "${GREEN}✓${NC} $1"; }
fail() { echo -e "${RED}✗${NC} $1"; exit 1; }
info() { echo -e "${YELLOW}→${NC} $1"; }

echo ""
echo "════════════════════════════════════════════"
echo "  Fluxor Smoke Tests — $BASE"
echo "════════════════════════════════════════════"
echo ""

# ── Health check ───────────────────────────────────────────────
info "Health check..."
HEALTH=$(curl -sf "$BASE/api/health" | python3 -c "import sys,json; d=json.load(sys.stdin); print(d['status'])")
[ "$HEALTH" = "ok" ] && ok "Health: $HEALTH" || fail "Health check failed: $HEALTH"

# ── Create workflow ────────────────────────────────────────────
# The ETL tasks need external services, so this only checks the definition is accepted.
info "Creating ETL workflow..."
WF=$(curl -sf -H @"$AUTH_FILE" -X POST "$BASE/api/workflows" \
  -H "Content-Type: application/json" \
  -d @"$(dirname "$0")/etl-pipeline.json")

WF_ID=$(echo "$WF" | python3 -c "import sys,json; print(json.load(sys.stdin)['id'])")
WF_NAME=$(echo "$WF" | python3 -c "import sys,json; print(json.load(sys.stdin)['name'])")
ok "Created workflow: '$WF_NAME' ($WF_ID)"

# ── Get workflow ───────────────────────────────────────────────
info "Fetching workflow by ID..."
GOT=$(curl -sf -H @"$AUTH_FILE" "$BASE/api/workflows/$WF_ID")
GOT_ID=$(echo "$GOT" | python3 -c "import sys,json; print(json.load(sys.stdin)['id'])")
[ "$GOT_ID" = "$WF_ID" ] && ok "GET /api/workflows/$WF_ID → match" || fail "Workflow ID mismatch"

# ── List workflows ─────────────────────────────────────────────
info "Listing workflows..."
LIST=$(curl -sf -H @"$AUTH_FILE" "$BASE/api/workflows")
COUNT=$(echo "$LIST" | python3 -c "import sys,json; print(json.load(sys.stdin)['count'])")
[ "$COUNT" -ge 1 ] && ok "List workflows: $COUNT total" || fail "Expected at least 1 workflow"

# ── Create no-op workflow ──────────────────────────────────────
# A generic task without a command is a no-op, so this one can run to completion.
info "Creating no-op workflow..."
NOOP=$(curl -sf -H @"$AUTH_FILE" -X POST "$BASE/api/workflows" \
  -H "Content-Type: application/json" \
  -d '{"name":"smoke-noop","tasks":[{"id":"noop","name":"No-op","type":"generic","dependencies":[]}]}')
NOOP_ID=$(echo "$NOOP" | python3 -c "import sys,json; print(json.load(sys.stdin)['id'])")
ok "Created workflow: 'smoke-noop' ($NOOP_ID)"

# ── Trigger execution ──────────────────────────────────────────
info "Triggering workflow execution..."
EXEC=$(curl -sf -H @"$AUTH_FILE" -X POST "$BASE/api/workflows/$NOOP_ID/trigger" \
  -H "Content-Type: application/json" \
  -d '{"triggered_by": "smoke-test", "env": "test"}')

EXEC_ID=$(echo "$EXEC" | python3 -c "import sys,json; print(json.load(sys.stdin)['id'])")
EXEC_STATUS=$(echo "$EXEC" | python3 -c "import sys,json; print(json.load(sys.stdin)['status'])")
ok "Triggered execution: $EXEC_ID (status: $EXEC_STATUS)"

# ── Poll execution status ──────────────────────────────────────
info "Polling execution status (max 60s)..."
TIMEOUT=60
START=$(date +%s)
while true; do
  NOW=$(date +%s)
  ELAPSED=$((NOW - START))
  if [ $ELAPSED -ge $TIMEOUT ]; then
    fail "Execution timed out after ${TIMEOUT}s"
  fi

  STATUS=$(curl -sf -H @"$AUTH_FILE" "$BASE/api/executions/$EXEC_ID" | \
    python3 -c "import sys,json; print(json.load(sys.stdin)['status'])")

  case "$STATUS" in
    completed)
      ok "Execution completed in ${ELAPSED}s"
      break
      ;;
    failed)
      fail "Execution failed after ${ELAPSED}s"
      ;;
    running|pending)
      echo "  ... $STATUS (${ELAPSED}s elapsed)"
      sleep 3
      ;;
    *)
      fail "Unexpected status: $STATUS"
      ;;
  esac
done

# ── List tasks ─────────────────────────────────────────────────
info "Listing tasks for execution..."
TASKS=$(curl -sf -H @"$AUTH_FILE" "$BASE/api/executions/$EXEC_ID/tasks")
TASK_COUNT=$(echo "$TASKS" | python3 -c "import sys,json; print(json.load(sys.stdin)['count'])")
ok "Execution has $TASK_COUNT tasks"

# ── Metrics ────────────────────────────────────────────────────
info "Checking metrics..."
METRICS=$(curl -sf -H @"$AUTH_FILE" "$BASE/api/metrics")
STARTED=$(echo "$METRICS" | python3 -c "import sys,json; print(json.load(sys.stdin)['workflows_started'])")
ok "Metrics: workflows_started=$STARTED"

# ── List executions ────────────────────────────────────────────
info "Listing executions..."
EXECS=$(curl -sf -H @"$AUTH_FILE" "$BASE/api/executions?limit=10")
EXEC_COUNT=$(echo "$EXECS" | python3 -c "import sys,json; print(json.load(sys.stdin)['count'])")
ok "List executions: $EXEC_COUNT total"

# ── Retry execution ────────────────────────────────────────────
info "Retrying execution (replay test)..."
RETRY=$(curl -sf -H @"$AUTH_FILE" -X POST "$BASE/api/executions/$EXEC_ID/retry" \
  -H "Content-Type: application/json" -d '{}')
RETRY_ID=$(echo "$RETRY" | python3 -c "import sys,json; print(json.load(sys.stdin)['id'])")
[ "$RETRY_ID" != "$EXEC_ID" ] && ok "Retry created new execution: $RETRY_ID" || fail "Retry should create a new execution ID"

# ── WebSocket handshake ────────────────────────────────────────
# A bare upgrade request: 101 proves /ws accepts the key. curl then waits on
# the open socket, so --max-time ends it and its exit status is ignored.
info "Testing WebSocket handshake..."
WS_CODE=$(curl -s -o /dev/null -w '%{http_code}' --max-time 2 -H @"$AUTH_FILE" \
  -H 'Connection: Upgrade' -H 'Upgrade: websocket' -H 'Sec-WebSocket-Version: 13' \
  -H 'Sec-WebSocket-Key: dGhlIHNhbXBsZSBub25jZQ==' "$BASE/ws" || true)
[ "$WS_CODE" = "101" ] && ok "WebSocket handshake: 101" || fail "WebSocket handshake returned $WS_CODE"

echo ""
echo "════════════════════════════════════════════"
echo -e "  ${GREEN}All smoke tests passed!${NC}"
echo "════════════════════════════════════════════"
echo ""
