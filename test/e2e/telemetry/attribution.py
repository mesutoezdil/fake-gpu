#!/usr/bin/env python3
"""Confirm the real AMD exporter attaches labels from kubelet GPU allocations."""
import os
import sys
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parents[2] / 'lib'))
import e2e

MODE = os.environ.get('ALLOCATOR', 'dra')
NS = 'telemetry-test-' + str(os.getpid())
prometheus = e2e.Prometheus(os.environ.get('PROMETHEUS_URL', 'http://localhost:9090'))
k = e2e.Kubectl(namespace=NS)


def main():
    assert MODE in ('dra', 'device-plugin'), 'ALLOCATOR must be dra or device-plugin'
    with e2e.scratch_namespace(k, NS):
        container = {'name': 'consumer', 'image': 'docker.io/library/busybox:1.36',
                     'command': ['sh', '-c', 'test -c /dev/kfd && ls /dev/dri/renderD* && sleep 600']}
        spec = {'terminationGracePeriodSeconds': 0, 'restartPolicy': 'Never', 'containers': [container]}
        if MODE == 'dra':
            k.apply({'apiVersion': 'resource.k8s.io/v1', 'kind': 'ResourceClaimTemplate', 'metadata': {'name': 'gpu'},
                     'spec': {'spec': {'devices': {'requests': [{'name': 'gpu', 'exactly': {'deviceClassName': 'gpu.amd.com'}}]}}}})
            spec['resourceClaims'] = [{'name': 'gpu', 'resourceClaimTemplateName': 'gpu'}]
            container['resources'] = {'claims': [{'name': 'gpu'}]}
        else:
            container['resources'] = {'limits': {'amd.com/gpu': 1}}
        k.apply({'apiVersion': 'v1', 'kind': 'Pod', 'metadata': {'name': 'gpu-consumer'}, 'spec': spec})
        k.ready('gpu-consumer', timeout='120s')
        logs = k.out('logs', 'gpu-consumer', '-n', NS)
        assert 'renderD' in logs, logs
        expr = 'amd_gpu_edge_temperature{pod="gpu-consumer",namespace="' + NS + '",container="consumer"}'
        rows = e2e.wait_for(lambda: prometheus.query(expr), 'allocated GPU metrics carry consumer pod/namespace/container labels',
                            timeout=120, interval=2)
        assert len(rows) == 1, rows
        e2e.ok(MODE + ' allocation -> kubelet pod-resources -> real AMD exporter workload labels')


if __name__ == '__main__':
    main()
