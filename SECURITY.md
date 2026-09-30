# Security policy

## Reporting a vulnerability

Use GitHub's private vulnerability reporting: **Security > Report a vulnerability** on this
repository. Do not open a public issue. The maintainer must enable private vulnerability
reporting in the repository settings for this to work.

## Supported versions

Only `main` is supported; fixes land there.

## Threat model

Fluxor is a single-node orchestrator that runs code its users submit. This section says what
it protects, what it assumes, and what it does not defend against.

### Assets

- **The host.** The backend can create containers on the host's Docker daemon.
- **Data in the stack.** Postgres (workflow definitions, execution history, API key hashes,
  audit log), MinIO (task artifacts), Redis (queues).
- **Credentials.** API keys, the session secret, database and MinIO passwords, and any secret a
  workflow carries in its task config.

### Actors and trust

| Actor | Trusted with | Boundary |
|---|---|---|
| Admin (API key, `admin` role) | Everything, including key management and the audit log | API role check (`internal/api/auth.go`) |
| Operator (`operator` role) | Creating and running workflows, which means running arbitrary code in task containers | API role check |
| Viewer (`viewer` role) | Reading workflows, executions, logs and artifacts | API role check |
| Task code | Nothing | Task container (below) |
| Network clients without a key | Health and readiness only | Authentication, `Origin` check on mutating requests |

An operator can run any code in a task container by design. The boundaries below exist so that
this code cannot escape the container or reach the rest of the stack.

### Controls

- **Authentication.** Every API route except health, readiness and login needs an API key or a
  browser session, and mutating requests need an allowed `Origin` (README, Authentication).
- **Task isolation.** Code tasks run only in containers, and fail closed without a container
  runtime. Each container has every capability dropped, `no-new-privileges`, a read-only root
  filesystem, no network, CPU, memory and PID limits, and runs as the backend's unprivileged
  uid (`internal/worker/container_executor.go`; README, Container-per-Task Isolation).
- **Egress.** In-process `http_request`, `database_query` and notification tasks can only reach
  public addresses unless an address is listed in `FLUXOR_EGRESS_ALLOW` (README, Environment
  variables).
- **Docker API.** The backend reaches the daemon only through `docker-proxy`, which answers 403
  to every endpoint the executor does not use (README, Docker socket access).
- **Files.** Workspace and artifact paths are opened through `os.Root`, and absolute paths and
  `..` are rejected. Artifacts are downloaded only through the task that produced them.
- **Exposure.** Compose binds every published port to `127.0.0.1`.
- **Secrets.** Secrets stay out of logs, API responses and error strings. Only a SHA-256 of
  each API key is stored.

### Residual risks

- **The Docker socket is host-root equivalent.** The proxy narrows the API but still allows
  `POST /containers/create`, and the request body is not validated. A compromised backend can
  create a privileged container, or bind-mount any host path, and take the host. Anyone who
  can run code *in the backend process* therefore owns the host. Task code runs in a task
  container, not in the backend, so this needs a backend vulnerability, not just an operator
  key.
- **Container escape.** Task containers share the host kernel. A kernel or runtime
  vulnerability lets task code escape, despite the hardening above.
- **Operators run code.** The operator role is equivalent to code execution on a
  hardened, network-less container. Grant it accordingly.
- **Plain HTTP.** The stack serves plain HTTP on localhost. TLS, and `Strict-Transport-Security`,
  are the job of whatever terminates TLS in front of it (set `FLUXOR_COOKIE_SECURE=true` there).
- **Secrets in task config.** A secret placed in a workflow's config is stored in Postgres and
  visible to every viewer of that workflow. There is no secret store yet.
- **No audit retention.** The audit log only grows; there is no retention policy yet.

### Upgrade paths

In rough order of effort, for deployments that need a stronger boundary:

1. **Validate the create body in the proxy path**: reject `Privileged`, host bind mounts
   other than the workspace volume, added capabilities and host namespaces. This closes the
   "compromised backend creates a privileged container" path, not kernel escapes.
2. **Rootless Docker** for the daemon the backend drives, so a container escape lands in an
   unprivileged user instead of root.
3. **A stronger runtime** for task containers: gVisor (`runsc`) intercepts syscalls in a user-space
   kernel, and Sysbox isolates containers with user namespaces. Either reduces the kernel's
   attack surface for task code.
4. **A separate worker host.** Run task containers on a machine that holds no stack data and no
   credentials beyond what tasks need, so an escape reaches nothing of value.
