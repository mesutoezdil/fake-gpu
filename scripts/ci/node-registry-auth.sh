#!/usr/bin/env bash
# Give a kind cluster's nodes the registry credentials of this machine, so
# pods can pull private images. Run after `docker login`.
set -euo pipefail
cluster="${1:?usage: $0 <kind cluster name>}"
config="${DOCKER_CONFIG:-$HOME/.docker}/config.json"
[ -f "$config" ] || { echo "no $config; run docker login first" >&2; exit 1; }
for node in $(kind get nodes --name "$cluster"); do
  docker cp "$config" "$node:/var/lib/kubelet/config.json"
  docker exec "$node" systemctl restart kubelet.service
done
kubectl --context "kind-$cluster" wait --for=condition=Ready nodes --all --timeout=120s
