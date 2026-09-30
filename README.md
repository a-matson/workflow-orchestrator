# Fluxor — Distributed Workflow Orchestration Platform

> DAG-based workflow execution with container-per-task isolation, MinIO artifact storage, real-time observability, and a visual drag-and-drop builder.

## Quick Start

```bash
git clone https://github.com/a-matson/workflow-orchestrator && cd fluxor
cp .env.example .env   # then edit the passwords
docker compose up -d --build
Open **http://localhost:3000** — the Builder page loads immediately.
```

`docker compose up -d --build` builds the frontend Vue UI inside a Docker image (no host-side npm build needed).

Note: Postgres applies `POSTGRES_PASSWORD` only when its volume is first created (for an existing volume, run `ALTER USER` or recreate the volume). Use alphanumeric passwords, since they are embedded in a URL and a command line.

| Service           | URL                                         |
|-------------------|---------------------------------------------|
| **UI**            | http://localhost:3000                       |
| **REST API**      | http://localhost:8080/api                   |
| **WebSocket**     | ws://localhost:8080/ws                      |
| **MinIO Console** | http://localhost:9001 (credentials from .env) |
| **Redis UI**      | http://localhost:8081 (profile: tools)      |

## Security status

- The API and WebSocket are currently unauthenticated.
- The backend mounts the Docker socket, which is host-root equivalent.
- `docker-compose.yml` currently publishes every port on all interfaces (0.0.0.0).
- Bind every port to 127.0.0.1 and never expose the stack to a network.

## Architecture

```
┌───────────────────────────────────────────────────────────────────────┐
│  Vue 3 + TS Frontend                                                  │
│  Builder (DAGEditor + VueFlow) │ Executions │ Logs │ Metrics          │
└─────────────────────────────┬─────────────────────────────────────────┘
                              │ REST + WebSocket
┌─────────────────────────────▼──────────────────────────────────────────┐
│  Go Backend                                                            │
│  REST /api/* │ WS Hub /ws │ Prometheus :9091                            │
│                                                                        │
│  ┌───────────────────────────────────────────────────────────────┐     │
│  │  Orchestrator                                                 │     │
│  │  Kahn topo-sort · dependency waves · concurrency semaphores   │     │
│  │  exponential backoff · dead-letter · crash recovery           │     │
│  └──────────────┬────────────────────────────┬───────────────────┘     │
│                 │                            │                         │
│  ┌──────────────▼─────┐    ┌────────────────▼────────────────────┐     │
│  │  PostgreSQL 16      │    │  Redis 7 Broker                    │     │
│  │  definitions        │    │  task queue   (LIST BLPOP)         │     │
│  │  executions/tasks   │    │  idempotency  (SET EX)             │     │
│  │  artifacts (JSONB)  │    │  dead_letter  (LIST)               │     │
│  └─────────────────────┘    │  locks        (SET NX EX)          │     │
│                              └──────────────┬────────────────────┘     │
│                                             │                          │
│  ┌──────────────────────────────────────────▼───────────────────────┐  │
│  │  Worker Pool                                                     │  │
│  │  BLPOP → acquire lock → resolve artifact inputs from MinIO       │  │
│  │    → spawn isolated Docker container (cap-drop ALL, no-net)      │  │
│  │    → execute task command inside /workspace                      │  │
│  │    → collect stdcopy logs → upload artifact outputs to MinIO     │  │
│  │    → publish result → orchestrator advances DAG                  │  │
│  └──────────────────────────────────────────────────────────────────┘  │
│                                             │                          │
│  ┌──────────────────────────────────────────▼───────────────────────┐  │
│  │  MinIO  (S3-compatible)                                          │  │
│  │  Bucket: fluxor-artifacts                                        │  │
│  │  Key:    artifacts/{workflow_exec_id}/{task_def_id}/{path}       │  │
│  └──────────────────────────────────────────────────────────────────┘  │
└────────────────────────────────────────────────────────────────────────┘
```
## Container-per-Task Isolation

Code tasks (`data_transform`, `generic`, `ml_inference`) always run in their own ephemeral Docker container; there is no in-process fallback. If Docker or MinIO is unreachable at startup the backend still starts, logs a warning, and fails those tasks with `container runtime unavailable`. `http_request`, `database_query` and `notification` tasks run in the backend unless they set a `container` field. When the task finishes the container is removed.

