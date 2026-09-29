# 0004. API key authentication

## Status
Accepted, 2026-09-29

## Context
The API and WebSocket are currently unauthenticated, so anyone who can reach them can
run workflows.

## Decision
Clients authenticate with API keys, stored SHA-256 hashed at rest, carrying one of the roles
admin, operator or viewer. The browser uses an HttpOnly, SameSite=Strict session cookie.
OIDC is a later addition.

## Consequences
- Browser sessions rely on an HttpOnly, SameSite=Strict cookie.
- OIDC is deferred.
