#!/usr/bin/env bash
# Vue Flow's stylesheet is ~10KB; shipping it in two CSS bundles doubles that on every cold load.
# Run after `npm run build`.
set -euo pipefail
cd "$(dirname "$0")/.."
n=$(grep -l 'vue-flow__pane' dist/assets/*.css 2>/dev/null | wc -l | tr -d ' ')
echo "CSS bundles containing vue-flow__pane: $n"
[ "$n" -eq 1 ]
