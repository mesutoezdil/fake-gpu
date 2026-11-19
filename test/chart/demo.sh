#!/usr/bin/env bash
# Render-only contracts for the demo kind config and the chart's dashboards.
set -euo pipefail
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
source "$ROOT/test/lib/common.sh"
CHART="$ROOT/charts/fake-gpu"
KIND="$ROOT/deploy/kind-node/config-chart"
WORK="$(mktemp -d)"
trap 'rm -rf "$WORK"' EXIT
kind_config() { helm template kind "$KIND" --show-only templates/kind.yaml "$@"; }
chart() { helm template mock "$CHART" -n amd-mock "$@"; }
rejects() {
    local label="$1"; shift
    if "$@" >"$WORK/rejected" 2>&1; then fail "$label accepted"; fi
}

kind_config -f "$ROOT/demo/config.yaml" > "$WORK/kind"
has "$WORK/kind" 'hostPort: 8080'
has "$WORK/kind" 'hostPort: 3000'
chart > "$WORK/default"
has "$WORK/default" 'name: mock-fake-gpu-grafana'
has "$WORK/default" 'nodePort: 30300'
has "$WORK/default" 'dns_sd_configs:'
pass 'default kind and chart expose both dashboards and real exporter telemetry'

kind_config --set kind.mockDashboardPort=18080 --set kind.grafanaPort=13000 > "$WORK/custom"
has "$WORK/custom" 'hostPort: 18080'
has "$WORK/custom" 'hostPort: 13000'
pass 'custom host ports render'

kind_config --set dashboard.enabled=false --set monitoring.grafana.enabled=false > "$WORK/off-kind"
lacks "$WORK/off-kind" 'extraPortMappings:'
chart --set dashboard.enabled=false --set monitoring.grafana.enabled=false > "$WORK/off-chart"
lacks "$WORK/off-chart" 'kind: Secret'
lacks "$WORK/off-chart" 'nodePort:'
pass 'disabling dashboards removes host mappings and Grafana workloads'

chart --set monitoring.enabled=false > "$WORK/external"
lacks "$WORK/external" 'kind: Deployment'
pass 'external monitoring excludes bundled Prometheus and Grafana'

rejects 'duplicate host ports' kind_config --set kind.grafanaPort=8080
rejects 'monitoring without exporter' chart --set metricsExporter.enabled=false
pass 'invalid dashboard/monitoring combinations rejected'

"$ROOT/scripts/dashboard-configmap.sh" custom > "$WORK/dashboard.json"
python3 - "$ROOT" "$WORK/dashboard.json" <<'PY'
import json
import sys
from pathlib import Path

root, configmap = Path(sys.argv[1]), json.loads(Path(sys.argv[2]).read_text())
shipped = json.loads((root / 'charts/fake-gpu/files/amd-real-exporter.json').read_text())
assert configmap['metadata']['namespace'] == 'custom' and configmap['metadata']['labels'] == {'grafana_dashboard': '1'}
assert json.loads(configmap['data']['amd-real-exporter.json']) == shipped, 'external dashboard differs from the chart'
assert shipped['uid'] == 'amd-real-exporter' and shipped['refresh'] == '5s', 'tests and docs open the dashboard by uid'
exprs = [t['expr'] for p in shipped['panels'] for t in p['targets']]
assert exprs and all('amd_gpu_' in e for e in exprs), 'panels must query the AMD exporter'
print('PASS: external dashboard ConfigMap carries the chart dashboard')
PY

chart --show-only templates/metrics-exporter/daemonset.yaml > "$WORK/probe-default"
has "$WORK/probe-default" 'timeoutSeconds: 5'
chart --show-only templates/metrics-exporter/daemonset.yaml --set metricsExporter.readinessTimeoutSeconds=10 > "$WORK/probe-custom"
has "$WORK/probe-custom" 'timeoutSeconds: 10'
chart --show-only templates/metrics-exporter/daemonset.yaml --set metricsExporter.readinessTimeoutSeconds=null > "$WORK/probe-old-values"
has "$WORK/probe-old-values" 'timeoutSeconds: 5'
pass 'exporter readiness supports emulated workers, overrides, and older release values'
