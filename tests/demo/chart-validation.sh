#!/usr/bin/env bash
# Render-only contracts for the demo kind config and the chart's dashboards.
set -euo pipefail
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
source "$ROOT/tests/lib/common.sh"
CHART="$ROOT/deployments/helm/fake-gpu"
KIND="$ROOT/deployments/kind-node/config-chart"
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

python3 - "$ROOT" <<'PY'
import json
import sys
from pathlib import Path

root = Path(sys.argv[1])
bundle = (root / 'deployments/metrics-exporter/grafana-dashboard.yaml').read_text()
marker = '  amd-real-exporter.json: |\n'
assert marker in bundle, 'grafana-dashboard.yaml no longer embeds amd-real-exporter.json'
embedded = bundle.split(marker, 1)[1]
upstream = json.loads('\n'.join(line[4:] for line in embedded.splitlines()))
shipped = json.loads((root / 'deployments/helm/fake-gpu/files/amd-real-exporter.json').read_text())
assert upstream == shipped, 'chart dashboard differs from deployments/metrics-exporter/grafana-dashboard.yaml'
for source, copy in [('deployments/dra/tiny-llm-demo.yaml', 'demo/llm/dra.yaml'),
                     ('deployments/tiny-llm-demo.yaml', 'demo/llm/device-plugin.yaml'),
                     ('deployments/dra/demo.yaml', 'demo/dra/claim.yaml'),
                     ('deployments/partition-demo.yaml', 'demo/partitioning/physical-workloads.yaml')]:
    expected = (root / source).read_text().replace('  namespace: default\n', '')
    assert expected == (root / copy).read_text(), copy + ' drifted from ' + source
print('PASS: demo manifests and bundled dashboard match tested source assets')
PY

chart --show-only templates/metrics-exporter.yaml > "$WORK/probe-default"
has "$WORK/probe-default" 'timeoutSeconds: 5'
chart --show-only templates/metrics-exporter.yaml --set metricsExporter.readinessTimeoutSeconds=10 > "$WORK/probe-custom"
has "$WORK/probe-custom" 'timeoutSeconds: 10'
chart --show-only templates/metrics-exporter.yaml --set metricsExporter.readinessTimeoutSeconds=null > "$WORK/probe-old-values"
has "$WORK/probe-old-values" 'timeoutSeconds: 5'
pass 'exporter readiness supports emulated workers, overrides, and older release values'
