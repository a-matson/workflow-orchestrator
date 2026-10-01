.PHONY: all dev dev-infra dev-backend dev-frontend build build-backend build-frontend \
	docker-build docker-up docker-down docker-logs migrate migrate-reset \
	test test-dag test-orchestrator test-integration e2e e2e-ui chaos check lint clean load-test hooks

# ── Variables ─────────────────────────────────────────────
GO_CMD       = ./cmd/server
BINARY       = ./bin/workflow-server
FRONTEND_DIR = ./frontend

# Compose has no default credentials; dev targets must use the same .env.
# Optional so CI (which sets its own env) still works without the file.
-include .env
POSTGRES_PASSWORD ?= workflow
ifdef MINIO_ROOT_USER
export MINIO_ACCESS_KEY = $(MINIO_ROOT_USER)
export MINIO_SECRET_KEY = $(MINIO_ROOT_PASSWORD)
endif
ifdef REDIS_PASSWORD
export REDIS_PASSWORD
endif

DB_URL ?= postgres://workflow:$(POSTGRES_PASSWORD)@localhost:5433/workflow?sslmode=disable
REDIS  ?= localhost:6379

# ── Default ───────────────────────────────────────────────
all: build

# ── Development ───────────────────────────────────────────
dev:
	@echo "Starting all services in dev mode..."
	@make -j3 dev-backend dev-frontend dev-infra

dev-infra:
	docker compose up postgres redis minio --wait

dev-backend:
	cd backend && POSTGRES_URL="$(DB_URL)" REDIS_ADDR="$(REDIS)" \
	  FLUXOR_ALLOWED_ORIGINS=http://localhost:5173 go run ./cmd/server

dev-frontend:
	cd frontend && npm install && npm run dev

# ── Build ─────────────────────────────────────────────────
build: build-backend build-frontend

build-backend:
	@mkdir -p bin
	cd backend && CGO_ENABLED=0 GOOS=linux go build \
	  -ldflags="-w -s -X main.version=$$(git describe --tags --always)" \
	  -o ../$(BINARY) ./cmd/server
	@echo "Built: $(BINARY)"

build-frontend:
	cd $(FRONTEND_DIR) && npm ci && npm run build
	@echo "Frontend built to $(FRONTEND_DIR)/dist"

# ── Docker ───────────────────────────────────────────────
docker-build:
	docker build -t workflow-backend:latest ./backend
	docker build -t workflow-frontend:latest ./frontend

docker-up:
	docker compose up -d --build

docker-down:
	docker compose down

docker-logs:
	docker compose logs -f backend

# ── Database ─────────────────────────────────────────────
migrate:
	cd backend && POSTGRES_URL="$(DB_URL)" go run ./cmd/migrate

migrate-reset:
	psql "$(DB_URL)" -c "DROP SCHEMA public CASCADE; CREATE SCHEMA public;"
	@make migrate

# ── Testing ──────────────────────────────────────────────
test:
	cd backend && go test ./... -v -race -cover

test-dag:
	cd backend && go test ./internal/dag/... -v

test-orchestrator:
	cd backend && go test -tags integration ./internal/orchestrator/... -v

# Brings up its own throwaway Postgres and Redis, and always tears them down,
# keeping the test exit status.
TEST_COMPOSE = docker compose -f docker-compose.test.yml -p fluxor-test
test-integration:
	@status=0; \
	$(TEST_COMPOSE) up -d --wait && \
	  (cd backend && \
	    POSTGRES_URL="postgres://workflow:workflow@127.0.0.1:55432/workflow_test?sslmode=disable" \
	    REDIS_ADDR=127.0.0.1:56379 FLUXOR_IT=1 \
	    go test -race -count=1 -tags integration ./...) || status=$$?; \
	$(TEST_COMPOSE) down -v; \
	exit $$status

