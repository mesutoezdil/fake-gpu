#!/usr/bin/env bash
# Maintainer mock-image build; leaves published DRA, node and exporter tags intact.
set -euo pipefail
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
source "$ROOT/scripts/release.env"
PUSH=false
if [ "${1:-}" = --push ]; then PUSH=true; shift; fi
[ "$#" -eq 0 ] || { echo "usage: $0 [--push]" >&2; exit 1; }
cd "$ROOT"
BUILD="$ROOT/tmp/mock-$MOCK_IMAGE_VERSION"
mkdir -p "$BUILD"
cat > "$BUILD/Dockerfile.compiler" <<'DOCKERFILE'
FROM docker.io/library/debian:bookworm-slim
RUN apt-get update && apt-get install -y --no-install-recommends gcc-x86-64-linux-gnu gcc-aarch64-linux-gnu libc6-dev-amd64-cross libc6-dev-arm64-cross && rm -rf /var/lib/apt/lists/*
DOCKERFILE
podman build -f "$BUILD/Dockerfile.compiler" -t localhost/amd-mock-cross-compiler "$BUILD"
go test ./...
for arch in amd64 arm64; do
 context="$BUILD/$arch"
 mkdir -p "$context/bin" "$context/pkg/mocksmi"
 cp -R profiles "$context/"
 CGO_ENABLED=0 GOOS=linux GOARCH="$arch" go build -o "$context/bin/node-agent" ./cmd/node-agent
 CGO_ENABLED=0 GOOS=linux GOARCH="$arch" go build -o "$context/bin/nri-plugin" ./cmd/nri-plugin
 CGO_ENABLED=0 GOOS=linux GOARCH="$arch" go build -o "$context/bin/amd-smi" ./cmd/amd-smi
 CGO_ENABLED=0 GOOS=linux GOARCH="$arch" go build -o "$context/bin/fakegpuctl" ./cmd/fakegpuctl
 compiler=x86_64-linux-gnu-gcc
 [ "$arch" = amd64 ] || compiler=aarch64-linux-gnu-gcc
 mkdir -p "$context/pkg/hipshim" && cp "$ROOT/pkg/hipshim/fakehip.h" "$context/pkg/hipshim/"
 podman run --rm -v "$ROOT/pkg/mocksmi:/src:ro" -v "$context/pkg/mocksmi:/out" localhost/amd-mock-cross-compiler "$compiler" -shared -fPIC -O2 -Wl,-soname,libamd_smi.so.27 -o /out/libamd_smi.so /src/mock_amdsmi.c /src/stubs.c
 podman run --rm -v "$ROOT/pkg/hipshim:/src:ro" -v "$context/pkg/hipshim:/out" localhost/amd-mock-cross-compiler "$compiler" -shared -fPIC -O2 -Wl,-soname,libfakehip.so -o /out/libfakehip.so /src/fakehip.c -lpthread
 podman build --platform "linux/$arch" -f Dockerfile.prebuilt -t "$IMAGE_REGISTRY/fake-gpu:v$MOCK_IMAGE_VERSION-$arch" "$context"
done
for image in "fake-gpu:v$MOCK_IMAGE_VERSION"; do
 manifest="$IMAGE_REGISTRY/$image"
 podman manifest rm "$manifest" >/dev/null 2>&1 || true
 podman manifest create "$manifest"
 for arch in amd64 arm64; do podman manifest add "$manifest" "containers-storage:$manifest-$arch"; done
 if $PUSH; then
   if [ -n "${REGISTRY_AUTHFILE:-}" ]; then
     podman manifest push --authfile "$REGISTRY_AUTHFILE" --all "$manifest" "docker://$manifest"
   else
     podman manifest push --all "$manifest" "docker://$manifest"
   fi
 fi
done
helm lint deployments/helm/fake-gpu --namespace amd-mock
helm package deployments/helm/fake-gpu -d "$BUILD"
if $PUSH; then helm push "$BUILD/fake-gpu-$CHART_VERSION.tgz" "oci://$IMAGE_REGISTRY"; fi
