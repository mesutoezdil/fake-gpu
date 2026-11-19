#!/usr/bin/env python3
"""Exercise presenter commands on a dedicated freshly installed demo cluster."""
import os
import sys
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parents[2] / 'lib'))
import e2e

ROOT = Path(__file__).resolve().parents[3]
CTX = os.environ.get('DEMO_CONTEXT', 'kind-amd-mock')
CONFIG = os.environ.get('DEMO_CONFIG', str(ROOT / 'demo/config.yaml'))
MODE = os.environ.get('ALLOCATOR', 'dra')
MOCK = os.environ.get('MOCK_URL', 'http://localhost:8080')
grafana = e2e.Grafana(os.environ.get('GRAFANA_URL', 'http://localhost:3000'),
                      os.environ.get('GRAFANA_ADMIN_PASSWORD', 'amdmock'))
k = e2e.Kubectl(context=CTX)


def run(command, *args):
    result = e2e.run([sys.executable, str(ROOT / 'demo/run.py'), command,
                      '--context', CTX, '--config', CONFIG, *args], timeout=900)
    print(result.stdout + result.stderr, flush=True)
    e2e.ok('presenter command: ' + command)


def gpus():
    return e2e.http_json(MOCK + '/api/gpus', timeout=10)['gpus']


def shows(expr, value, label):
    e2e.wait_for(lambda: value in grafana.numbers(expr), label, timeout=180, interval=2)
    e2e.ok(label)


def main():
    try:
        run('cleanup')
        run('llm')
        if MODE == 'dra':
            run('dra')
        run('multi-gpu', '--count', '2')
        run('allocation')
        run('profiles')
        run('telemetry')
        run('faults', '--action', 'overheat')
        assert gpus()[0]['temperature_c'] == 105
        shows('amd_gpu_edge_temperature{gpu_id="0"}', 105, 'presenter overheat reaches Grafana')
        run('faults', '--action', 'recover')
        run('faults', '--action', 'ecc-error')
        shows('amd_gpu_ecc_uncorrect_total{gpu_id="0"}', 1, 'presenter ECC reaches Grafana')
        run('faults', '--action', 'recover')
        shows('amd_gpu_ecc_uncorrect_total{gpu_id="0"}', 0, 'presenter recovery reaches Grafana')
        run('faults', '--action', 'throttle')
        shows('amd_gpu_violation_processor_hot_residency_percentage{gpu_id="0"}', 100,
              'presenter throttle reaches Grafana through the AMD exporter')
        run('faults', '--action', 'recover')
        run('faults', '--action', 'retire-page')
        shows('gpu_retired_pages{gpu_id="0"}', 1, 'presenter page retirement reaches Grafana')
        run('faults', '--action', 'recover')
        run('faults', '--action', 'xgmi-down')
        shows('gpu_xgmi_link_up{gpu_id="0"}', 0, 'presenter xGMI fault reaches Grafana')
        run('faults', '--action', 'recover')
        shows('gpu_xgmi_link_up{gpu_id="0"}', 1, 'presenter xGMI recovery reaches Grafana')
        shows('gpu_retired_pages{gpu_id="0"}', 0, 'presenter page recovery reaches Grafana')
        run('cleanup')
        run('partitioning', '--mode', 'CPX')
        assert len(gpus()) == 64
        if MODE == 'device-plugin':
            nodes = k.items('nodes')
            assert sum(int(n['status']['allocatable']['amd.com/gpu']) for n in nodes) == 8
        run('partitioning', '--mode', 'SPX')
        assert len(gpus()) == 8
        run('multi-gpu', '--count', '1', '--replicas', '4')
        devices = [k.exec(f'consumer-{i}', 'sh', '-c', 'ls /dev/dri/renderD*', namespace='amd-demo-multi-gpu').stdout.strip()
                   for i in range(4)]
        assert len(set(devices)) == 4, devices
        e2e.ok('four independent physical GPU allocations')
    finally:
        # Best effort: a failed cleanup must not hide the original error.
        try:
            run('cleanup')
        except e2e.CommandError as error:
            print('WARNING: cleanup failed: ' + str(error), flush=True)


if __name__ == '__main__':
    main()