**Security model:**
- `--cap-drop ALL` — drops every Linux capability
- `no-new-privileges:true` — blocks privilege escalation
- Read-only root filesystem with a tmpfs `/tmp`
- Connected to the `fluxor-tasks` internal network — **no internet, no access to Postgres or Redis**
- CPU and memory hard limits from `ContainerSpec`
- Max 256 PIDs per container

**Artifact flow — passing files between tasks:**

```
Task A runs in container-A
  → writes output.json to /workspace/
  → worker uploads /workspace/output.json → MinIO
     key: artifacts/{execID}/{taskA_id}/output.json

Task B depends on Task A; artifacts_in: [{path: "output.json"}]
  → orchestrator resolves MinIO key from Task A's ArtifactsOut
  → worker downloads output.json → /workspace/output.json
  → spawns container-B with /workspace bind-mounted
  → Task B reads /workspace/output.json
```

### Docker socket access

The backend container reaches the host Docker daemon through the mounted socket, and compose adds the container user to the socket's group via `DOCKER_SOCKET_GID` (default `0`, which matches Docker Desktop). On Linux the socket usually belongs to the `docker` group, so export its GID before starting: `export DOCKER_SOCKET_GID=$(stat -c %g /var/run/docker.sock)`.

### Configuring a task for isolation

In the **Builder → task config panel → Container Isolation**, toggle Enabled to override the defaults below. Code tasks use the defaults when it is off.

| Field | Default | Description |
|-------|---------|-------------|
| Docker Image | `alpine:3.22` | Any image on the Docker daemon |
| Memory MB | 256 | Hard memory limit |
| CPU millis | 500 | 500 = 0.5 vCPU |

Add file paths under **Artifact Outputs** (files this task writes) and **Artifact Inputs** (files from dependency tasks this task needs).

---

## Development Setup

```bash
# Start infrastructure
docker compose up -d postgres redis minio

# Run backend. The Vite dev server (http://localhost:5173) calls the API
# cross-origin, so it must be allowlisted.
cd backend
go mod download
FLUXOR_ALLOWED_ORIGINS=http://localhost:5173 go run ./cmd/server

# Run frontend dev server (separate terminal)
cd frontend
npm install
npm run dev     # → http://localhost:5173

# Lint and format frontend
npm run lint
npm run format
```

### Environment variables

| Variable | Default | Description |
|----------|---------|-------------|
| `POSTGRES_URL` | `postgres://workflow:<POSTGRES_PASSWORD>@localhost:5432/workflow` | PostgreSQL DSN (password from `.env` with compose) |
| `REDIS_ADDR` | `localhost:6379` | Redis address |
| `MINIO_ENDPOINT` | `localhost:9000` | MinIO S3 endpoint |
| `REDIS_PASSWORD` | _(none)_ | Redis password (from `.env` with compose) |
| `MINIO_ACCESS_KEY` | _(none)_ | MinIO access key (`MINIO_ROOT_USER` in `.env` with compose) |
| `MINIO_SECRET_KEY` | _(none)_ | MinIO secret key (`MINIO_ROOT_PASSWORD` in `.env` with compose) |
| `MINIO_BUCKET` | `fluxor-artifacts` | Artifact bucket name |
| `DOCKER_HOST` | `unix:///var/run/docker.sock` | Docker daemon socket |
| `WORKER_COUNT` | `3` | Number of worker goroutines |
| `WORKER_CONCURRENCY` | `5` | Tasks per worker |
| `HTTP_ADDR` | `:8080` | HTTP listen address |
| `FLUXOR_ALLOWED_ORIGINS` | _(empty)_ | Comma-separated browser origins allowed cross-origin (same-origin on localhost is always allowed) |
| `FLUXOR_EGRESS_ALLOW` | _(empty)_ | Comma-separated CIDRs (`10.20.0.0/16`) and exact `host:port` entries (`geo-service.internal:80`) that `http_request`, notification and `database_query` tasks may reach despite the egress guard |
| `LOG_LEVEL` | `info` | `debug` or `info` |
| `LOG_FORMAT` | `json` | `json` (one object per line, with `request_id`) or `console` (pretty, local dev) |

