#!/usr/bin/env python3
"""Prove fixed MI300X DPX/NPS2 same-parent allocation on an installed DRA cluster.
Uses a dedicated namespace; keeps the final two-container demo for inspection.
KUBECONFIG selects the cluster. No current-context mutation.
"""
import json
import os
import sys
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parents[2] / 'lib'))
import e2e

NS = os.environ.get('PARTITION_NS', 'amd-demo-partition-allocation')
MOCK_NS = 'amd-mock'
DRIVER = 'gpu.amd.com'
k = e2e.Kubectl(namespace=NS)


def devices():
    return {d['name']: d for s in k.items('resourceslices', namespace=None)
            if s['spec']['driver'] == DRIVER for d in s['spec']['devices']}


def parent(d):
    return d['attributes']['resource.kubernetes.io/pciBusID']['string']


def request(name, bdf=None):
    expression = 'device.attributes["gpu.amd.com"].type == "amdgpu-partition"'
    if bdf:
        expression += ' && device.attributes["resource.kubernetes.io"].pciBusID == ' + json.dumps(bdf)
    return {'name': name, 'exactly': {'deviceClassName': DRIVER, 'count': 1,
                                     'selectors': [{'cel': {'expression': expression}}]}}


def claim(name, requests, same_parent=False):
    spec = {'requests': requests}
    if same_parent:
        spec['constraints'] = [{'requests': [r['name'] for r in requests],
                                'matchAttribute': 'resource.kubernetes.io/pciBusID'}]
    k.apply({'apiVersion': 'resource.k8s.io/v1', 'kind': 'ResourceClaim',
             'metadata': {'name': name}, 'spec': {'devices': spec}})


def pod(name, claim_name, containers=None):
    containers = containers or [('consumer', 'gpu')]
    k.apply({'apiVersion': 'v1', 'kind': 'Pod', 'metadata': {'name': name}, 'spec': {
        'terminationGracePeriodSeconds': 1, 'restartPolicy': 'Never',
        'resourceClaims': [{'name': 'partition', 'resourceClaimName': claim_name}],
        'containers': [{'name': c, 'image': 'docker.io/busybox:1.36',
                        'command': ['sh', '-c', 'test -c /dev/kfd; ls -l /dev/kfd /dev/dri; sleep 86400'],
                        'resources': {'claims': [{'name': 'partition', 'request': r}]}}
                       for c, r in containers]}})


def evidence(pod_name, claim_name, container='consumer', req='gpu'):
    result = [a for a in k.allocation(claim_name) if a['request'] == req]
    assert len(result) == 1, result
    assert result[0]['driver'] == DRIVER
    d = devices()[result[0]['device']]
    assert d['attributes']['type']['string'] == 'amdgpu-partition', d
    assert d['attributes']['partitionProfile']['string'] == 'dpx_nps2', d
    assert d['capacity']['memory']['value'] == '96Gi', d
    expected = 'renderD' + result[0]['device'].split('-')[-1]
    output = k.exec(pod_name, 'sh', '-c',
                    'test -c /dev/kfd && for f in /dev/dri/renderD*; do test -c "$f" || exit 1; basename "$f"; done',
                    container=container).stdout
    assert output.strip().splitlines() == [expected], output
    print(f'EVIDENCE: {pod_name}/{container}: claim={claim_name} request={req} '
          f'device={result[0]["device"]} parent={parent(d)} memory=96Gi render={expected}', flush=True)
    print(k.out('logs', '-n', NS, pod_name, '-c', container).strip(), flush=True)
    return result[0]['device'], parent(d)


def cdi_exists(uid):
    # An exec failure raises instead of reading as "file absent".
    path = f'/var/run/cdi/k8s.gpu.amd.com-gpu_{uid}.yaml'
    return e2e.file_exists(k, 'daemonset/fake-gpu', path, namespace=MOCK_NS)


def driver_pods():
    return {p['metadata']['uid'] for p in k.items('pods', namespace=MOCK_NS, selector='app.kubernetes.io/name=dra')}


def delete(name):
    k.delete('pod', name)
    k.delete('resourceclaim', name)


