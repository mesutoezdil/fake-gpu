#!/usr/bin/env bash
# Build and optionally publish the optional CUSE driver image.
set -euo pipefail
cd "$(dirname "$0")/.."
runtime=${CONTAINER_RUNTIME:-podman}
repository=${DRIVER_IMAGE_REPOSITORY:-ghcr.io/mesutoezdil/fake-gpu-driver}
version=${DRIVER_IMAGE_VERSION:-v0.1.0}
push=false
if [ "${1:-}" = --push ]; then push=true; shift; fi
[ "$#" = 0 ] || { echo "usage: $0 [--push]" >&2; exit 2; }
if [ "$runtime" = docker ]; then
  args=(--platform "linux/amd64,linux/arm64" -f deploy/driver-mock/Dockerfile -t "$repository:$version")
  if $push; then args+=(--push); fi
  # Without --push, retain both platforms in the build cache.
  docker buildx build "${args[@]}" .
else
  [ "$runtime" = podman ] || { echo "runtime must be docker or podman" >&2; exit 2; }
  for arch in amd64 arm64; do
    podman build --platform "linux/$arch" -f deploy/driver-mock/Dockerfile -t "$repository:$version-$arch" .
  done
  podman manifest rm "$repository:$version" >/dev/null 2>&1 || true
  podman manifest create "$repository:$version"
  for arch in amd64 arm64; do podman manifest add "$repository:$version" "containers-storage:$repository:$version-$arch"; done
  if $push; then podman manifest push --all "$repository:$version" "docker://$repository:$version"; fi
fi
