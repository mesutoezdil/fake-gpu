#!/usr/bin/env python3
"""Physical device-plugin scheduling/fault contracts on a dedicated one-node cluster."""
import os
import sys
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parents[2] / 'lib'))
import e2e

NS = os.environ.get('MOCK_NS', 'amd-mock')
TEST_NS = os.environ.get('PROFILE_TEST_NS', 'amd-profile-test')
PROFILE = os.environ.get('PROFILE', 'mi300x')
COUNTS = {'mi210': 8, 'mi250x': 16, 'mi300a': 4, 'mi300x': 8,
          'mi325x': 8, 'mi350x': 8, 'mi355x': 8}
PLUGIN = 'ds/fake-gpu-device-plugin'
SYS = '/var/lib/fake-gpu/sys'
k = e2e.Kubectl(namespace=TEST_NS)
api = e2e.MockAPI(k, namespace=NS)


def restart_plugin():
    k.rollout_restart(PLUGIN, namespace='kube-system', timeout='120s')


def pod(name, count):
    k.apply({'apiVersion': 'v1', 'kind': 'Pod', 'metadata': {'name': name}, 'spec': {
        'restartPolicy': 'Never', 'terminationGracePeriodSeconds': 1,
        'containers': [{'name': 'test', 'image': 'docker.io/busybox:1.36',
                        'command': ['sh', '-c', "trap 'exit 0' TERM INT; sleep 3600 & wait"],
                        'resources': {'limits': {'amd.com/gpu': count}}}]}})


def recover(gpu=0):
    api(f'/api/actions/recover?gpu={gpu}', post=True)


def main():
    assert PROFILE in COUNTS, f'unknown PROFILE {PROFILE}; one of {sorted(COUNTS)}'
    nodes = k.items('nodes', namespace=None)
    assert len(nodes) == 1, 'Use a dedicated single-node cluster'
    node = nodes[0]['metadata']['name']
    assert nodes[0]['status']['nodeInfo']['kubeletVersion'].startswith('v1.37.'), 'Requires Kubernetes 1.37'
    assert not [x for x in k.items('daemonsets', namespace=NS)
                if x['metadata'].get('labels', {}).get('app.kubernetes.io/component') == 'kubeletplugin'], 'Disable DRA'
    count = COUNTS[PROFILE]

    def capacity():
        return int(k.get('node', node, namespace=None)['status']['allocatable'].get('amd.com/gpu', 0))

    with e2e.scratch_namespace(k, TEST_NS, keep=e2e.env_flag('KEEP')):
        try:
            catalog = {p['slug']: p for p in api('/api/profiles')}
            fleet = api('/api/gpus')['gpus']
            assert len(fleet) == count == catalog[PROFILE]['gpu_count'], (len(fleet), count)
            assert all(g['name'] == catalog[PROFILE]['name'] and g['profile_slug'] == PROFILE and
                       g['memory_total_mb'] == catalog[PROFILE]['memory_gb'] * 1024 for g in fleet), fleet
            assert len({g['uuid'] for g in fleet}) == count
            e2e.wait_for(lambda: capacity() == count, 'profile resource count')
            e2e.ok(PROFILE + ': API identity/memory and allocatable count match the profile')
            pod('one', 1)
            k.ready('one')
            k.exec('one', 'sh', '-c', e2e.device_probe(1))
            e2e.ok('one-GPU consumer receives exactly one render character device')
            k.delete('pod', 'one')
            pod('full', count)
            k.ready('full')
            pod('overflow', 1)
            e2e.wait_for(lambda: k.scheduling_rejected('overflow'), 'exhaustion scheduling rejection')
            e2e.stays(lambda: k.get('pod', 'overflow')['status']['phase'] == 'Pending', 'overflow stays Pending')
            e2e.ok('full-pool consumer excludes an additional GPU request')
            k.delete('pod', 'full')
            k.ready('overflow')
            k.delete('pod', 'overflow')
            e2e.ok('pending consumer starts after capacity is released')
            bdf = fleet[0]['pci_bdf']
            api('/api/actions/crash?gpu=0', post=True)
            assert api('/api/gpus')['gpus'][0]['status'] == 'crashed'
            fatal = api.sh('cat', SYS + '/class/kfd/kfd/topology/nodes/1/ras/fatal_error').strip()
            assert fatal == '1', 'fatal_error is ' + repr(fatal)
            api.sh('sh', '-c', 'test ! -e ' + SYS + '/module/amdgpu/drivers/pci:amdgpu/' + bdf)
            restart_plugin()
            e2e.wait_for(lambda: capacity() == count - 1, 'crashed GPU removed from scheduling')
            e2e.ok('crash reaches sysfs and device-plugin capacity after rediscovery')
            recover()
            restart_plugin()
            e2e.wait_for(lambda: capacity() == count, 'recovered GPU rediscovered')
            e2e.ok('recovery restores physical GPU capacity')
            # Every fault, checked in the API and in the rendered sysfs files, by the
            # scenario the image ships. A failed expectation makes the command fail.
            print(api.sh('fakegpuctl', 'run', '/usr/share/fake-gpu/scenarios/faults.yaml'), flush=True)
            e2e.ok('overheat, ECC, page retirement, xGMI and throttling reach sysfs and recover')
            api('/api/actions/retire-page?gpu=0&count=2', post=True)
            assert 'RETIRED: 2' in api.sh('amd-smi', 'bad-pages', '--gpu', '0')
            assert api('/api/actions/throttle?gpu=0', post=True)['throttled']
            e2e.wait_for(lambda: 'THROTTLE_STATUS: THROTTLED' in api.sh('amd-smi', 'metric', '--gpu', '0'),
                         'throttle visible in amd-smi', timeout=30)
            recover()
            e2e.ok('page retirement and throttling reach amd-smi')
            e2e.summary()
        finally:
            # Best effort: a failed recovery must not hide the original error.
            try:
                recover()
            except e2e.TRANSIENT as error:
                print('WARNING: could not recover GPU 0: ' + str(error), flush=True)


if __name__ == '__main__':
    main()