def main():
    ds = e2e.wait_for(lambda: (d if len(d := devices()) == 16 else None),
                      'AMD driver advertises 16 partitions for 8 physical MI300X GPUs', timeout=180)
    e2e.ok('AMD driver advertises 16 partitions for 8 physical MI300X GPUs')
    parents = {parent(d) for d in ds.values()}
    assert len(parents) == 8, parents
    for address in parents:
        assert sum(parent(d) == address for d in ds.values()) == 2, address
    for d in ds.values():
        assert d['attributes']['type']['string'] == 'amdgpu-partition', d
        assert d['attributes']['partitionProfile']['string'] == 'dpx_nps2', d
        assert d['capacity']['memory']['value'] == '96Gi', d
        assert d['capacity']['computeUnits']['value'] == '152', d
    bdf = min(parents)
    siblings = {name for name, d in ds.items() if parent(d) == bdf}
    assert len(siblings) == 2, siblings
    # The demo stays Running for inspection, so the namespace is kept.
    with e2e.scratch_namespace(k, NS, keep=True):
        for name in ['first', 'second', 'waiting']:
            claim(name, [request('gpu', bdf)])
            pod(name, name)
            if name != 'waiting':
                k.ready(name)
        first_uid = k.get('resourceclaim', 'first')['metadata']['uid']
        second_uid = k.get('resourceclaim', 'second')['metadata']['uid']
        assert cdi_exists(first_uid) and cdi_exists(second_uid), 'missing CDI spec for an allocated claim'
        first, _ = evidence('first', 'first')
        second, _ = evidence('second', 'second')
        assert {first, second} == siblings, (first, second, siblings)
        e2e.wait_for(lambda: k.scheduling_rejected('waiting'), 'scheduler rejects the third consumer')
        e2e.stays(lambda: not k.allocation('waiting') and k.get('pod', 'waiting')['status']['phase'] == 'Pending',
                  'third consumer stays Pending and unallocated')
        e2e.ok('third consumer stays Pending when the selected physical GPU has no free partitions')
        # An explicitly created ResourceClaim owns its allocation until it is deleted.
        delete('first')
        e2e.wait_for(lambda: not cdi_exists(first_uid), 'AMD NodeUnprepare removes the released claim CDI file')
        e2e.ok('AMD NodeUnprepare removes the released claim CDI file')
        assert cdi_exists(second_uid), 'sibling CDI spec removed with the released claim'
        k.ready('waiting')
        reused, _ = evidence('waiting', 'waiting')
        assert reused == first, (reused, first)
        assert evidence('second', 'second')[0] == second
        e2e.ok('released partition is reused while the sibling consumer remains Ready')
        old_pods = driver_pods()
        assert old_pods, 'no DRA driver pods found in ' + MOCK_NS
        k('rollout', 'restart', 'daemonset', '-n', MOCK_NS, '-l', 'app.kubernetes.io/name=dra')
        k('rollout', 'status', 'daemonset', '-n', MOCK_NS, '-l', 'app.kubernetes.io/name=dra', '--timeout=180s')
        new_pods = driver_pods()
        assert new_pods and old_pods.isdisjoint(new_pods), 'driver pod did not restart'
        assert evidence('second', 'second')[0] == second
        assert evidence('waiting', 'waiting')[0] == reused
        e2e.ok('driver restart preserves both live allocations')
        for name in ['second', 'waiting']:
            delete(name)
        claim('two-slices', [request('left'), request('right')], same_parent=True)
        pod('two-containers', 'two-slices', [('left', 'left'), ('right', 'right')])
        k.ready('two-containers')
        left, lp = evidence('two-containers', 'two-slices', 'left', 'left')
        right, rp = evidence('two-containers', 'two-slices', 'right', 'right')
        assert left != right and lp == rp, (left, lp, right, rp)
        e2e.ok('two containers in one pod receive distinct partitions of the same physical GPU')
        print(f'Demo remains Running in namespace {NS}; delete that namespace to release its claims.', flush=True)


if __name__ == '__main__':
    main()
