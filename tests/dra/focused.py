#!/usr/bin/env python3
"""Installed-driver DRA capacity, container-sharing and PCIe-root checks.

Uses normal claims and Pods in a new namespace. Does not modify discovery,
allocation status, driver settings, or existing workloads. Requires two free
GPUs on one node with the same advertised PCIe root. KUBECONFIG selects cluster.
"""
import json
import os
import sys
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parents[1] / 'lib'))
import e2e

NS = os.environ.get('FOCUSED_NS', 'amd-dra-focused')
ROOT = 'resource.kubernetes.io/pcieRoot'
DRIVER = 'gpu.amd.com'
k = e2e.Kubectl(namespace=NS)


def request(name='gpu', memory=None, expression=None):
    exactly = {'deviceClassName': DRIVER, 'count': 1}
    if memory:
        exactly['capacity'] = {'requests': {'memory': memory}}
    if expression:
        exactly['selectors'] = [{'cel': {'expression': expression}}]
    return {'name': name, 'exactly': exactly}


def claim(name, requests, constraints=None):
    devices = {'requests': requests}
    if constraints:
        devices['constraints'] = constraints
    k.apply({'apiVersion': 'resource.k8s.io/v1', 'kind': 'ResourceClaim',
             'metadata': {'name': name}, 'spec': {'devices': devices}})


# Compare path plus major/minor identity, not just a successful ls command.
PROBE = '''set -eu
test -c /dev/kfd
set -- /dev/dri/card*; [ "$#" -eq 1 ]; test -c "$1"
set -- /dev/dri/renderD*; [ "$#" -eq 1 ]; test -c "$1"
stat -c '%n %t:%T' /dev/kfd /dev/dri/card* /dev/dri/renderD*'''


def pod(name, node, sharing=False):
    container = {'name': 'main', 'image': 'docker.io/busybox:1.36',
                 'command': ['sh', '-c', PROBE + '\nsleep 3600'],
                 'resources': {'claims': [{'name': 'gpu'}]}}
    spec = {'restartPolicy': 'Never', 'terminationGracePeriodSeconds': 1,
            'nodeSelector': {'kubernetes.io/hostname': node},
            'resourceClaims': [{'name': 'gpu', 'resourceClaimName': name}],
            'containers': [container]}
    if sharing:
        init = dict(container, name='gpu-check', command=['sh', '-c', PROBE])
        spec['initContainers'] = [init]
    k.apply({'apiVersion': 'v1', 'kind': 'Pod', 'metadata': {'name': name}, 'spec': spec})


def pending(name):
    """The scheduler rejects the pod, and it then stays Pending and unallocated."""
    e2e.wait_for(lambda: k.scheduling_rejected(name), 'scheduling rejection for ' + name)
    # Keep observing after rejection to catch an unexpected allocation.
    e2e.stays(lambda: not k.allocation(name) and k.get('pod', name)['status']['phase'] == 'Pending',
              name + ' stays Pending and unallocated')


def release(name):
    for kind in ('pod', 'resourceclaim'):
        k.delete(kind, name)


def free_siblings(healthy):
    """Inventory plus the first node and PCIe root with two unallocated devices."""
    slices = [s for s in k.items('resourceslices', namespace=None) if s['spec']['driver'] == DRIVER]
    inventory = {(s['spec']['pool']['name'], d['name']): (s['spec']['nodeName'], d)
                 for s in slices for d in s['spec']['devices']}
    used = {(r['pool'], r['device']) for c in k.items('resourceclaims', all_namespaces=True)
            for r in c.get('status', {}).get('allocation', {}).get('devices', {}).get('results', [])
            if r['driver'] == DRIVER}
    groups = {}
    for key, (node, d) in inventory.items():
        if key not in used and node in healthy:
            groups.setdefault((node, d['attributes'][ROOT]['string']), []).append((key, d))
    candidates = [(group, devices) for group, devices in sorted(groups.items()) if len(devices) >= 2]
    return (inventory, candidates[0]) if candidates else None


