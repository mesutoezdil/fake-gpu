#!/usr/bin/env python3
"""Ensure disabled dashboards have no host exposure or Grafana workload."""
import os
import sys
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parents[1] / 'lib'))
import e2e

k = e2e.Kubectl(namespace='amd-mock', context=os.environ.get('DEMO_CONTEXT', 'kind-amd-mock'))
URLS = [os.environ.get('MOCK_URL', 'http://localhost:8080') + '/api/gpus',
        os.environ.get('GRAFANA_URL', 'http://localhost:3000') + '/api/health']


def closed(url):
    try:
        e2e.http(url, timeout=3)
    except OSError:
        return True
    return False


def main():
    service = k.get('svc', 'fake-gpu-metrics')
    assert service['spec']['type'] == 'ClusterIP', service['spec']['type']
    assert all('nodePort' not in p for p in service['spec']['ports']), service['spec']['ports']
    assert not k.out('get', 'deploy/fake-gpu-grafana', '-n', 'amd-mock', '--ignore-not-found').strip(), \
        'Grafana deployment still exists'
    for url in URLS:
        e2e.wait_for(lambda url=url: closed(url), 'dashboard no longer exposed: ' + url, timeout=60, interval=2)
        e2e.ok('disabled host dashboard: ' + url)


if __name__ == '__main__':
    main()