# A separate compose project with the committed example env, so the run never
# touches the developer's stack or volumes (exported shell variables still
# override the env file). Logs are dumped on failure because CI tears the stack
# down before anyone can inspect it. The workspace volume has a fixed name, so
# e2e gets its own to keep `down -v` off the dev stack's volume.
# Its own subnet lets it run beside the dev stack (Docker rejects overlapping networks).
E2E_COMPOSE = FLUXOR_WORKSPACE_VOLUME=fluxor-e2e-task-workspaces \
	FLUXOR_INTERNAL_SUBNET=172.29.251.0/24 FLUXOR_PROXY_IP=172.29.251.10 \
	FLUXOR_RATE_LIMIT_PER_MIN=5000 \
	COMPOSE_PROFILES=tracing OTEL_EXPORTER_OTLP_ENDPOINT=http://jaeger:4317 OTEL_BSP_SCHEDULE_DELAY=200 \
	docker compose --env-file .env.example -p fluxor-e2e
# Runs $(1) against a fresh e2e stack. Each run mints its own admin key, so no
# credential is committed or reused.
define e2e_stack
	@status=0; \
	FLUXOR_BOOTSTRAP_ADMIN_KEY="flx_$$(head -c 32 /dev/urandom | base64 | tr '+/' '-_' | tr -d '=\n')"; \
	FLUXOR_API_KEY="$$FLUXOR_BOOTSTRAP_ADMIN_KEY"; \
	FLUXOR_SECRETS_KEY="$$(head -c 32 /dev/urandom | base64)"; \
	export FLUXOR_BOOTSTRAP_ADMIN_KEY FLUXOR_API_KEY FLUXOR_SECRETS_KEY; \
	trap '$(E2E_COMPOSE) down -v' INT TERM; \
	$(E2E_COMPOSE) up -d --build --wait && \
	  $(1) || status=$$?; \
	[ $$status -eq 0 ] || $(E2E_COMPOSE) logs --no-color --tail=200; \
	$(E2E_COMPOSE) down -v; \
	exit $$status
endef

e2e:
	$(call e2e_stack,(cd backend && go test -tags e2e -count=1 -v ./e2e/) && examples/smoke-test.sh)

# Kills and restarts the backend 20 times during a DAG; minutes long, so it
# runs nightly (.github/workflows/chaos.yml) rather than on every PR.
chaos:
	$(call e2e_stack,(cd backend && go test -tags e2e,chaos -count=1 -v -timeout 20m -run TestChaos ./e2e/))

# Installing first keeps a slow npm or browser download out of the stack's lifetime.
# `A11Y_RECORD=1 make e2e-ui` rewrites e2e-ui/a11y-baseline.json.
e2e-ui:
	cd e2e-ui && npm ci && npx playwright install chromium
	$(call e2e_stack,(cd e2e-ui && npx playwright test))

# ── Linting ──────────────────────────────────────────────
lint:
	cd backend && golangci-lint run ./...
	cd frontend && npm run lint && npm run type-check

hooks:
	git config core.hooksPath .githooks

# gofmt -l exits 0 even when files need formatting, so test its output.
check:
	@test -z "$$(gofmt -l backend)" || { gofmt -l backend; echo "gofmt: files need formatting"; exit 1; }
	cd backend && go vet ./...
	cd backend && golangci-lint run ./...
	cd backend && go test -race -count=1 ./...
	cd frontend && npm run lint && npm run type-check && npm run test:unit

# ── Cleanup ──────────────────────────────────────────────
clean:
	rm -rf bin/
	rm -rf frontend/dist/
	@echo "Cleaned."

# ── Load testing ─────────────────────────────────────────
load-test:
	@: "$${FLUXOR_API_KEY:?set FLUXOR_API_KEY to an operator key}"
	@echo "Triggering 20 workflow executions in parallel..."
	@# The header arrives on stdin (-H @-), keeping the key out of ps output.
	@for i in $$(seq 1 20); do \
	  printf 'Authorization: Bearer %s\n' "$$FLUXOR_API_KEY" | \
	  curl -sX POST http://localhost:8080/api/workflows/$(WF_ID)/trigger -H @- \
	    -H 'Content-Type: application/json' \
	    -d '{"test_run": '$$i'}' & \
	done; wait
	@echo "Load test complete."
