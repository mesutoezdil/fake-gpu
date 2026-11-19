"""Helpers shared by the end-to-end checks under test/.

The checks drive a cluster through kubectl or a host port over HTTP, then poll
until the state they expect shows up. Keeping those primitives in one place
gives every check the same timeouts, the same error messages and the same
tolerance for errors that only mean "not yet".

A check imports this module after putting test/lib on sys.path:

    sys.path.insert(0, str(Path(__file__).resolve().parents[2] / 'lib'))
    import e2e
"""
import base64
import contextlib
import json
import os
import subprocess
import time
import urllib.parse
import urllib.request

# Upper bound for one kubectl call. Longer than any --timeout a check hands to
# kubectl wait or rollout status, so those report their own error first.
COMMAND_TIMEOUT = float(os.environ.get('E2E_COMMAND_TIMEOUT', '300'))
HTTP_TIMEOUT = 15


class CommandError(RuntimeError):
    """A command exited non-zero or ran past its timeout."""

    def __init__(self, args, detail):
        self.args_ = list(args)
        super().__init__(' '.join(self.args_) + ': ' + (detail or 'failed without output'))


# Errors a poll treats as "not yet". Programming errors such as TypeError or
# NameError are not in here, so they fail at once instead of at the timeout.
TRANSIENT = (AssertionError, LookupError, OSError, ValueError, CommandError)


def run(args, *, input=None, check=True, timeout=COMMAND_TIMEOUT):
    """Run a command, capture text output, raise CommandError on failure."""
    try:
        result = subprocess.run(list(args), input=input, capture_output=True, text=True,
                                timeout=timeout, check=False)
    except subprocess.TimeoutExpired as error:
        raise CommandError(args, f'timed out after {timeout:g}s') from error
    if check and result.returncode:
        detail = (result.stderr.strip() or result.stdout.strip())
        raise CommandError(args, f'exit {result.returncode}: {detail}')
    return result


def wait_for(check, label, timeout=120, interval=1):
    """Call check() until it returns a truthy value, and return that value.

    Transient errors count as "not yet"; the last one is part of the timeout
    message, so a failure says why rather than only that it took too long.
    """
    deadline = time.monotonic() + timeout
    last = None
    while True:
        try:
            value = check()
            if value:
                return value
        except TRANSIENT as error:
            last = error
        if time.monotonic() >= deadline:
            message = f'timed out after {timeout:g}s: {label}'
            if last is not None:
                message += f' (last error: {type(last).__name__}: {last})'
            raise AssertionError(message)
        time.sleep(interval)


def stays(check, label, seconds=5, interval=1):
    """Require check() to stay truthy for the given number of seconds."""
    deadline = time.monotonic() + seconds
    while True:
        assert check(), 'no longer true: ' + label
        if time.monotonic() >= deadline:
            return
        time.sleep(interval)


passed = 0


def ok(label):
    """Report one passed check."""
    global passed
    passed += 1
    print('PASS: ' + label, flush=True)


def summary():
    print(f'Results: {passed} passed, 0 failed', flush=True)


def env_flag(name, default='0'):
    return os.environ.get(name, default) == '1'


_UNSET = object()


