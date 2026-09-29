# 0003. Container-only code execution

## Status
Accepted, 2026-09-29

## Context
Tasks can run shell commands, scripts and binaries supplied by users. Running them inside
the backend process gives them the backend's privileges and network position.

## Decision
User-supplied code (shell, scripts, binaries) runs only in task containers. There is no
in-process fallback: a missing container runtime fails the task. HTTP and notification tasks
stay in-process behind an egress guard that blocks private, loopback, link-local and
metadata IPs.

## Consequences
- The backend holds the Docker socket, which is host-root equivalent. See the README security note.
- Without a container runtime, code tasks fail instead of running unisolated.
