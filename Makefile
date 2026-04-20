.PHONY: all dev build clean test proto migrate lint

# ── Variables ─────────────────────────────────────────────
GO_CMD       = ./cmd/server
BINARY       = ./bin/workflow-server
PROTO_DIR    = ./backend/proto
PROTO_OUT    = ./backend/proto/gen
FRONTEND_DIR = ./frontend
MIGRATION_DIR = ./backend/migrations

DB_URL ?= postgres://workflow:workflow@localhost:5433/workflow?sslmode=disable
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
	  go run ./cmd/server

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
	@echo "Running migrations..."
	@for f in $(MIGRATION_DIR)/*.sql; do \
	  echo "Applying $$f..."; \
	  psql "$(DB_URL)" -f "$$f"; \
	done
	@echo "Migrations complete."

migrate-reset:
	psql "$(DB_URL)" -c "DROP SCHEMA public CASCADE; CREATE SCHEMA public;"
	@make migrate

# ── gRPC codegen ─────────────────────────────────────────
proto:
	@mkdir -p $(PROTO_OUT)
	protoc \
	  --go_out=$(PROTO_OUT) --go_opt=paths=source_relative \
	  --go-grpc_out=$(PROTO_OUT) --go-grpc_opt=paths=source_relative \
	  -I $(PROTO_DIR) \
	  $(PROTO_DIR)/orchestrator.proto
	@echo "Proto files generated in $(PROTO_OUT)"

# ── Testing ──────────────────────────────────────────────
test:
	cd backend && go test ./... -v -race -cover

test-dag:
	cd backend && go test ./internal/dag/... -v

test-orchestrator:
	cd backend && go test ./internal/orchestrator/... -v

test-integration:
	cd backend && POSTGRES_URL="$(DB_URL)" REDIS_ADDR="$(REDIS)" \
	  go test ./... -tags=integration -v

# ── Linting ──────────────────────────────────────────────
lint:
	cd backend && golangci-lint run ./...
	cd frontend && npm run type-check

# ── Cleanup ──────────────────────────────────────────────
clean:
	rm -rf bin/
	rm -rf frontend/dist/
	rm -rf backend/proto/gen/
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
