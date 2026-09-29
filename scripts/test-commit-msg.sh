#!/bin/sh
set -eu
hook="$(dirname "$0")/../.githooks/commit-msg"
f=$(mktemp)
trap 'rm -f "$f"' EXIT
fail=0
check() { # expected-exit subject
  printf '%s\n' "$2" > "$f"
  rc=0; sh "$hook" "$f" 2>/dev/null || rc=$?
  if [ "$rc" -ne "$1" ]; then echo "WRONG verdict ($rc, want $1): $2"; fail=1; fi
}
check 0 "feat: add hooks"
check 0 "fix(orchestrator): retry on redelivery"
check 0 "refactor(api)!: drop v1 route"
check 0 "chore(deps.go): bump"
check 0 "Merge branch 'main' into x"
check 0 'Revert "feat: x"'
check 0 "fixup! feat: x"
check 0 "squash! feat: x"
check 1 "added some stuff"
check 1 "feat:missing space"
check 1 "Fix: capitalised type"
check 1 "feat(Bad Scope): x"
check 1 "wip"
check 1 "feat: "
exit "$fail"
