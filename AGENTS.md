# AGENTS.md

Fluxor is a DAG workflow orchestrator: Go backend (`backend/`), Vue 3 + TS UI (`frontend/`),
Postgres, Redis, MinIO, and Docker task containers. Deployment is single-node.

## Evidence
Every claim in a PR, review, or commit message carries evidence: a command with its output,
a test name, or a `file:line`. Anything you could not measure goes under **Assumptions** in
the PR, labelled as an assumption.

## Workflow
- One concern per branch, cut from the latest `main`: fix/ feat/ sec/ obs/ test/ ci/ chore/ docs/ perf/ a11y/ refactor/.
- Conventional commit subjects: `fix(orchestrator): …`.
- Run `make hooks` once per clone to enable the commit-msg and pre-push hooks.
- A bug fix starts **red**:
  1. Re-run its reproducing test on the current `main`. If it is already green, close the item as "fixed by #N" and keep the test.
  2. Commit the failing test alone. If it needs a new entry point, add a no-op stub so the test fails on its assertion, not on compilation.
  3. Push, so CI records the red run.
  4. Commit the fix.
- Config and infra bugs use a scripted check as their red test. Refactors and features ship their tests in the same change.
- Bug fixes merge with rebase-merge, so the red commit stays in history. Everything else squash-merges.
- Fill every section of `.github/pull_request_template.md`.

## Verify before pushing
- Backend: `cd backend && gofmt -l . && go vet ./... && golangci-lint run ./... && go test -race ./...`
- Backend integration (needs Postgres and Redis, e.g. `docker compose up -d postgres redis`): `go test -race -tags integration ./...`. Plain `go test ./...` skips these without a warning.
- Frontend: `cd frontend && npm run lint && npm run type-check && npm run test:unit`

## UI tests
UI tests select elements by `data-testid`; add one when a test needs to find an element.

## Comments
Comments carry the **why**: a constraint, a trade-off, a non-obvious invariant, or a link to an
issue. Names and structure carry the what. When code needs a comment to say what it does, rename
or extract instead. Doc comments on exported identifiers state the contract, not the steps.
- Keep:   `// Key includes retry_count so a retry is not mistaken for a redelivery.`
- Delete: `// Update task state to queued` above `task.Status = TaskStatusQueued`.
Mark a deliberate shortcut with a why-comment that names its ceiling and upgrade path.

## Errors
Handle or return every error. A discarded error (`_ = f()`) carries a why-comment.

## Security guardrails
- User-supplied code runs only inside task containers. Without a container runtime, the task **fails closed**.
- Outbound requests made by tasks go through the egress guard.
- Open workspace and artifact files through `os.Root`. Reject absolute paths and `..`.
- Secrets stay out of logs, API responses, and error strings.
- Mutating endpoints require authentication and an allowed `Origin`.

## Facts that are easy to get wrong
- Postgres is the source of truth. Change status only through the store's transition methods.
  In-memory execution state is a cache rebuilt from the database.
- Workers run inside the backend process. A restart kills them all, so recovery re-queues every open task.
- The backend drives the host's Docker daemon through a mounted socket, so paths inside the
  backend container are not visible to the daemon. The shared workspace root is the only exception.
- `time.Duration` fields cross the API as integer nanoseconds.

## Dependencies
Prefer the standard library. A new dependency needs one sentence in the PR explaining why the
standard library or an existing dependency falls short.
