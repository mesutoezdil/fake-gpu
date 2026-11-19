#!/usr/bin/env python3
"""Single-node DRA lifecycle tests against an already installed driver."""
import os
import sys
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parents[2] / 'lib'))
import e2e

NS = os.environ.get('LIFECYCLE_NS', 'amd-dra-lifecycle')
DRIVER_NS = os.environ.get('DRA_NS', 'amd-mock')
DRIVER = 'gpu.amd.com'
k = e2e.Kubectl(namespace=NS)


def claim(name, count=1):
    k.apply({'apiVersion': 'resource.k8s.io/v1', 'kind': 'ResourceClaim',
             'metadata': {'name': name}, 'spec': {'devices': {'requests': [
                 {'name': 'gpu', 'exactly': {'deviceClassName': DRIVER, 'count': count}}
             ]}}})


def pod(name, claim_name):
    k.apply({'apiVersion': 'v1', 'kind': 'Pod', 'metadata': {'name': name}, 'spec': {
        'restartPolicy': 'Never', 'terminationGracePeriodSeconds': 2,
        'containers': [{'name': 'demo', 'image': 'docker.io/busybox:1.36',
                        'command': ['sh', '-c', "trap 'exit 0' TERM INT; sleep 3600 & wait"],
                        'resources': {'claims': [{'name': 'gpu'}]}}],
        'resourceClaims': [{'name': 'gpu', 'resourceClaimName': claim_name}]
    }})


def devices(name, count):
    k.exec(name, 'sh', '-c', e2e.device_probe(count, cards=True))


def reserved(name):
    return k.get('resourceclaim', name).get('status', {}).get('reservedFor', [])


def main():
    plugins = [d for d in k.items('daemonsets', namespace=DRIVER_NS)
               if d['metadata'].get('labels', {}).get('app.kubernetes.io/component') == 'kubeletplugin']
    assert len(plugins) == 1, 'Expected one DRA DaemonSet in ' + DRIVER_NS
    ds = 'ds/' + plugins[0]['metadata']['name']
    slices = [s for s in k.items('resourceslices', namespace=None) if s['spec']['driver'] == DRIVER]
    nodes = {s['spec']['nodeName'] for s in slices}
    assert len(nodes) == 1, 'Exhaustion test requires one mock node, found ' + repr(sorted(nodes))
    count = sum(len(s['spec']['devices']) for s in slices)
    assert count >= 2, f'Need at least two GPUs, found {count}'

    def cdi_exists(uid):
        # An exec failure raises instead of reading as "file absent".
        path = '/var/run/cdi/k8s.gpu.amd.com-gpu_' + uid + '.yaml'
        return e2e.file_exists(k, ds, path, namespace=DRIVER_NS, container='plugin')

    with e2e.scratch_namespace(k, NS, keep=e2e.env_flag('KEEP')):
        claim('multi', 2)
        pod('multi', 'multi')
        k.ready('multi')
        results = k.allocation('multi')
        assert len(results) == 2 and len({r['device'] for r in results}) == 2, results
        devices('multi', 2)
        e2e.ok('two-GPU claim gets two distinct GPUs and exactly two card/render pairs')
        multi_uid = k.get('resourceclaim', 'multi')['metadata']['uid']
        assert cdi_exists(multi_uid), 'no CDI spec for the allocated claim'
        k.delete('pod', 'multi')
        e2e.wait_for(lambda: not reserved('multi'), 'reservation released')
        e2e.wait_for(lambda: not k.allocation('multi'), 'explicit claim deallocated')
        e2e.wait_for(lambda: not cdi_exists(multi_uid), 'CDI spec removed after last consumer')
        e2e.ok('explicit claim object remains but allocation and CDI are released after its last consumer')
        k.delete('resourceclaim', 'multi')
        e2e.wait_for(lambda: not cdi_exists(multi_uid), 'CDI spec removed')
        e2e.ok('unprepare removes the deleted claim CDI spec')

        claim('all', count)
        pod('all', 'all')
        k.ready('all')
        all_results = k.allocation('all')
        assert len(all_results) == count and len({r['device'] for r in all_results}) == count, all_results
        devices('all', count)
        claim('overflow')
        pod('overflow', 'overflow')
        e2e.wait_for(lambda: k.scheduling_rejected('overflow'), 'overflow scheduling rejection')
        e2e.stays(lambda: not k.allocation('overflow') and k.get('pod', 'overflow')['status']['phase'] == 'Pending',
                  'overflow claim stays unallocated and Pending')
        e2e.ok('full-pool allocation is exclusive; an additional claim stays unallocated')
        k.delete('pod', 'all')
        k.delete('resourceclaim', 'all')
        k.ready('overflow')
        devices('overflow', 1)
        e2e.ok('pending claim starts after the exhausted pool is released')

        retained = k.allocation('overflow')
        uid = k.get('resourceclaim', 'overflow')['metadata']['uid']
        k.rollout_restart(ds, namespace=DRIVER_NS)
        devices('overflow', 1)
        assert k.allocation('overflow') == retained, 'allocation changed across driver restart'
        assert cdi_exists(uid), 'CDI spec lost across driver restart'
        e2e.ok('driver restart preserves allocation and its CDI spec')
        # A second consumer of the same explicit claim shares that allocation.
        pod('shared', 'overflow')
        k.ready('shared')
        devices('shared', 1)
        assert len(reserved('overflow')) == 2, reserved('overflow')
        assert k.allocation('overflow') == retained, 'second consumer changed the allocation'
        e2e.ok('two pods can consume one explicit claim without a second GPU allocation')
        k.delete('pod', 'shared')
        k.delete('pod', 'overflow')
        e2e.wait_for(lambda: not reserved('overflow'), 'explicit consumers released')
        e2e.wait_for(lambda: not k.allocation('overflow'), 'last consumer deallocation')
        pod('reuse', 'overflow')
        k.ready('reuse')
        assert len(k.allocation('overflow')) == 1
        devices('reuse', 1)
        e2e.ok('existing explicit claim is allocated again for a new consumer')
        k.delete('pod', 'reuse')
        k.delete('resourceclaim', 'overflow')
        e2e.wait_for(lambda: not cdi_exists(uid), 'final CDI cleanup')
        e2e.ok('final explicit-claim deletion removes its CDI spec')
        e2e.summary()


if __name__ == '__main__':
    main()
