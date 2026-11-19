#!/usr/bin/env python3
"""Kill the real provider process, then require an actual container restart."""
import os
import sys
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parents[1] / 'lib'))
import e2e

NS = 'amd-mock'
MODE = os.environ.get('PROVIDER_RESTART_MODE', 'unclean')
k = e2e.Kubectl(namespace=NS)
# Terminate node-agent itself, not PID 1; no simulated successful restart.
KILL = '''
for p in /proc/[0-9]*/comm; do
 read name < "$p" || continue
 if [ "$name" = node-agent ]; then
  pid=${p#/proc/}; pid=${pid%/comm}
  echo "Killing actual node-agent PID $pid"
  kill -"$1" "$pid"
  exit 0
 fi
done
echo "no node-agent process found" >&2
exit 1
'''


def agent_pod():
    pods = k.items('pods', selector='app.kubernetes.io/name=fake-gpu')
    assert len(pods) == 1, [p['metadata']['name'] for p in pods]
    return pods[0]


def inodes():
    return k.out('exec', '-n', NS, 'ds/fake-gpu', '--', 'stat', '-c', '%i',
                 '/var/lib/fake-gpu/sys', '/var/lib/fake-gpu/smi').splitlines()


def main():
    assert MODE in ('unclean', 'graceful'), 'PROVIDER_RESTART_MODE must be unclean or graceful'
    signal = 'KILL' if MODE == 'unclean' else 'TERM'
    before = agent_pod()
    previous_inodes = inodes()
    restarts = before['status']['containerStatuses'][0]['restartCount']
    # The exec session dies with the process it kills, so its exit status is
    # not evidence either way; the restart count below is.
    result = k('exec', '-n', NS, before['metadata']['name'], '--', 'sh', '-ec', KILL, 'sh', signal, check=False)
    print(result.stdout + result.stderr, flush=True)

    def restarted():
        after = agent_pod()
        statuses = after['status'].get('containerStatuses', [])
        if statuses and statuses[0]['restartCount'] > restarts and statuses[0]['ready']:
            return after, statuses[0]['restartCount']
        return None

    after, count = e2e.wait_for(restarted, 'provider restarts and becomes ready', timeout=180, interval=2)
    assert after['metadata']['uid'] == before['metadata']['uid'], 'pod was replaced, not restarted'
    assert inodes() == previous_inodes, 'mounted sys/SMI directory inodes changed'
    e2e.ok(f'actual {MODE} node-agent restart: count {restarts} -> {count}; '
           'mounted directories preserved, CUSE/API ready')


if __name__ == '__main__':
    main()
