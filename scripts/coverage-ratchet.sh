#!/usr/bin/env bash
# Usage: coverage-ratchet.sh <backend|frontend> <measured-pct>
# Fails when coverage falls below the recorded baseline; there is no absolute target.
set -eu

[ $# -eq 2 ] || { echo "usage: $0 <backend|frontend> <measured-pct>" >&2; exit 2; }
key=$1
measured=$2
baseline=$(jq -er --arg k "$key" '.[$k]' "$(dirname "$0")/../coverage-baseline.json")

# 0.5 points of slack: coverage shifts slightly between runs (races, timing-dependent branches).
if awk -v m="$measured" -v b="$baseline" 'BEGIN{exit !(m < b - 0.5)}'; then
  echo "coverage ratchet: $key coverage $measured% is below baseline $baseline%" >&2
  exit 1
fi
if awk -v m="$measured" -v b="$baseline" 'BEGIN{exit !(m > b + 1)}'; then
  echo "coverage ratchet: $key coverage $measured% beats baseline $baseline%; raise it in coverage-baseline.json"
fi
echo "coverage ratchet: $key $measured% (baseline $baseline%) ok"
