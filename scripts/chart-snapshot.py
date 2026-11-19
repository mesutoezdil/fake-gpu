#!/usr/bin/env python3
"""Render the chart under many value sets and print a normalized snapshot.

    scripts/chart-snapshot.py CHART > snapshot.json

Resources are keyed by kind, namespace and name with template source
comments dropped, so two snapshots are equal exactly when the charts render
the same objects, whatever template files they come from.
"""
import json
import subprocess
import sys

import yaml

PROFILES = ['mi210', 'mi250x', 'mi300a', 'mi300x', 'mi325x', 'mi350x', 'mi355x']
CASES = {
    'default': [],
    'device-plugin': ['--set', 'dra.enabled=false', '--set', 'devicePlugin.enabled=true'],
    'no-monitoring': ['--set', 'monitoring.enabled=false'],
    'no-exporter': ['--set', 'monitoring.enabled=false', '--set', 'metricsExporter.enabled=false'],
    'no-dashboards': ['--set', 'dashboard.enabled=false', '--set', 'monitoring.grafana.enabled=false'],
    'service-monitors': ['--set', 'prometheus.serviceMonitor.enabled=true', '--set', 'metricsExporter.serviceMonitor.enabled=true'],
    'driver-mock': ['--set', 'driverMock.enabled=true'],
    'kernel-log-nri': ['--set', 'kernelLog.enabled=true', '--set', 'nri.enabled=true'],
    'dpx': ['--set', 'gpu.partition=DPX'],
    'custom-root': ['--set-string', 'nodeSelector.mock=true', '--set-string', 'dra.kubeletPlugin.nodeSelector.mock=true',
                    '--set', 'mockRootDir=/custom/mock', '--set', 'dra.mockSysPath=/custom/mock/sys'],
    'image-overrides': ['--set', 'image.repository=local/x', '--set', 'image.tag=t', '--set', 'image.pullPolicy=Never',
                        '--set', 'metricsExporter.image=local/e:t', '--set', 'metricsExporter.readinessTimeoutSeconds=10'],
    'old-values': ['--set', 'metricsExporter.readinessTimeoutSeconds=null', '--set', 'gpu.partition=null'],
    'renamed-release': ['--set', 'fullnameOverride=other'],
    **{'profile-' + p: ['--set', 'gpu.profile=' + p] for p in PROFILES},
}
INVALID = {
    'dra-and-plugin': ['--set', 'devicePlugin.enabled=true'],
    'monitoring-without-exporter': ['--set', 'metricsExporter.enabled=false'],
    'real-sys': ['--set', 'dra.mockSysPath=/sys'],
    'selector-mismatch': ['--set-string', 'nodeSelector.mock=true'],
    'bad-partition': ['--set', 'gpu.partition=CPX'],
    'dpx-wrong-profile': ['--set', 'gpu.partition=DPX', '--set', 'gpu.profile=mi210'],
}


def render(chart, args):
    return subprocess.run(['helm', 'template', 'fake-gpu', chart, '-n', 'amd-mock', *args],
                          capture_output=True, text=True, check=False)


def main():
    chart = sys.argv[1]
    snapshot = {}
    for case, args in CASES.items():
        result = render(chart, args)
        if result.returncode:
            raise SystemExit(f'{case}: {result.stderr}')
        objects = {}
        for doc in yaml.safe_load_all(result.stdout):
            if not doc:
                continue
            key = '/'.join([doc['kind'], doc['metadata'].get('namespace', ''), doc['metadata']['name']])
            assert key not in objects, f'{case}: duplicate {key}'
            objects[key] = doc
        snapshot[case] = objects
    for case, args in INVALID.items():
        result = render(chart, args)
        if not result.returncode:
            raise SystemExit(f'{case}: invalid values rendered')
        message = [line for line in result.stderr.splitlines() if 'error' in line.lower()]
        snapshot['invalid-' + case] = message[-1].split(': ')[-1] if message else result.stderr
    json.dump(snapshot, sys.stdout, indent=1, sort_keys=True)


if __name__ == '__main__':
    main()
