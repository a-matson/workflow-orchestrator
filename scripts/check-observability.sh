#!/bin/sh
# The observability profile must define Prometheus and Grafana, and Prometheus'
# config and alert rules must load. Static: runs without starting the stack.
set -eu

fail=0
cfg=$(docker compose --profile observability --env-file .env.example config --format json)
for s in prometheus grafana; do
  if ! printf '%s' "$cfg" | jq -e --arg s "$s" '.services[$s].profiles // [] | index("observability")' >/dev/null; then
    echo "FAIL: no $s service in the observability profile" >&2
    fail=1
  fi
done

image=$(printf '%s' "$cfg" | jq -r '.services.prometheus.image // empty')
if [ -z "$image" ]; then
  exit 1
fi
if [ ! -f prometheus-alerts.yml ]; then
  echo "FAIL: prometheus-alerts.yml is missing" >&2
  exit 1
fi
# promtool ships in the Prometheus image, so the check uses the pinned version.
if ! docker run --rm --entrypoint promtool -v "$PWD:/cfg:ro" -w /cfg "$image" \
  check config --syntax-only prometheus.yml; then
  fail=1
fi
if ! docker run --rm --entrypoint promtool -v "$PWD:/cfg:ro" -w /cfg "$image" \
  check rules prometheus-alerts.yml; then
  fail=1
fi
exit $fail