class Kubectl:
    """kubectl bound to an optional context and default namespace.

    namespace=None on a call means no -n flag: cluster-scoped kinds, or the
    context's current namespace.
    """

    def __init__(self, namespace=None, context=None, binary='kubectl'):
        self.namespace = namespace
        self.prefix = [binary] + (['--context', context] if context else [])

    def __call__(self, *args, payload=None, check=True, timeout=COMMAND_TIMEOUT):
        data = None if payload is None else json.dumps(payload)
        return run([*self.prefix, *args], input=data, check=check, timeout=timeout)

    def out(self, *args, **kwargs):
        return self(*args, **kwargs).stdout

    def _ns(self, namespace):
        namespace = self.namespace if namespace is _UNSET else namespace
        return ['-n', namespace] if namespace else []

    def get(self, kind, name=None, namespace=_UNSET, all_namespaces=False):
        scope = ['-A'] if all_namespaces else self._ns(namespace)
        return json.loads(self.out('get', kind, *([name] if name else []), *scope, '-o', 'json'))

    def items(self, kind, namespace=_UNSET, all_namespaces=False, selector=None):
        args = ['-l', selector] if selector else []
        scope = ['-A'] if all_namespaces else self._ns(namespace)
        return json.loads(self.out('get', kind, *args, *scope, '-o', 'json'))['items']

    def apply(self, obj, namespace=_UNSET):
        self('apply', *self._ns(namespace), '-f', '-', payload=obj)

    def exists(self, kind, name, namespace=_UNSET):
        return self('get', kind, name, *self._ns(namespace), check=False).returncode == 0

    def delete(self, kind, name, namespace=_UNSET, timeout='120s'):
        self('delete', *self._ns(namespace), kind, name, '--wait=true', '--timeout=' + timeout)

    def ready(self, pod, namespace=_UNSET, timeout='180s'):
        self('wait', *self._ns(namespace), 'pod/' + pod, '--for=condition=Ready', '--timeout=' + timeout)

    def exec(self, pod, *command, namespace=_UNSET, container=None, check=True):
        args = ['-c', container] if container else []
        return self('exec', *self._ns(namespace), pod, *args, '--', *command, check=check)

    def allocation(self, claim, namespace=_UNSET):
        """Device results of a ResourceClaim; empty while unallocated."""
        status = self.get('resourceclaim', claim, namespace).get('status', {})
        return status.get('allocation', {}).get('devices', {}).get('results', [])

    def scheduling_rejected(self, pod, namespace=_UNSET):
        """The scheduler has reported FailedScheduling for this pod object."""
        uid = self.get('pod', pod, namespace)['metadata']['uid']
        return any(e.get('reason') == 'FailedScheduling' and e.get('involvedObject', {}).get('uid') == uid
                   for e in self.items('events', namespace))

    def rollout_restart(self, target, namespace=_UNSET, timeout='180s'):
        self('rollout', 'restart', *self._ns(namespace), target)
        self('rollout', 'status', *self._ns(namespace), target, '--timeout=' + timeout)

    def diagnostics(self, namespace=_UNSET, kinds=('pods', 'resourceclaims', 'events')):
        """Print the state of a namespace; never raises."""
        for kind in kinds:
            output = 'yaml' if kind == 'resourceclaims' else 'wide'
            result = self('get', kind, *self._ns(namespace), '-o', output, check=False)
            print(f'--- {kind}\n' + (result.stdout or result.stderr), flush=True)


def file_exists(kubectl, pod, path, namespace=_UNSET, container=None):
    """Whether path exists inside a container.

    Unlike the exit status of a bare `test -e`, a failing exec (pod restarting,
    container gone) raises CommandError rather than reading as "absent", so a
    wait for a file to disappear cannot pass by accident.
    """
    script = 'if test -e "$1"; then echo present; else echo absent; fi'
    out = kubectl.exec(pod, 'sh', '-c', script, 'sh', path, namespace=namespace, container=container).stdout.split()
    if out not in (['present'], ['absent']):
        raise CommandError(['exec', pod, 'test', '-e', path], 'unexpected output: ' + repr(out))
    return out == ['present']


@contextlib.contextmanager
def scratch_namespace(kubectl, name, keep=False, wait=False):
    """Create an unused namespace, dump its state on failure, delete it after.

    Refuses a namespace that already exists, so a check never adopts or deletes
    workloads it did not create. wait=True blocks until the deletion finishes,
    for checks that run again right away under the same name.
    """
    if kubectl.exists('namespace', name, namespace=None):
        raise SystemExit(f'namespace {name} already exists; choose an unused one')
    kubectl('create', 'namespace', name)
    succeeded = False
    try:
        yield name
        succeeded = True
    except BaseException:
        kubectl.diagnostics(namespace=name)
        raise
    finally:
        if not keep:
            flags = ['--wait=true', '--timeout=120s'] if wait else ['--wait=false']
            # A failed cleanup must not hide the error that ended the check.
            kubectl('delete', 'namespace', name, *flags, check=wait and succeeded)


