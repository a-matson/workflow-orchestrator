.PHONY: all dev dev-infra dev-backend dev-frontend build build-backend build-frontend \
	docker-build docker-up docker-down docker-logs migrate migrate-reset \
	test test-dag test-orchestrator test-integration check lint clean load-test hooks

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

test-integration:
	cd backend && POSTGRES_URL="$(DB_URL)" REDIS_ADDR="$(REDIS)" \
	  go test -race -count=1 -tags integration ./...

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
	@echo "Triggering 20 workflow executions in parallel..."
	@for i in $$(seq 1 20); do \
	  curl -sX POST http://localhost:8080/api/workflows/$(WF_ID)/trigger \
	    -H 'Content-Type: application/json' \
	    -d '{"test_run": '$$i'}' & \
	done; wait
	@echo "Load test complete."
