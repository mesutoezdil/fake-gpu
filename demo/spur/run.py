#!/usr/bin/env python3
"""Spur, unchanged, schedules GPU jobs onto the mock: a job that takes the
whole pool runs, a second one queues without a competing pod, and starts
once the first completes. KUBECONFIG defaults to the demo's own cluster.
"""
import json
import os
import subprocess
import time
from pathlib import Path

ROOT = Path(__file__).resolve().parents[2]
NS = 'amd-demo-spur'
os.environ['KUBECONFIG'] = os.environ.get('SPUR_KUBECONFIG', str(ROOT / 'tmp/spur/kubeconfig'))

# The job proves its devices, then waits until the presenter releases it.
JOB = '''set -eu
echo "BEGIN job={name} requested_gpus={count} host=$(hostname)"
test -c /dev/kfd
set -- /dev/dri/renderD*; [ "$#" -eq {count} ]
for d in "$@"; do test -c "$d"; done
stat -c '%n %t:%T' /dev/kfd /dev/dri/renderD*
echo DEVICE_INJECTION_PASS
echo "Waiting for presenter release; no GPU computation is performed."
while [ ! -e /tmp/presenter-release ]; do sleep 1; done
echo JOB_COMPLETED'''


def kubectl(*args, payload=None):
    p = subprocess.run(['kubectl', '--request-timeout=20s', *args], capture_output=True, text=True, timeout=240,
                       input=None if payload is None else json.dumps(payload), check=False)
    if p.returncode:
        raise RuntimeError(p.stderr or p.stdout)
    return p.stdout


def get(kind, name=None):
    return json.loads(kubectl('get', kind, *([name] if name else []), '-n', NS, '-o', 'json'))


def wait(check, what, timeout=180):
    deadline = time.monotonic() + timeout
    while not check():
        if time.monotonic() >= deadline:
            raise AssertionError('Timed out: ' + what)
        time.sleep(2)
    print('PASS: ' + what, flush=True)


def job(name):
    return get('spurjob', name).get('status', {})


def pods(name):
    """The pods Spur's operator made for a job."""
    job_id = job(name).get('spurJobId')
    if job_id is None:
        return []
    return [p for p in get('pods')['items'] if p['metadata'].get('labels', {}).get('spur.amd.com/job-id') == str(job_id)]


def running(name):
    return any(p.get('status', {}).get('phase') == 'Running'
               and all(c.get('ready') for c in p['status'].get('containerStatuses', [])) for p in pods(name))


def submit(name, count):
    kubectl('create', '-f', '-', payload={
        'apiVersion': 'spur.amd.com/v1alpha1', 'kind': 'SpurJob', 'metadata': {'name': name, 'namespace': NS},
        'spec': {'name': name, 'image': 'docker.io/busybox:1.36', 'gpus': {'count': count, 'gpuType': 'mi300x'},
                 'cpusPerTask': 1, 'memoryPerNode': '64Mi', 'timeLimit': '10m',
                 'command': ['/bin/sh', '-c'], 'args': [JOB.format(name=name, count=count)],
                 'nodeSelector': {'spur.amd.com/managed': 'true'}}})


def show(name, count):
    """Check the job's pod got exactly its GPUs, unprivileged, and print it."""
    found = pods(name)
    assert len(found) == 1, found
    pod = found[0]
    spec, container = pod['spec'], pod['spec']['containers'][0]
    assert container['resources']['limits']['amd.com/gpu'] == str(count), container['resources']
    assert not spec.get('hostPID') and not spec.get('hostNetwork')
    assert not container.get('securityContext', {}).get('privileged')
    assert not any(v.get('hostPath', {}).get('path', '').startswith('/dev') for v in spec.get('volumes', []))
    output = kubectl('logs', '-n', NS, pod['metadata']['name'])
    assert 'DEVICE_INJECTION_PASS' in output, output
    print(f'EVIDENCE SpurJob={name} id={job(name)["spurJobId"]} Pod={pod["metadata"]["name"]} '
          f'node={spec["nodeName"]} amd.com/gpu={count}', flush=True)
    print(output, flush=True)
    return pod['metadata']['name']


def release(name, pod):
    kubectl('exec', '-n', NS, pod, '--', 'touch', '/tmp/presenter-release')
    wait(lambda: job(name).get('state') == 'Completed', name + ' reaches Completed through Spur')
    print(kubectl('logs', '-n', NS, pod), flush=True)


def main():
    assert not get('spurjobs')['items'], 'Demo namespace already has SpurJobs; clean up jobs before rerunning'
    nodes = json.loads(kubectl('get', 'nodes', '-l', 'spur.amd.com/managed=true', '-o', 'json'))['items']
    assert len(nodes) == 1, 'This demo requires exactly one managed node'
    pool = int(nodes[0]['status']['allocatable']['amd.com/gpu'])
    assert pool >= 1
    print(kubectl('exec', '-n', NS, 'deploy/spurctld', '--', 'spur', '--version'), flush=True)

    submit('hold-pool', pool)
    wait(lambda: running('hold-pool'), 'Spur creates a running job consuming the whole mock GPU pool')
    first = show('hold-pool', pool)

    submit('queued-gpu', 1)
    wait(lambda: job('queued-gpu').get('state') == 'Pending', 'second SpurJob is Pending while the pool is occupied')
    for _ in range(5):
        time.sleep(2)
        assert job('queued-gpu').get('state') == 'Pending' and not pods('queued-gpu'), 'Spur dispatched the blocked job'
    print(kubectl('exec', '-n', NS, 'deploy/spurctld', '--', 'env', 'SPUR_CONTROLLER_ADDR=http://localhost:6817', 'spur', 'queue'), flush=True)
    print('PASS: Spur keeps the second job queued without creating a competing GPU Pod', flush=True)

    release('hold-pool', first)
    wait(lambda: running('queued-gpu'), 'queued job starts after the first job releases the GPU pool')
    release('queued-gpu', show('queued-gpu', 1))
    print(kubectl('get', 'spurjobs,pods', '-n', NS, '-o', 'wide'), flush=True)
    print('PASS: unchanged Spur submission, GPU allocation, queueing, completion and reuse', flush=True)


if __name__ == '__main__':
    main()
