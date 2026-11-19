#!/usr/bin/env python3
"""Exercise the optional backend against the real renderer and real CUSE."""
import os
import sys
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parents[1] / 'lib'))
import e2e

RUNTIME = os.environ.get('CONTAINER_RUNTIME', 'podman')
IMAGE = os.environ.get('DRIVER_IMAGE', 'localhost/fake-gpu-driver:dev')
NAME = 'amd-main-cuse-check'
PORT = os.environ.get('DRIVER_TEST_PORT', '18080')
ROOT = '/var/lib/fake-gpu'
PROFILES = ['mi210', 'mi250x', 'mi300a', 'mi300x', 'mi325x', 'mi350x', 'mi355x']
MI300X = (304, 206158430208, 'AMD Instinct MI300X')


def container(*args):
    return e2e.run([RUNTIME, *args]).stdout


def api(path):
    return e2e.http_json('http://127.0.0.1:' + PORT + path, timeout=10)


def wait_ready():
    return e2e.wait_for(lambda: api('/api/gpus'), 'provider API ready', timeout=30, interval=0.25)


def probe(index, cu, memory, name, unavailable=False):
    for node in ['card' + str(index), 'renderD' + str(128 + index)]:
        print(container('exec', '-e', 'AMD_DRIVER_MOCK_ROOT=' + ROOT,
                        NAME, 'driver-probe', ROOT + '/dev/dri/' + node,
                        'unavailable' if unavailable else str(cu), str(memory), name), flush=True)


def main():
    # Never replace an existing container of the same name.
    container('run', '-d', '--privileged', '--name', NAME,
              '-p', '127.0.0.1:' + PORT + ':8080', '--entrypoint', 'node-agent', IMAGE,
              '--config=/etc/fake-gpu/profiles/mi300x.yaml',
              '--device-backend=cuse', '--cuse-host-dev=/sandbox-dev', '--root=' + ROOT)
    try:
        wait_ready()
        for profile in PROFILES:
            api('/api/profiles/switch?profile=' + profile)
            gpus = api('/api/gpus')['gpus']
            assert gpus, profile + ': empty fleet'
            for i in range(len(gpus)):
                properties = container('exec', NAME, 'cat', f'{ROOT}/sys/class/kfd/kfd/topology/nodes/{i + 1}/properties')
                props = dict(line.split(maxsplit=1) for line in properties.splitlines() if line.strip())
                smi = container('exec', NAME, 'cat', f'{ROOT}/smi/gpu{i}').splitlines()
                probe(i, int(props['simd_count']) // int(props['simd_per_cu']), int(props['local_mem_size']), smi[1])
            e2e.ok('all native devices and libdrm capacity: ' + profile)
        api('/api/profiles/switch?profile=mi300x')
        api('/api/actions/crash?gpu=0')
        probe(0, *MI300X, unavailable=True)
        api('/api/actions/recover?gpu=0')
        probe(0, *MI300X)
        e2e.ok('dashboard fault/recovery preserves CUSE-backed devices')
        # Abrupt provider loss leaves no falsely successful open. Restart recreates
        # endpoints and replaces stale major numbers using the persisted marker.
        container('kill', NAME)
        container('start', NAME)
        wait_ready()
        probe(7, *MI300X)
        e2e.ok('unclean provider restart and native device access')
    finally:
        logs = e2e.run([RUNTIME, 'logs', NAME], check=False)
        print(logs.stdout + logs.stderr, flush=True)
        e2e.run([RUNTIME, 'rm', '-f', NAME], check=False)


if __name__ == '__main__':
    main()