def main():
    # Read-only preflight before creating any resources.
    healthy = {p['spec']['nodeName'] for p in k.items('pods', namespace=os.environ.get('DRA_NS', 'amd-mock'))
               if any(c['name'] == 'node-agent' for c in p['spec']['containers'])
               and any(c['type'] == 'Ready' and c['status'] == 'True'
                       for c in p.get('status', {}).get('conditions', []))}
    assert healthy, 'No Ready mock node agents; repair the installed stack before running these tests'
    # Driver readiness can precede its first ResourceSlice publication.
    inventory, ((node, root), devices) = e2e.wait_for(
        lambda: free_siblings(healthy), 'two free AMD devices on one node with the same PCIe root')
    # Device names are scoped to pools; never compare names across nodes alone.
    _, device = devices[0]
    device_selector = ('device.attributes["resource.kubernetes.io"].pciBusID == ' +
                       json.dumps(device['attributes']['resource.kubernetes.io/pciBusID']['string']))
    with e2e.scratch_namespace(k, NS, keep=e2e.env_flag('KEEP')):
        print(f'PREFLIGHT node={node} root={root} free_siblings={len(devices)}', flush=True)
        # Exact advertised capacity avoids lossy parsing of Kubernetes quantities.
        memory = device['capacity']['memory']['value']
        claim('memory-fit', [request(memory=memory, expression=device_selector)])
        pod('memory-fit', node)
        k.ready('memory-fit')
        allocated = k.allocation('memory-fit')
        assert len(allocated) == 1, allocated
        key = (allocated[0]['pool'], allocated[0]['device'])
        assert inventory[key][0] == node
        assert inventory[key][1]['capacity']['memory']['value'] == memory
        k.exec('memory-fit', 'sh', '-c', PROBE)
        e2e.ok(f'memory request {memory} allocates a matching device with CDI injection')
        release('memory-fit')

        # 1 EiB exceeds all supported mock profiles (maximum 192 GiB).
        # Both API acceptance and a scheduler rejection are required for PASS.
        claim('memory-too-large', [request(memory='1Ei', expression=device_selector)])
        pod('memory-too-large', node)
        pending('memory-too-large')
        e2e.ok('oversized memory request is accepted but remains Pending and unallocated')
        release('memory-too-large')

        claim('shared-container', [request(expression=device_selector)])
        pod('shared-container', node, sharing=True)
        k.ready('shared-container')
        assert len(k.allocation('shared-container')) == 1
        p = k.get('pod', 'shared-container')
        assert p['status']['initContainerStatuses'][0]['state']['terminated']['exitCode'] == 0
        init = k.out('logs', '-n', NS, 'shared-container', '-c', 'gpu-check').strip()
        main_log = k.out('logs', '-n', NS, 'shared-container', '-c', 'main').strip()
        assert init and init == main_log, (init, main_log)
        a = k.allocation('shared-container')[0]
        assert 'renderD' + a['device'].split('-')[-1] in init, (a, init)
        print('EVIDENCE init/main device identity:\n' + init, flush=True)
        e2e.ok('init and main containers use one allocation and identical card/render/KFD identities')
        release('shared-container')

        root_selector = ('device.attributes["resource.kubernetes.io"].pcieRoot == ' + json.dumps(root))
        requests = [request('left', expression=root_selector), request('right', expression=root_selector)]
        claim('root-match', requests, [{'requests': ['left', 'right'], 'matchAttribute': ROOT}])
        pod('root-match', node)
        k.ready('root-match')
        allocations = k.allocation('root-match')
        keys = {(a['pool'], a['device']) for a in allocations}
        assert len(allocations) == len(keys) == 2, allocations
        assert all(inventory[key][0] == node and inventory[key][1]['attributes'][ROOT]['string'] == root
                   for key in keys)
        print('EVIDENCE same-root allocation: ' + json.dumps(allocations), flush=True)
        e2e.ok('two distinct devices satisfy the same-PCIe-root matchAttribute constraint')
        release('root-match')

        # Each selector is satisfiable; equal roots cannot satisfy distinctAttribute.
        # This proves constraint enforcement rather than a nonexistent-root selector.
        claim('root-impossible', requests, [{'requests': ['left', 'right'], 'distinctAttribute': ROOT}])
        pod('root-impossible', node)
        pending('root-impossible')
        e2e.ok('individually matching requests with contradictory PCIe-root constraint remain unallocated')
        release('root-impossible')
        e2e.summary()


if __name__ == '__main__':
    main()
