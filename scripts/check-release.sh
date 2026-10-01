#!/bin/sh
# Usage: check-release.sh <binary> <version>
# Checks what a tagged release depends on: the binary carries the version it was
# built with, and CI publishes both images when a v* tag is pushed.
set -eu

bin=${1:-}
want=${2:-}
[ -n "$bin" ] && [ -n "$want" ] || { echo "usage: $0 <binary> <version>" >&2; exit 2; }
ci=.github/workflows/ci.yml
fail=0

# -trimpath drops -ldflags from the build info, but an -X string is stored
# verbatim, so search the binary instead of running one built for another platform.
if ! grep -aqF -- "$want" "$bin"; then
  echo "FAIL: $bin was not built with -X main.version=$want (the image would report \"dev\")" >&2
  fail=1
fi

if ! grep -Eq "tags: \[['\"]?v\*" "$ci"; then
  echo "FAIL: $ci does not run on v* tags, so a tag publishes nothing" >&2
  fail=1
fi

if ! grep -q 'context: ./frontend' "$ci"; then
  echo "FAIL: $ci never builds or pushes the frontend image" >&2
  fail=1
fi

[ "$fail" -eq 0 ] && echo "release check: ok ($want)"
exit "$fail"