def device_probe(count, cards=False):
    """Shell that exits non-zero unless /dev/kfd and exactly count render (and,
    with cards, count card) character devices are present.

    Counting paths and testing each one for a character device catches both a
    missing device and a plain file left where a device node should be.
    """
    lines = ['test -c /dev/kfd || exit 1']
    for pattern in ['/dev/dri/renderD*'] + (['/dev/dri/card*'] if cards else []):
        lines.append(f'set -- {pattern}; [ "$#" -eq {int(count)} ] || exit 1')
        lines.append('for dev in "$@"; do test -c "$dev" || exit 1; done')
    return '\n'.join(lines)


def http(url, data=None, method=None, headers=None, timeout=HTTP_TIMEOUT):
    """Send a request and return (status, body text)."""
    body = None if data is None else json.dumps(data).encode()
    request = urllib.request.Request(url, body, {'Content-Type': 'application/json', **(headers or {})},
                                     method=method)
    with urllib.request.urlopen(request, timeout=timeout) as response:
        return response.status, response.read().decode()


def http_json(url, data=None, method=None, headers=None, timeout=HTTP_TIMEOUT):
    return json.loads(http(url, data, method, headers, timeout)[1])


def basic_auth(user, password):
    return 'Basic ' + base64.b64encode(f'{user}:{password}'.encode()).decode()


class Prometheus:
    def __init__(self, base):
        self.base = base.rstrip('/')

    def query(self, expr):
        response = http_json(self.base + '/api/v1/query?' + urllib.parse.urlencode({'query': expr}))
        assert response['status'] == 'success', response
        return response['data']['result']

    def values(self, expr):
        return [float(r['value'][1]) for r in self.query(expr)]


class Grafana:
    def __init__(self, base, password, user='admin'):
        self.base = base.rstrip('/')
        self.headers = {'Authorization': basic_auth(user, password)}

    def get(self, path, payload=None):
        return http_json(self.base + path, payload, headers=self.headers)

    def query(self, expr, datasource='prometheus'):
        """Frames of an instant query through a Grafana datasource."""
        result = self.get('/api/ds/query', {'from': 'now-5m', 'to': 'now', 'queries': [{
            'refId': 'A', 'expr': expr, 'instant': True, 'format': 'table',
            'datasource': {'type': 'prometheus', 'uid': datasource},
            'intervalMs': 1000, 'maxDataPoints': 1000}]})['results']['A']
        assert not result.get('error'), result
        return result.get('frames', [])

    def numbers(self, expr, datasource='prometheus'):
        """Every numeric value of an instant query."""
        return [value for frame in self.query(expr, datasource)
                for field, values in zip(frame['schema']['fields'], frame['data']['values'], strict=True)
                if field['type'] == 'number' for value in values]

    def has_data(self, expr, datasource='prometheus'):
        return any(any(values for values in frame.get('data', {}).get('values', []))
                   for frame in self.query(expr, datasource))


class MockAPI:
    """The node agent's HTTP API, reached through kubectl exec into the agent.

    Reaching it from inside the pod needs no port-forward or host mapping.
    """

    def __init__(self, kubectl, namespace='amd-mock', target='ds/fake-gpu', container='node-agent'):
        self.kubectl, self.namespace, self.target, self.container = kubectl, namespace, target, container

    def sh(self, *command):
        return self.kubectl.exec(self.target, *command, namespace=self.namespace, container=self.container).stdout

    def __call__(self, path, post=False):
        args = ['wget', '-qO-'] + (['--post-data='] if post else [])
        return json.loads(self.sh(*args, 'http://localhost:8080' + path))
