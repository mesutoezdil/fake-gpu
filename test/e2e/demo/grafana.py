#!/usr/bin/env python3
"""Check the default host-exposed Grafana and its real AMD metric queries."""
import os
import sys
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parents[2] / 'lib'))
import e2e

COUNT = int(os.environ.get('EXPECTED_GPU_COUNT', '8'))
grafana = e2e.Grafana(os.environ.get('GRAFANA_URL', 'http://localhost:3000'),
                      os.environ.get('GRAFANA_ADMIN_PASSWORD', 'amdmock'))


def check(check_, label):
    value = e2e.wait_for(check_, label, timeout=180, interval=2)
    e2e.ok(label)
    return value


def main():
    check(lambda: grafana.get('/api/health').get('database') == 'ok', 'Grafana host access without port-forward')
    dashboard = check(lambda: grafana.get('/api/dashboards/uid/amd-real-exporter'),
                      'AMD dashboard provisioned by default')['dashboard']
    assert dashboard.get('refresh') == '5s', 'GPU dashboard must refresh automatically'
    e2e.ok('automatic 5-second dashboard refresh')
    check(lambda: COUNT in grafana.numbers('count(amd_gpu_edge_temperature)'),
          'real AMD exporter GPU count through built-in Prometheus')
    targets = [(panel['title'], target['expr']) for panel in dashboard['panels'] for target in panel.get('targets', [])]
    assert targets, 'dashboard has no panel queries'
    for title, expr in targets:
        check(lambda expr=expr: grafana.has_data(expr), 'Grafana panel: ' + title)


if __name__ == '__main__':
    main()
