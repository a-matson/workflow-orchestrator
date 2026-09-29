# 0002. Postgres is the source of truth

## Status
Accepted, 2026-09-29

## Context
Tasks and executions change status from several goroutines and after restarts. Unguarded
writes let a stale actor overwrite newer state.

## Decision
Task and execution status changes only through conditional UPDATEs (compare-and-set on the
expected status and attempt). In-memory state is a cache rebuilt from the database.

## Consequences
- A failed compare-and-set means another actor won; callers must treat it as a normal outcome.
- After a restart, in-memory state is rebuilt from Postgres.
