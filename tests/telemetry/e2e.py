#!/usr/bin/env python3
"""Exercise real AMD exporter -> Prometheus -> Grafana and the SMI state bridge."""
import os
import sys
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parents[1] / 'lib'))
import e2e

MOCK = os.environ.get('MOCK_URL', 'http://localhost:8080')
prometheus = e2e.Prometheus(os.environ.get('PROMETHEUS_URL', 'http://localhost:9090'))
grafana = e2e.Grafana(os.environ.get('GRAFANA_URL', 'http://localhost:3000'),
                      os.environ.get('GRAFANA_ADMIN_PASSWORD', 'amdmock'))


def mock(path, post=False):
    return e2e.http_json(MOCK + path, method='POST' if post else None)


def check(check_, label, timeout=120):
    value = e2e.wait_for(check_, label, timeout=timeout, interval=2)
    e2e.ok(label)
    return value


def gpu0(metric, test):
    """Some sample of metric on GPU 0 passes test."""
    return any(test(v) for v in prometheus.values(metric + '{gpu_id="0"}'))


def healthy_targets():
    targets = e2e.http_json(prometheus.base + '/api/v1/targets')['data']['activeTargets']
    targets = [t for t in targets if 'metrics-exporter' in t['labels'].get('service', '')]
    return targets if targets and all(t['health'] == 'up' for t in targets) else None


def main():
    gpus = mock('/api/gpus')['gpus']
    assert gpus, 'mock fleet is empty'
    count = len(gpus)
    temperatures = check(lambda: (r if len(r := prometheus.query('amd_gpu_edge_temperature')) == count else None),
                         'real AMD exporter GPU count')
    ids = {r['metric']['gpu_id'] for r in temperatures}
    assert ids == {str(g['index']) for g in gpus}, ids
    assert all(r['metric']['card_model'] == gpus[0]['name'] for r in temperatures)
    assert len({r['metric']['serial_number'] for r in temperatures}) == count
    assert all(0 < float(r['value'][1]) < 120 for r in temperatures), temperatures
    e2e.ok('GPU identities, model, serials and temperature units')
    for metric in ['amd_gpu_package_power', 'amd_gpu_gfx_activity', 'amd_gpu_used_vram', 'amd_gpu_ecc_uncorrect_total']:
        assert len(prometheus.query(metric)) == count, metric
    total = prometheus.query('amd_gpu_total_vram')
    assert len(total) == count, total
    assert all(float(r['value'][1]) == gpus[int(r['metric']['gpu_id'])]['memory_total_mb'] for r in total), total
    clocks = prometheus.query('avg by (gpu_id) (amd_gpu_clock{clock_type="system"})')
    assert len(clocks) == count and all(float(r['value'][1]) > 0 for r in clocks), clocks
    e2e.ok('power, utilization, ECC, clocks and VRAM values/units')
    check(healthy_targets, 'exporter ServiceMonitor target is UP')

    original = gpus[0]['ecc_errors']
    assert gpus[0]['status'] == 'healthy', 'run fault test with GPU 0 healthy'
    try:
        mock('/api/actions/ecc-error?gpu=0', post=True)
        check(lambda: gpu0('amd_gpu_ecc_uncorrect_total', lambda v: v == original + 1),
              'dashboard ECC action reaches Prometheus through AMD SMI')
    finally:
        # The ECC action toggles, so a second call clears it again.
        mock('/api/actions/ecc-error?gpu=0', post=True)
    check(lambda: gpu0('amd_gpu_ecc_uncorrect_total', lambda v: v == original), 'ECC recovery reaches Prometheus')

    # Exercise the exact dashboard endpoints, with recovery even on failure.
    total_mb, used_mb = gpus[0]['memory_total_mb'], gpus[0]['memory_used_mb']
    actions = [
        ('overheat', [('amd_gpu_edge_temperature', lambda v: v == 105)]),
        ('busy', [('amd_gpu_gfx_activity', lambda v: v >= 40), ('amd_gpu_package_power', lambda v: v > 0),
                  ('amd_gpu_used_vram', lambda v: v == total_mb * 70 // 100)]),
        ('idle', [('amd_gpu_gfx_activity', lambda v: 0 <= v <= 7), ('amd_gpu_used_vram', lambda v: v == used_mb)]),
        ('throttle', [('amd_gpu_violation_processor_hot_residency_percentage', lambda v: v == 100),
                      ('amd_gpu_violation_socket_thermal_residency_percentage', lambda v: v == 100)]),
        ('crash', [('amd_gpu_package_power', lambda v: v == 0), ('amd_gpu_gfx_activity', lambda v: v == 0)]),
        ('recover', [('amd_gpu_violation_processor_hot_residency_percentage', lambda v: v == 0),
                     ('amd_gpu_edge_temperature', lambda v: 0 < v < 95), ('amd_gpu_package_power', lambda v: v > 0)]),
    ]
    try:
        for action, fields in actions:
            mock('/api/actions/' + action + '?gpu=0', post=True)
            check(lambda fields=fields: all(gpu0(metric, test) for metric, test in fields),
                  'dashboard ' + action + ' reaches Prometheus through AMD SMI')
    finally:
        mock('/api/actions/recover?gpu=0', post=True)

    dashboard = check(lambda: grafana.get('/api/dashboards/uid/amd-real-exporter'), 'Grafana dashboard provisioned')['dashboard']
    datasource = grafana.get('/api/datasources/name/Prometheus')['uid']
    targets = [(panel['title'], target['expr']) for panel in dashboard['panels'] for target in panel.get('targets', [])]
    assert targets, 'dashboard has no panel queries'
    for title, expr in targets:
        assert 'amd_gpu_' in expr, expr
        assert grafana.has_data(expr, datasource), title + ': no data for ' + expr
    e2e.ok('every Grafana panel obtains real exporter data through its Prometheus datasource')


if __name__ == '__main__':
    main()
