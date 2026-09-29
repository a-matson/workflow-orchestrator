#!/bin/sh
# Usage: check-image-arch.sh linux/arm64|linux/amd64 [binary]
# With no binary, builds the Dockerfile's `binaries` stage for the platform into a temp dir.
set -eu

platform=${1:-}
bin=${2:-}
[ -n "$platform" ] || { echo "usage: $0 linux/arm64|linux/amd64 [binary]" >&2; exit 2; }

case $platform in
  linux/arm64) want=b7 ;;
  linux/amd64) want=3e ;;
  *) echo "unsupported platform: $platform" >&2; exit 2 ;;
esac

if [ -z "$bin" ]; then
  tmp=$(mktemp -d)
  trap 'rm -rf "$tmp"' EXIT INT TERM
  # `binaries` is FROM scratch, so only the cross-compiled binary is exported.
  docker buildx build --platform "$platform" --target binaries -o "type=local,dest=$tmp" ./backend
  bin=$tmp/workflow-server
fi

# ELF e_machine is the 2 bytes at offset 18; the low byte identifies both targets.
got=$(od -An -tx1 -j18 -N1 "$bin" | tr -d ' \n')
case $got in
  3e) name=x86-64 ;;
  b7) name=aarch64 ;;
  *) name=unknown ;;
esac

if [ "$got" != "$want" ]; then
  echo "FAIL: $platform image contains a $name binary"
  exit 1
fi
echo "OK: $platform"
