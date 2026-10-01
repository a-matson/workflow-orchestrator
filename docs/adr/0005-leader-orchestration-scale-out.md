# 0005. Leader-owned orchestration with scaled-out API and workers

## Status
Accepted, 2026-10-01. Supersedes [0001](0001-single-node.md). Lands in steps (roadmap H1–H9);
until the last step merges, the deployment is still single-node.

## Context
A single backend replica (0001) is a single point of failure, and task throughput is capped by
one host's worker pool. The parts that stop several replicas from running together are:
- Execution progress is decided from an in-memory cache (`Orchestrator.active`), so two
  orchestrators would each hold a partial, stale view of the same execution.
- Startup recovery resets every queued and running task, and orphan cleanup removes every
  container and workspace of the stack, including those of a live peer.
- The result queue is a Redis list read with BRPOP: a result popped by a process that then
  crashes is lost.
- WebSocket events and task cancellation reach only the local process.
- Workers call the orchestrator in-process (`TaskNotifier`).

Rewriting the orchestrator to re-read the database on every advance would remove the cache, at
the cost of a database read per result and a rewrite of the most complex package.

## Decision
- One **leader**, elected with a Postgres session advisory lock held for the process lifetime,
  runs the orchestrator, the result processor and the pollers (retry, timeout reaper, retention).
  The in-memory cache stays valid because only the leader advances executions. A new leader
  rebuilds it through the existing recovery path.
- **API replicas** scale horizontally. A trigger inserts the execution and calls `pg_notify`; the
  leader listens, with a periodic poll as a fallback. Cron claims are already compare-and-set.
- **Workers** scale horizontally as a separate role (`FLUXOR_ROLE=all|api|worker`, default `all`).
  They report pickup and progress through Postgres and Redis, not in-process calls.
- Results move to a **Redis Stream** with a consumer group, acknowledged after the result commits.
  The task queue stays a Redis list; the pickup compare-and-set (0002) drops duplicates and the
  leader re-enqueues tasks stuck in `queued`.
- Running tasks send a **heartbeat**; the leader re-queues tasks whose heartbeat is stale instead
  of resetting every open task at startup. Containers and workspaces are labelled per instance,
  so cleanup touches only the instance's own.
- WebSocket events and cancellation fan out over **Redis pub/sub**.

## Consequences
- Throughput of advancing executions is bounded by one leader. If load tests show the leader
  saturating, the upgrade path is stateless advance: re-read the execution under a per-execution
  advisory transaction lock and drop the cache.
- Failover takes up to one lock-retry interval plus recovery time.
- Workers hold Postgres and Redis credentials, so worker hosts must be trusted. Runners on other
  networks need an HTTP agent protocol (deferred).
- `FLUXOR_SESSION_SECRET` must be set when more than one process serves the API.
- The per-process API rate limit becomes N times the configured value with N API replicas.
