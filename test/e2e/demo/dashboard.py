#!/usr/bin/env python3
"""Verify automatic host access through the quick-start kind mapping."""
import json
import os
import sys
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parents[2] / 'lib'))
import e2e

BASE = os.environ.get('DASHBOARD_URL', 'http://127.0.0.1:8080').rstrip('/')
EXPECTED = int(os.environ.get('EXPECTED_GPU_COUNT', '8'))
PROFILES = 7


def read(path):
    status, body = e2e.wait_for(lambda: e2e.http(BASE + path, timeout=5), 'GET ' + BASE + path)
    assert status == 200, f'GET {path}: HTTP {status}'
    return body


def main():
    page = read('/')
    assert '<title>fake-gpu console</title>' in page and '</html>' in page, 'Dashboard HTML missing'
    e2e.ok('dashboard HTML reachable from host without port-forward')
    fleet = json.loads(read('/api/gpus'))['gpus']
    assert len(fleet) == EXPECTED, f'{len(fleet)} GPUs, want {EXPECTED}'
    assert len({g['uuid'] for g in fleet}) == EXPECTED, 'duplicate GPU UUIDs'
    e2e.ok(f'host dashboard API reports {EXPECTED} distinct GPUs')
    profiles = json.loads(read('/api/profiles'))
    assert len(profiles) == PROFILES, f'{len(profiles)} profiles, want {PROFILES}'
    e2e.ok('host dashboard profile API responds')
    assert 'gpu_temperature' in read('/metrics'), 'gpu_temperature missing from /metrics'
    e2e.ok('host metrics endpoint responds')


if __name__ == '__main__':
    main()
