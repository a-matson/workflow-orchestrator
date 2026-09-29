#!/bin/sh
# Usage: check-image-arch.sh linux/arm64|linux/amd64
set -eu

platform=$1
tmp=$(mktemp -d)
img=check-image-arch:$$
cid=
cleanup() {
  [ -z "$cid" ] || docker rm -f "$cid" >/dev/null 2>&1 || true
  docker rmi -f "$img" >/dev/null 2>&1 || true
  rm -rf "$tmp"
}
trap cleanup EXIT

# Checks the builder stage so the result does not depend on the runtime stage building.
# Loaded and copied out because exporting the whole builder rootfs (Go toolchain,
# read-only module cache) to a local dir is slow and fails on permissions.
docker buildx build --platform "$platform" --target builder --load -t "$img" ./backend
cid=$(docker create --platform "$platform" "$img")
docker cp "$cid:/workflow-server" "$tmp/workflow-server"

case $platform in
  linux/arm64) want=b7 ;;
  linux/amd64) want=3e ;;
  *) echo "unsupported platform: $platform" >&2; exit 2 ;;
esac

got=$(od -An -tx1 -j18 -N1 "$tmp/workflow-server" | tr -d ' \n')
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
