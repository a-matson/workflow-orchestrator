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

- The API and WebSocket require an API key or a browser session from the UI's login page (see [Authentication](#authentication)).
- The backend reaches Docker only through a filtering proxy (see [Docker socket access](#docker-socket-access)). Container creation is still allowed, so a compromised backend can still take the host.
- `docker-compose.yml` binds every published port to 127.0.0.1. The stack serves plain HTTP; put a TLS-terminating proxy in front before exposing it to a network.
- [SECURITY.md](SECURITY.md) has the threat model: what is trusted, the residual risks, and upgrade paths.

## Architecture

```
┌───────────────────────────────────────────────────────────────────────┐
│  Vue 3 + TS Frontend                                                  │
│  Builder (DAGEditor + VueFlow) │ Executions │ Logs │ Metrics          │
└─────────────────────────────┬─────────────────────────────────────────┘
                              │ REST + WebSocket
┌─────────────────────────────▼──────────────────────────────────────────┐
│  Go Backend                                                            │
│  REST /api/* │ WS Hub /ws │ Prometheus :9091                           │
│                                                                        │
│  ┌───────────────────────────────────────────────────────────────┐     │
│  │  Orchestrator                                                 │     │
│  │  Kahn topo-sort · dependency waves · max_parallel from rows   │     │
│  │  exponential backoff · dead-letter · crash recovery           │     │
│  └──────────────┬────────────────────────────┬───────────────────┘     │
│                 │                            │                         │
│  ┌──────────────▼──────┐    ┌────────────────▼───────────────────┐     │
│  │  PostgreSQL 16      │    │  Redis 7 Broker                    │     │
│  │  definitions        │    │  task queue   (LIST BRPOP)         │     │
│  │  executions/tasks   │    │  results      (LIST)               │     │
│  │  artifacts (JSONB)  │    │  dead_letter  (LIST)               │     │
│  └─────────────────────┘    │                                    │     │
│                             └───────────────┬────────────────────┘     │
│                                             │                          │
│  ┌──────────────────────────────────────────▼───────────────────────┐  │
│  │  Worker Pool                                                     │  │
│  │  BRPOP → mark running → resolve artifact inputs from MinIO       │  │
│  │    → spawn isolated Docker container (cap-drop ALL, no-net)      │  │
│  │      via docker-proxy → host Docker socket                       │  │
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
- No network (`NetworkMode: none`) — **no internet, no access to Postgres, Redis or other task containers**; use `http_request` or `notification` tasks for outbound calls
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

The backend does not mount the Docker socket. It sets `DOCKER_HOST=tcp://docker-proxy:2375` and reaches the host daemon through `docker-proxy` ([tecnativa/docker-socket-proxy](https://github.com/Tecnativa/docker-socket-proxy), HAProxy, pinned by digest), which alone mounts the socket. The proxy sits on the `docker-api` network, shared only with the backend, marked `internal`, and publishes no ports.

The proxy answers 403 to every endpoint outside the calls `ContainerExecutor` makes:

| Endpoint | Executor call | Proxy flag |
|----------|---------------|------------|
| `HEAD`/`GET /_ping` | `Ping` (API version negotiation for the volume-subpath gate) | `PING=1` |
| `GET /images/{name}/json` | `ImageInspect` | `IMAGES=1` |
| `POST /images/create` | `ImagePull` | `IMAGES=1`, `POST=1` |
| `POST /containers/create` | `ContainerCreate` | `CONTAINERS=1`, `POST=1` |
| `POST /containers/{id}/start` | `ContainerStart` | `CONTAINERS=1`, `POST=1` |
| `POST /containers/{id}/wait` | `ContainerWait` | `CONTAINERS=1`, `POST=1` |
| `GET /containers/{id}/logs` | `ContainerLogs` | `CONTAINERS=1` |
| `POST /containers/{id}/kill` | `ContainerKill` | `CONTAINERS=1`, `POST=1` |
| `DELETE /containers/{id}` | `ContainerRemove` | `CONTAINERS=1`, `POST=1` |

Everything else is off, including `EVENTS` and `VERSION`, which the image enables by default. The flags are coarse: `CONTAINERS=1` opens every `/containers` path (archive, attach, exec creation; running an exec needs `/exec`, which stays off) and `POST=1` opens every non-GET method on the allowed paths.

**Limit:** the proxy narrows the API, it does not make container creation safe. `POST /containers/create` can still request a privileged container or bind-mount any host path, so a compromised backend remains host-root equivalent. Validating the create body is not done yet.

HAProxy closes a response idle for 10 minutes, which cuts `/wait` on a long task; the executor waits again (`waitExit`).

Compose adds the proxy to the socket's group via `DOCKER_SOCKET_GID` (default `0`, which matches Docker Desktop). On Linux the socket usually belongs to the `docker` group, so export its GID before starting: `export DOCKER_SOCKET_GID=$(stat -c %g /var/run/docker.sock)`.

### Configuring a task for isolation

In the **Builder → task config panel → Container Isolation**, toggle Enabled to override the defaults below. Code tasks use the defaults when it is off.

| Field | Default | Description |
|-------|---------|-------------|
| Docker Image | `alpine:3.22` | Any image on the Docker daemon |
| Memory MB | 256 | Hard memory limit |
| CPU millis | 500 | 500 = 0.5 vCPU |

Add file paths under **Artifact Outputs** (files this task writes) and **Artifact Inputs** (files from dependency tasks this task needs).

---

### Templating task config

Strings in a task's `config` can use Go `text/template`. They see the run's trigger payload and the outputs of the task's direct dependencies, and are rendered by the worker just before the task runs:

```json
{"url": "https://{{ .payload.region }}.example.com/items/{{ .tasks.extract.output.id }}"}
```

A missing key fails the task instead of rendering an empty string, and a template that does not parse is rejected when the workflow is saved. Rendered values are inserted as text, so quote or validate them where they end up in a command or a query.

### Schedules

A workflow's `schedule` is a cron expression, evaluated in UTC: five fields (`0 2 * * *` is 02:00 every day) or a descriptor such as `@hourly`. Each time it fires, the backend starts a run whose trigger payload is `{"scheduled_at": "<RFC3339 time>"}`. The response's `next_run_at` shows the next firing; saving the workflow restarts the schedule from now. A firing missed while the backend was down runs once at startup, not once per missed time. Clear `schedule` to stop it.

### Alerts

A workflow's `alerts` posts a run's outcome as JSON to a URL when the run finishes: `on_success` when it completes, `on_failure` when it fails. A cancelled run posts nothing.

```json
{"alerts": {"on_failure": {"url": "https://hooks.example.com/fluxor"}}}
```

The body is `{"event": "workflow.failed", "workflow_id", "workflow_name", "execution_id", "status", "started_at", "completed_at", "failed_tasks": ["load"]}`. Alerts go out through the egress guard, so a private or loopback host needs `FLUXOR_EGRESS_ALLOW`. Each alert is one attempt with a 10-second timeout: a receiver that is down or answers outside 2xx misses it, and the backend logs a warning. Anyone who can read the workflow can see the URL, so prefer a URL that does not embed a long-lived credential.

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

### Observability

`docker compose --profile observability up -d` adds Prometheus (http://localhost:9090) and Grafana (http://localhost:3001). Prometheus scrapes the backend's `:9091/metrics` and loads the alert rules in `prometheus-alerts.yml`: backend unreachable for 2 minutes, more than 100 queued task messages for 10 minutes, and any task dead-lettered in the last 15 minutes. Nothing routes the alerts anywhere yet; they show under Alerts in Prometheus. Grafana provisions the "Fluxor Orchestrator" dashboard, viewable without logging in; set `GRAFANA_ADMIN_PASSWORD` in `.env` to edit it.

### Environment variables

| Variable | Default | Description |
|----------|---------|-------------|
| `POSTGRES_URL` | `postgres://workflow@localhost:5432/workflow` | PostgreSQL DSN (compose builds it with the password from `.env`; the default takes the password from `PGPASSWORD`) |
| `REDIS_ADDR` | `localhost:6379` | Redis address |
| `MINIO_ENDPOINT` | `localhost:9000` | MinIO S3 endpoint |
| `REDIS_PASSWORD` | _(none)_ | Redis password (from `.env` with compose) |
| `MINIO_ACCESS_KEY` | _(none)_ | MinIO access key (`MINIO_ROOT_USER` in `.env` with compose) |
| `MINIO_SECRET_KEY` | _(none)_ | MinIO secret key (`MINIO_ROOT_PASSWORD` in `.env` with compose) |
| `MINIO_BUCKET` | `fluxor-artifacts` | Artifact bucket name |
| `DOCKER_HOST` | `unix:///var/run/docker.sock` | Docker daemon address (compose sets `tcp://docker-proxy:2375`) |
| `WORKER_COUNT` | `3` | Number of worker goroutines |
| `WORKER_CONCURRENCY` | `5` | Tasks per worker |
| `HTTP_ADDR` | `:8080` | HTTP listen address |
| `FLUXOR_ALLOWED_ORIGINS` | _(empty)_ | Comma-separated browser origins allowed cross-origin (same-origin on localhost is always allowed) |
| `FLUXOR_EGRESS_ALLOW` | _(empty)_ | Comma-separated CIDRs (`10.20.0.0/16`) and exact `host:port` entries (`geo-service.internal:80`) that `http_request`, notification and `database_query` tasks may reach despite the egress guard |
| `FLUXOR_MAX_ARTIFACT_BYTES` | `104857600` (100 MiB) | Largest artifact a container task may download into or upload from its workspace; an invalid value keeps the container executor from starting |
| `FLUXOR_MAX_TASK_OUTPUT_BYTES` | `1048576` (1 MiB) | Largest task output or fetched response body (and container stdout/stderr) kept; longer ones are cut and marked `truncated`. An invalid value stops startup |
| `FLUXOR_MAX_TASK_LOG_BYTES` | `1048576` (1 MiB) | Largest set of log entries kept per attempt; later entries are dropped after a truncation notice. An invalid value stops startup |
| `FLUXOR_INTERNAL_SUBNET` | `172.29.250.0/24` | Compose only: subnet of the `fluxor-internal` network. Docker refuses overlapping networks, so give each stack on one host its own (`make e2e` uses `172.29.251.0/24`), with `FLUXOR_PROXY_IP` inside it |
| `FLUXOR_PROXY_IP` | `172.29.250.10` | Compose only: fixed address of the frontend (nginx) container. The backend trusts `X-Forwarded-For` from this `/32` alone, not the subnet, because the bridge gateway (the source address of every host client) lies inside the subnet |
| `FLUXOR_TRUSTED_PROXIES` | _(empty)_ | Comma-separated CIDRs of reverse proxies whose `X-Forwarded-For` the rate limiter believes (the right-most address outside this set is the client). From any other peer the header is ignored and the limiter keys on the peer IP (IPv6 on its /64). Compose defaults it to `FLUXOR_PROXY_IP/32`; the bundled nginx overwrites the header with the client address. With an empty value behind a proxy, all clients share the proxy's bucket. A proxy you add in front of nginx must be listed and must append to the header |
| `FLUXOR_RATE_LIMIT_PER_MIN` | `200` | Requests per minute per client IP for the whole API. Positive integer; anything else stops startup |
| `FLUXOR_LOGIN_RATE_LIMIT_PER_MIN` | `10` | `POST /api/session` per minute per client IP, counted separately. Positive integer; anything else stops startup. A `429` carries `Retry-After` |
| `FLUXOR_MAX_BODY_BYTES` | `1048576` (1 MiB) | Largest request body the API reads; a bigger one gets `413` `request body too large`. `POST /api/session` keeps its own 4 KiB cap. Positive integer; anything else stops startup |
| `LOG_LEVEL` | `info` | `debug` or `info` |
| `FLUXOR_SESSION_SECRET` | _(random per start)_ | Base64 HMAC key (≥ 32 bytes) for browser session cookies (see [Browser login](#browser-login)) |
| `FLUXOR_COOKIE_SECURE` | `false` | Mark the session cookie `Secure` even over plain HTTP, for a TLS-terminating proxy |
| `FLUXOR_RETENTION_DAYS` | `0` | Delete finished runs, with their tasks and logs, this many days after they finished; `0` keeps them forever. Artifacts in MinIO are not deleted |
| `FLUXOR_AUDIT_RETENTION_DAYS` | `0` | Delete audit entries older than this many days; `0` keeps them forever |
| `LOG_FORMAT` | `json` | `json` (one object per line, with `request_id`) or `console` (pretty, local dev) |
| `FLUXOR_BOOTSTRAP_ADMIN_KEY` | _(empty)_ | Admin API key installed at startup, if set (see [Authentication](#authentication)) |

Deployments on a real hostname must list their public origin in `FLUXOR_ALLOWED_ORIGINS`; likewise, reaching the backend through a LAN IP or hostname requires listing that origin, or requests are rejected with 421.

`http_request` and notification tasks cannot reach loopback, private (RFC 1918, `fc00::/7`, `fec0::/10`), link-local (including the `169.254.169.254` metadata endpoint), CGNAT (`100.64.0.0/10`), reserved or special-use (`192.0.0.0/24`, `198.18.0.0/15`, `240.0.0.0/4`, broadcast), unspecified or multicast addresses, including their IPv4-mapped IPv6 forms. IPv6 ranges that embed an IPv4 address are denied whole: IPv4-compatible and IPv4-translated, NAT64 (`64:ff9b::/96`, `64:ff9b:1::/48`), Teredo (`2001::/32`) and 6to4 (`2002::/16`). The check runs on the IP actually dialled, on every redirect hop (it follows at most 5 redirects, http and https only), and proxy environment variables are ignored. To let tasks call an internal service, list it in `FLUXOR_EGRESS_ALLOW`: a CIDR allows every port in the range; a `host:port` allows that name or IP literal, as written in the task URL, whatever it resolves to. Denied requests fail with `egress denied`. `database_query` checks every host in the connection string (fallback hosts included, names resolved) against the same rules before connecting; `host:port` entries match the host as written in the connection string, and unix-socket hosts are always denied.

On an IPv6-only host behind NAT64, a network-specific NAT64 prefix (RFC 6052) or an ISATAP address (`::5efe:a.b.c.d` under any /64) can embed an internal IPv4 address that no fixed prefix catches; block those at the network layer.

---

## REST API

Example (the header comes from stdin, so the key stays out of `ps`):

```bash
printf 'Authorization: Bearer %s\n' "$FLUXOR_API_KEY" |
  curl -X POST http://localhost:8080/api/workflows -H @- -H 'Content-Type: application/json' -d @examples/etl-pipeline.json
```

```
POST   /api/workflows                  Create workflow definition
POST   /api/workflows/import           Create a workflow from YAML ({"yaml": "..."})
GET    /api/workflows/{id}/export      Download a workflow as YAML
GET    /api/workflows                  List all definitions
GET    /api/workflows/{id}             Get definition
POST   /api/workflows/{id}/trigger     Start execution
GET    /api/executions                 List executions
GET    /api/executions/{id}            Get execution with tasks
POST   /api/executions/{id}/cancel     Cancel running execution
POST   /api/executions/{id}/retry      Retry failed execution
POST   /api/executions/{id}/resume     Resume a failed or cancelled run: re-run tasks that did not complete
GET    /api/executions/{execID}/tasks  List tasks for execution
GET    /api/tasks/{id}                 Get task execution
GET    /api/tasks/{id}/logs            Get task logs
GET    /api/tasks/{id}/artifacts       Get task artifact metadata
GET    /api/tasks/{id}/artifacts/{path} Download one artifact the task produced
GET    /api/metrics                    Platform metrics
POST   /api/client-errors              Log an error the UI could not handle (sent by the UI, rate-limited)
GET    /api/health                     Liveness (process up, no dependency checks)
GET    /api/ready                      Readiness (Postgres, Redis, MinIO; 503 if any is down)
GET    /ws                             WebSocket (real-time events)
GET    /api/keys                       List API keys (admin)
POST   /api/keys                       Create an API key: {"name","role"} (admin)
DELETE /api/keys/{id}                  Revoke an API key (admin)
GET    /api/audit?limit&offset         Audit trail, newest first (admin)
```

---

## Authentication

Every endpoint except `GET /api/health` and `GET /api/ready` requires an API key, sent as
`Authorization: Bearer flx_…`. That includes the `/ws` upgrade request. A missing, unknown or
revoked key gets `401`; a key whose role is too low gets `403`. Revocation applies to the next
HTTP request; an already open `/ws` connection re-checks its key every 30 seconds and is closed
with code 1008 (policy violation), so it can outlive the revocation by up to 30 seconds.

| Role | May |
|------|-----|
| `viewer` | every `GET`, including `/ws` |
| `operator` | viewer, plus create/update workflows, trigger, cancel and retry executions |
| `admin` | operator, plus manage API keys |

Create the first key with the backend binary (it reads `POSTGRES_URL`). The key is printed once;
only its SHA-256 is stored.

```bash
docker compose exec backend ./workflow-server apikey create --name me --role admin
docker compose exec backend ./workflow-server apikey list
docker compose exec backend ./workflow-server apikey revoke <id>
```

Alternatively, set `FLUXOR_BOOTSTRAP_ADMIN_KEY` in `.env` to a key you generated
(`echo "flx_$(head -c 32 /dev/urandom | base64 | tr '+/' '-_' | tr -d '=\n')"`); the backend
installs it as an admin key at startup. Revoking it is permanent, even if it stays in `.env`.
Changing the variable adds the new key but leaves the old one active, so after rotating it
revoke the old `bootstrap-admin` key (`apikey list`, then `apikey revoke <id>`).

### Browser login

The UI has no Bearer header to send, so it signs in once with an API key at `/login`.
`POST /api/session` with `{"api_key":"flx_…"}` checks the key and sets the `fluxor_session`
cookie: HttpOnly, `SameSite=Strict`, `Path=/`, valid for 12 hours. That cookie authenticates
every request, `/ws` included, with the key's name and role. `GET /api/session` returns
`{name, role}`; `DELETE /api/session` (the UI's Log out) clears the cookie. Revoking the key ends
its sessions on the next request and closes their open `/ws` streams within 30 seconds; a
`/ws` stream opened with the cookie likewise closes within 30 seconds of the cookie expiring.
Session responses carry `Cache-Control: no-store`, and the login body is capped at 4 KiB.

The cookie is signed with HMAC-SHA256 under `FLUXOR_SESSION_SECRET`, at least 32 bytes, base64
encoded (`head -c 32 /dev/urandom | base64`). Without it the backend picks a random secret and
logs a warning, so every restart logs everyone out. The cookie is marked `Secure` when the request
arrives over TLS; behind a TLS-terminating proxy, set `FLUXOR_COOKIE_SECURE=true` (any value
Go's `strconv.ParseBool` accepts; anything else stops startup).

### Audit log

Every mutation records who did what in the `audit_log` table: workflow create/update, execution
trigger/cancel/retry, API key create/revoke, and browser login/logout. Each row holds the acting
key's id and name, the action (`workflow.trigger`, `key.revoke`, ...), the target type and id, the
`X-Request-ID` of the request, and an outcome of `success`, `denied` or `error`. No request
payloads are stored, since they may carry secrets. Requests refused with 401/403 on a mutating
route are recorded as `denied` (with target `route` and the request path); refused GETs are not.

`GET /api/audit?limit=50&offset=0` (admin only, limit capped at 200) returns
`{"entries": [...], "count": n}`. A failed audit write is logged at error level and does not fail
the request, so the log line is the fallback record. The table has no retention and grows until
F10's retention work lands.

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
| At-least-once delivery | Redis LIST + BRPOP; a restart re-queues every unfinished attempt, which then runs again from the start. Recovery resumes every pending or running execution |
| At-most-once execution per attempt, within one process lifetime | A worker runs a message only if it moves the task row from `queued` to `running` at the message's `retry_count`; a duplicate, or a pickup that cannot be recorded, is dropped. A result closes the attempt only if it comes from the worker that picked it up |
| Exponential backoff | `delay = initial × multiplier^n`, capped at `max_delay` |
| Jitter | ±25% randomisation — prevents thundering herd |
| Retry scheduling | A retry waits in its task row (`retrying`, `next_retry_at`); a 5s poller dispatches it once due |
| Dead-letter | After `max_retries`, task → `workflow:tasks:dead_letter` |
| Trigger rules | A task's `trigger_rule` decides when it runs: `all_success` (default; skipped if a dependency failed or was skipped), `all_done` (once every dependency ended), `one_failed` (once a dependency failed; skipped if none did). A task's `when` (a template such as `{{ eq .payload.env "prod" }}`, checked once its rule is met) skips it unless it renders `true`, recording why. A workflow that uses `all_done`, `one_failed` or `when` lets a failure flow through the rules and ends failed once every task has ended; one that uses only the default fails fast, cancelling open tasks |
| Task timeouts | A task's `timeout` (integer nanoseconds or a string like `"30s"`; 1 h when unset) kills its run; a running task with no result 1 min past its timeout is failed by the same 5s poller, and its retry policy applies |
| Replay | `POST /api/executions/:id/retry` resets and re-runs |
| Concurrency control | `max_parallel` counted from the execution's queued and running task rows |
| Crash recovery | State reconstructed from PostgreSQL on restart |
| Graceful shutdown | On SIGTERM workers stop taking tasks and give running ones 25 s to finish and publish their results; any still running are stopped without a result and re-queued by the next start's recovery |
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
