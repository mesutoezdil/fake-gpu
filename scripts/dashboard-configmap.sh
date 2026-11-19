#!/usr/bin/env bash
# Print the Grafana dashboard ConfigMap for an external monitoring stack:
# the chart's own dashboard, labelled for the kube-prometheus-stack sidecar.
#
#   scripts/dashboard-configmap.sh [namespace] | kubectl apply -f -
set -euo pipefail
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
namespace="${1:-monitoring}"
python3 - "$ROOT/charts/fake-gpu/files/amd-real-exporter.json" "$namespace" <<'PY'
import json
import sys

dashboard, namespace = sys.argv[1], sys.argv[2]
with open(dashboard) as f:
    body = json.load(f)
print(json.dumps({
    'apiVersion': 'v1',
    'kind': 'ConfigMap',
    'metadata': {'name': 'amd-real-exporter-dashboard', 'namespace': namespace,
                 'labels': {'grafana_dashboard': '1'}},
    'data': {'amd-real-exporter.json': json.dumps(body, indent=2) + '\n'},
}, indent=2))
PY
