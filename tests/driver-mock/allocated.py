#!/usr/bin/env python3
"""Prove native opens and libdrm queries inside genuinely allocated Pods."""
import os
import sys
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parents[1] / 'lib'))
import e2e

MODE = os.environ.get('ALLOCATOR', 'dra')
IMAGE = os.environ.get('DRIVER_IMAGE', 'local/fake-gpu-driver:test')
NS = 'amd-driver-consumer-test'
PARTITION = os.environ.get('PARTITION_MODE', 'SPX')
SHAPES = {'SPX': (304, 206158430208), 'DPX': (152, 103079215104)}
MARKER = 'PASS: allocated workload native KFD/render access and libdrm capacity'
k = e2e.Kubectl(namespace=NS)


def consumer(cu, vram):
    spec = {'restartPolicy': 'Never', 'containers': [{
        'name': 'consumer', 'image': IMAGE, 'imagePullPolicy': 'Never',
        'env': [{'name': 'LD_LIBRARY_PATH', 'value': '/var/lib/fake-gpu/driver/usr/lib64'}],
        'command': ['sh', '-ec', f'''
set -- /dev/dri/renderD*
test "$#" = 1
test -c "$1"
exec 3<>/dev/kfd
driver-probe "$1" {cu} {vram} 'AMD Instinct MI300X'
echo '{MARKER}'
'''],
        'volumeMounts': [{'name': 'state', 'mountPath': '/var/lib/fake-gpu', 'readOnly': True}]}],
        'volumes': [{'name': 'state', 'hostPath': {'path': '/var/lib/fake-gpu', 'type': 'Directory'}}]}
    if MODE == 'dra':
        spec['resourceClaims'] = [{'name': 'gpu', 'resourceClaimTemplateName': 'gpu'}]
        spec['containers'][0]['resources'] = {'claims': [{'name': 'gpu'}]}
    else:
        spec['containers'][0]['resources'] = {'limits': {'amd.com/gpu': 1}}
    return {'apiVersion': 'v1', 'kind': 'Pod', 'metadata': {'name': 'consumer'}, 'spec': spec}


def finished():
    phase = k.get('pod', 'consumer')['status'].get('phase')
    return phase if phase in ('Succeeded', 'Failed') else None


def main():
    assert MODE in ('dra', 'device-plugin'), 'ALLOCATOR must be dra or device-plugin'
    assert PARTITION in SHAPES, f'PARTITION_MODE must be one of {sorted(SHAPES)}'
    cu, vram = SHAPES[PARTITION]
    # Waiting for the deletion lets the next run reuse the namespace name.
    with e2e.scratch_namespace(k, NS, wait=True):
        if MODE == 'dra':
            k.apply({'apiVersion': 'resource.k8s.io/v1', 'kind': 'ResourceClaimTemplate',
                     'metadata': {'name': 'gpu'}, 'spec': {'spec': {'devices': {'requests': [
                         {'name': 'gpu', 'exactly': {'deviceClassName': 'gpu.amd.com'}}]}}}})
        k.apply(consumer(cu, vram))
        phase = e2e.wait_for(finished, 'allocated consumer completes', timeout=180, interval=2)
        logs = k.out('logs', 'consumer', '-n', NS)
        print(logs, flush=True)
        assert phase == 'Succeeded', k.get('pod', 'consumer')['status']
        assert MARKER in logs, 'consumer did not report success'


if __name__ == '__main__':
    main()
