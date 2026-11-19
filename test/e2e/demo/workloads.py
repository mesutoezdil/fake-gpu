#!/usr/bin/env python3
"""Published-cluster demo checks. Run MODE=dra or MODE=device-plugin separately."""
import os
import sys
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parents[2] / 'lib'))
import e2e

MODE = os.environ.get('MODE', 'dra')
ROOT = Path(__file__).resolve().parents[3]
k = e2e.Kubectl()
api = e2e.MockAPI(k)


def gone(label):
    e2e.wait_for(lambda: not k.items('pods', selector=label), 'demo pods with ' + label + ' deleted')


def devices(pod, count):
    k.exec(pod, 'sh', '-c', e2e.device_probe(count))


def deployed(manifest, label, deployments):
    """Apply a demo manifest, wait for its deployments, return its live pods."""
    k('apply', '-f', str(manifest))
    for deployment in deployments:
        k('rollout', 'status', 'deployment/' + deployment, '--timeout=180s')
    return [p for p in k.items('pods', selector=label) if not p['metadata'].get('deletionTimestamp')]


def remove(manifest, label):
    k('delete', '-f', str(manifest), '--ignore-not-found', '--wait=true', '--timeout=180s')
    gone(label)


def tiny_llm():
    manifest = ROOT / ('deploy/workloads/tiny-llm-dra.yaml' if MODE == 'dra' else 'deploy/workloads/tiny-llm-device-plugin.yaml')
    label = 'app=' + ('tiny-llm-dra' if MODE == 'dra' else 'tiny-llm')
    deployment = 'tiny-llm-dra-demo' if MODE == 'dra' else 'tiny-llm-demo'
    try:
        pods = deployed(manifest, label, [deployment])
        assert len(pods) == 1, [p['metadata']['name'] for p in pods]
        pod = pods[0]['metadata']['name']
        devices(pod, 1)
        e2e.wait_for(lambda: 'LLM service ready.' in k.out('logs', pod), 'demo output finished', timeout=30)
        e2e.ok(f'{MODE} Tiny LLM scheduled, exact GPU injected, scripted output finished')
    finally:
        remove(manifest, label)


def virtual_partitions():
    # No GPU consumers remain. Partition API is a virtual model, not an allocator.
    try:
        for mode, parts in [('DPX', 2), ('CPX', 8), ('QPX', 4), ('SPX', 1), ('CPX', 8), ('SPX', 1)]:
            api('/api/partitions/set?mode=' + mode, post=True)
            gpus = api('/api/gpus')['gpus']
            assert len(gpus) == 8 * parts, (mode, len(gpus))
            assert len({g['uuid'] for g in gpus}) == len(gpus), mode + ': duplicate UUIDs'
            assert all(g['memory_total_mb'] == 192 * 1024 // parts for g in gpus), mode + ': memory split'
            assert len({g['pci_bdf'] for g in gpus}) == 8, mode + ': physical BDFs'
            if MODE == 'dra':
                slices = k.items('resourceslices')
                assert sum(len(s['spec'].get('devices', [])) for s in slices) == 8, mode + ': ResourceSlice devices'
            else:
                nodes = k.items('nodes')
                assert int(nodes[0]['status']['allocatable']['amd.com/gpu']) == 8, mode + ': allocatable'
            e2e.ok(f'{MODE} {mode}: {len(gpus)} virtual entries; physical capacity stays 8')
    finally:
        api('/api/partitions/set?mode=SPX', post=True)


def physical_replicas():
    manifest = ROOT / 'deploy/workloads/partition-replicas.yaml'
    label = 'app=partition-demo'
    try:
        pods = deployed(manifest, label, ['llm-inference-a', 'llm-inference-b'])
        assert len(pods) == 4, [p['metadata']['name'] for p in pods]
        allocated = []
        for pod in pods:
            name = pod['metadata']['name']
            devices(name, 1)
            allocated.append(k.exec(name, 'sh', '-c', 'ls /dev/dri/renderD*').stdout.strip())
        assert len(set(allocated)) == 4, allocated
        e2e.ok('four simultaneous LLM replicas receive four distinct physical GPUs')
    finally:
        remove(manifest, label)


def main():
    assert MODE in ('dra', 'device-plugin'), 'MODE must be dra or device-plugin'
    tiny_llm()
    virtual_partitions()
    if MODE == 'device-plugin':
        physical_replicas()


if __name__ == '__main__':
    main()