Deployments on a real hostname must list their public origin in `FLUXOR_ALLOWED_ORIGINS`; likewise, reaching the backend through a LAN IP or hostname requires listing that origin, or requests are rejected with 421.

`http_request` and notification tasks cannot reach loopback, private (RFC 1918, `fc00::/7`, `fec0::/10`), link-local (including the `169.254.169.254` metadata endpoint), CGNAT (`100.64.0.0/10`), reserved or special-use (`192.0.0.0/24`, `198.18.0.0/15`, `240.0.0.0/4`, broadcast), unspecified or multicast addresses, including their IPv4-mapped IPv6 forms. IPv6 ranges that embed an IPv4 address are denied whole: IPv4-compatible and IPv4-translated, NAT64 (`64:ff9b::/96`, `64:ff9b:1::/48`), Teredo (`2001::/32`) and 6to4 (`2002::/16`). The check runs on the IP actually dialled, on every redirect hop (it follows at most 5 redirects, http and https only), and proxy environment variables are ignored. To let tasks call an internal service, list it in `FLUXOR_EGRESS_ALLOW`: a CIDR allows every port in the range; a `host:port` allows that name or IP literal, as written in the task URL, whatever it resolves to. Denied requests fail with `egress denied`. `database_query` checks every host in the connection string (fallback hosts included, names resolved) against the same rules before connecting; `host:port` entries match the host as written in the connection string, and unix-socket hosts are always denied.

On an IPv6-only host behind NAT64, a network-specific NAT64 prefix (RFC 6052) or an ISATAP address (`::5efe:a.b.c.d` under any /64) can embed an internal IPv4 address that no fixed prefix catches; block those at the network layer.

---

## REST API

Example: `curl -X POST http://localhost:8080/api/workflows -H 'Content-Type: application/json' -d @examples/etl-pipeline.json`

```
POST   /api/workflows                  Create workflow definition
GET    /api/workflows                  List all definitions
GET    /api/workflows/{id}             Get definition
POST   /api/workflows/{id}/trigger     Start execution
GET    /api/executions                 List executions
GET    /api/executions/{id}            Get execution with tasks
POST   /api/executions/{id}/cancel     Cancel running execution
POST   /api/executions/{id}/retry      Retry failed execution
GET    /api/executions/{execID}/tasks  List tasks for execution
GET    /api/tasks/{id}                 Get task execution
GET    /api/tasks/{id}/logs            Get task logs
GET    /api/tasks/{id}/artifacts       Get task artifact metadata
GET    /api/artifacts/url?key=…        Pre-signed MinIO download URL
GET    /api/metrics                    Platform metrics
GET    /api/health                     Liveness (process up, no dependency checks)
GET    /api/ready                      Readiness (Postgres, Redis, MinIO; 503 if any is down)
GET    /ws                             WebSocket (real-time events)
```

---

## Running Migrations

The backend applies embedded migrations on startup, tracked in `schema_migrations`. For manual runs:

```bash
make migrate
```

---

## Reliability Design

| Concern | Mechanism |
|---|---|
| At-least-once delivery | Redis LIST + BLPOP; re-queued on worker restart |
| Exactly-once processing | Redis `SET NX EX` idempotency key per `(exec,task,retry)` |
| Exponential backoff | `delay = initial × multiplier^n`, capped at `max_delay` |
| Jitter | ±25% randomisation — prevents thundering herd |
| Retry scheduling | A retry waits in its task row (`retrying`, `next_retry_at`); a 5s poller dispatches it once due |
| Dead-letter | After `max_retries`, task → `workflow:tasks:dead_letter` |
| Replay | `POST /api/executions/:id/retry` resets and re-runs |
| Concurrency control | Per-workflow Go channel semaphore (`max_parallel`) |
| Crash recovery | State reconstructed from PostgreSQL on restart |
| Queue durability | Redis runs with `maxmemory-policy noeviction`; the backend refuses to start on an evicting policy |
| DAG validation | Kahn's BFS at submission time — rejects cycles & missing deps |

## Building for Production

```bash
# Build frontend
cd frontend && npm run build

# Build backend
cd backend && go build -o workflow-server ./cmd/server

# Or build the Docker image
docker build -t fluxor-backend ./backend
```
