# 0001. Single-node deployment

## Status
Accepted, 2026-09-29. Superseded by [0005](0005-leader-orchestration-scale-out.md) on 2026-10-01;
still in effect until 0005's steps land.

## Context
Workers run inside the backend process and execution state is held in memory as a cache.
Running several backend replicas would let them race over the same tasks.

## Decision
One backend replica owns all executions. Workers run in-process. High availability is deferred.

## Consequences
- A backend restart kills every worker, so recovery must re-queue every open task.
- Scaling out is not supported until this decision is revisited.
