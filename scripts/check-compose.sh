#!/bin/sh
# Guards against re-exposing services on all interfaces or re-adding literal credentials.
set -eu

fail=0

# --profile: redis-commander is profile-gated and would otherwise be skipped.
open=$(docker compose --profile '*' --env-file .env.example config --format json |
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

exit $fail
