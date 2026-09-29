#!/usr/bin/env bash
# Usage: govulncheck-gate.sh <govulncheck -format json output file>
# Fails on reachable vulnerabilities not in .govulncheck-allow, and on allow
# entries that no longer match a finding, so the list cannot rot.
set -euo pipefail

allow_file="${ALLOW_FILE:-$(dirname "$0")/../.govulncheck-allow}"
report="${1:?usage: $0 <govulncheck-json>}"

# A finding whose first trace frame has a "function" reaches called code;
# module- or package-level findings (imported but never called) are noise here.
found=$(jq -r 'select(.finding and (.finding.trace[0] | has("function"))) | .finding.osv' "$report" | sort -u)
allowed=$(sed -e 's/#.*//' -e 's/[[:space:]]*$//' "$allow_file" | grep -v '^$' | sort -u || true)

new=$(comm -23 <(echo "$found") <(echo "$allowed") | grep -v '^$' || true)
stale=$(comm -13 <(echo "$found") <(echo "$allowed") | grep -v '^$' || true)

rc=0
if [ -n "$new" ]; then
  echo "Reachable vulnerabilities not in $allow_file:" >&2
  echo "$new" | sed 's/^/  /' >&2
  rc=1
fi
if [ -n "$stale" ]; then
  echo "Allow-listed IDs that no longer appear (remove them):" >&2
  echo "$stale" | sed 's/^/  /' >&2
  rc=1
fi
[ "$rc" -eq 0 ] && echo "govulncheck gate OK: $(echo "$allowed" | grep -c .) allow-listed, 0 new"
exit "$rc"
