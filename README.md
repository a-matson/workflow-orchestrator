# Fluxor — Distributed Workflow Orchestration Platform

> DAG-based workflow execution with reliability, scalability, and real-time observability.

## Quick Start

```bash
git clone https://github.com/your-org/fluxor && cd fluxor
cd frontend && npm install && npm run build && cd ..
docker compose up -d
BASE_URL=http://localhost:8080 ./examples/smoke-test.sh
```

| Service | URL |
|---|---|
| **UI** | http://localhost:3000 |
| **REST API** | http://localhost:8080/api |
| **WebSocket** | ws://localhost:8080/ws |
| **gRPC** | localhost:9090 |
| **Redis UI** | http://localhost:8081 (profile: tools) |
| **Grafana** | http://localhost:3001 (profile: observability) |

## Architecture

```
┌──────────────────────────────────────────────────────────────────┐
│  Vue 3 + TS Frontend                                             │
│  DAG Builder (VueFlow) │ Executions │ Log Viewer │ Metrics       │
└──────────────────────────────┬───────────────────────────────────┘
                               │ REST + WebSocket
┌──────────────────────────────▼───────────────────────────────────┐
│  Go Backend                                                      │
│  REST /api/* │ WS Hub /ws │ gRPC :9090 │ Prometheus :9091        │
│                      ▼                                           │
│  ┌──────────────────────────────────────────────────────────┐    │
│  │  Orchestrator                                            │    │
│  │  • Kahn's topological sort  • Concurrency semaphores     │    │
│  │  • Dependency wave dispatch • Redis NX idempotency       │    │
│  │  • Exp backoff retry        • Dead-letter after max-retry│    │
│  └──────────────┬───────────────────────────┬───────────────┘    │
│                 │                           │                    │
│  ┌──────────────▼───────┐    ┌──────────────▼───────────────┐    │
│  │  PostgreSQL 16       │    │  Redis 7 Broker              │    │
│  │  executions/tasks    │    │  task queue (LIST BLPOP)     │    │
│  │  logs (JSONB)        │    │  retry ZSet (scored by time) │    │
│  │  definitions         │    │  dead_letter (LIST)          │    │
│  └──────────────────────┘    │  locks (SET NX EX)           │    │
│                              └──────────────┬───────────────┘    │
│                                             │                    │
│  ┌──────────────────────────────────────────▼─────────────────┐  │
│  │  Worker Pool (3 workers × 5 concurrency)                   │  │
│  │  BLPOP → lock → execute (http/db/transform/ml/notify)      │  │
│  │  → publish result → orchestrator advances DAG              │  │
│  └────────────────────────────────────────────────────────────┘  │
└──────────────────────────────────────────────────────────────────┘
```

## File Layout

```
fluxor/
├── backend/
│   ├── cmd/server/main.go                    Entrypoint + wiring
│   └── internal/
│       ├── models/models.go                  Domain types
│       ├── dag/graph.go + graph_test.go      DAG parser, topo sort, cycle detection
│       ├── orchestrator/orchestrator.go      Central state machine
│       ├── persistence/store.go              PostgreSQL (pgxpool)
│       ├── persistence/redis.go              Redis broker
│       ├── retry/manager.go + _test.go       Backoff, dead-letter, replay
│       ├── scheduler/processor.go            Result consumer + retry poller
│       ├── worker/worker.go                  Worker pool (5 task type simulators)
│       ├── api/handler.go                    REST handlers
│       ├── api/websocket.go                  WS hub (gorilla/websocket)
│       ├── api/grpc_server.go                gRPC implementation
│       └── metrics/prometheus.go             Prometheus metrics
│   ├── proto/orchestrator.proto              Full gRPC service definition
│   └── migrations/001_initial_schema.sql     PostgreSQL schema + triggers
│
├── frontend/src/
│   ├── App.vue                               Shell: topbar, sidebar, tab router
│   ├── stores/{workflow,execution,websocket} Pinia stores
│   ├── composables/{useApi,useWebSocket,     Typed helpers
│   │                useElapsed,useTemplates}
│   └── components/
│       ├── dag/{DAGEditor,TaskNode,           VueFlow DAG builder + templates
│       │        WorkflowTemplates}.vue
│       ├── execution/{ExecutionsView,         Live DAG view, task drill-down
│       │             ExecutionDashboard}.vue
│       ├── logs/LogViewer.vue                 Streaming terminal, filter/search
│       ├── metrics/MetricsView.vue            KPI, sparkline, donut, table
│       └── shared/StatusBadge.vue             Animated status pill
│
├── examples/etl-pipeline.json                Example workflow payload
├── examples/smoke-test.sh                    End-to-end API test
├── grafana/provisioning/                     Datasource + dashboard JSON
├── .github/workflows/ci.yml                  CI: test + lint + docker push
├── docker-compose.yml                        Full stack with health checks
├── nginx.conf                                SPA + API proxy + WS upgrade
├── Makefile                                  dev/build/test/proto/migrate
└── prometheus.yml                            Scrape config
```

## REST API

```
POST   /api/workflows                   Create workflow definition
GET    /api/workflows                   List definitions
GET    /api/workflows/:id               Get definition
POST   /api/workflows/:id/trigger       Trigger execution
GET    /api/executions                  List executions (paginated)
GET    /api/executions/:id              Get execution + tasks
POST   /api/executions/:id/cancel       Cancel running execution
POST   /api/executions/:id/retry        Replay finished execution
GET    /api/executions/:id/tasks        List tasks
GET    /api/tasks/:id/logs              Get task logs
GET    /api/metrics                     Platform metrics JSON
GET    /api/health                      Health check
GET    /ws                              WebSocket upgrade
```

## Reliability Design

| Concern | Mechanism |
|---|---|
| At-least-once delivery | Redis LIST + BLPOP; re-queued on worker restart |
| Exactly-once processing | Redis `SET NX EX` idempotency key per `(exec,task,retry)` |
| Exponential backoff | `delay = initial × multiplier^n`, capped at `max_delay` |
| Jitter | ±25% randomisation — prevents thundering herd |
| Dead-letter | After `max_retries`, task → `workflow:tasks:dead_letter` |
| Replay | `POST /api/executions/:id/retry` resets and re-runs |
| Concurrency control | Per-workflow Go channel semaphore (`max_parallel`) |
| Crash recovery | State reconstructed from PostgreSQL on restart |
| DAG validation | Kahn's BFS at submission time — rejects cycles & missing deps |

## Development

```bash
make dev             # Start infra + backend + frontend concurrently
make test            # Run unit tests with race detector
make test-integration # Requires running Postgres + Redis
make proto           # Regenerate gRPC code from .proto
make migrate         # Apply SQL migrations
make lint            # golangci-lint + vue-tsc
make build           # Compile binary + bundle frontend
```
