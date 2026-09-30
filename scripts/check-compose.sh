#!/bin/sh
# Guards against re-exposing services on all interfaces or re-adding literal credentials.
set -eu

fail=0

# Capture first: sh has no pipefail, so a failing compose would otherwise feed jq empty input and pass.
cfg=$(docker compose --profile '*' --env-file .env.example config --format json)
ports=$(printf '%s' "$cfg" | jq '[.services[].ports // [] | length] | add')
if [ "${ports:-0}" -eq 0 ]; then
  echo "FAIL: no published ports found in compose config" >&2
  exit 1
fi
open=$(printf '%s' "$cfg" |
  jq -r '.services | to_entries[] | .key as $s | (.value.ports // [])[]
         | select((.host_ip // "") != "127.0.0.1")
         | "\($s): \(.published):\(.target) host_ip=\(.host_ip // "unset")"')
if [ -n "$open" ]; then
  echo "FAIL: ports not bound to 127.0.0.1:" >&2
  echo "$open" >&2
  fail=1
fi

for s in 'minioadmin' 'POSTGRES_PASSWORD: workflow' 'workflow:workflow@'; do
  if grep -nF -- "$s" docker-compose.yml >&2; then
    echo "FAIL: literal credential '$s' in docker-compose.yml" >&2
    fail=1
  fi
done

# A cached local image hides an upstream removal until a fresh clone or CI tries to pull it.
for img in $(printf '%s' "$cfg" | jq -r '[.services[].image // empty] | unique[]'); do
  # With a digest pinned, pull ignores the tag, but manifest inspect rejects tag@digest once the
  # tag has moved on, so resolve by repo@digest exactly as a pull would.
  ref=$(printf '%s' "$img" | sed -E 's/:[^/@]+@/@/')
  if ! docker manifest inspect "$ref" >/dev/null 2>&1; then
    echo "FAIL: image $img cannot be resolved from its registry" >&2
    fail=1
  fi
done

exit $fail
