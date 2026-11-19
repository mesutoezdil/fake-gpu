#!/usr/bin/env python3
"""Unit tests for the shared check helpers and the small deploy filters.

    python3 -m unittest discover -s test/lib -v
"""
import json
import os
import stat
import subprocess
import sys
import tempfile
import textwrap
import time
import unittest
from pathlib import Path
from unittest import mock

sys.path.insert(0, str(Path(__file__).resolve().parent))
import e2e

ROOT = Path(__file__).resolve().parents[2]
COMMON = ROOT / 'test/lib/common.sh'
SYS_REMAP = ROOT / 'deploy/dra/sys-remap.sh'


class RunTest(unittest.TestCase):
    def test_returns_output(self):
        self.assertEqual(e2e.run(['sh', '-c', 'echo hi']).stdout, 'hi\n')

    def test_failure_names_command_status_and_stderr(self):
        with self.assertRaises(e2e.CommandError) as caught:
            e2e.run(['sh', '-c', 'echo boom >&2; exit 3'])
        message = str(caught.exception)
        self.assertIn('exit 3', message)
        self.assertIn('boom', message)
        self.assertIn('sh -c', message)

    def test_unchecked_failure_returns_result(self):
        self.assertEqual(e2e.run(['sh', '-c', 'exit 4'], check=False).returncode, 4)

    def test_timeout_is_a_command_error(self):
        with self.assertRaises(e2e.CommandError) as caught:
            e2e.run(['sleep', '5'], timeout=0.2)
        self.assertIn('timed out', str(caught.exception))

    def test_command_error_is_transient(self):
        self.assertTrue(issubclass(e2e.CommandError, e2e.TRANSIENT))


class WaitForTest(unittest.TestCase):
    def test_returns_first_truthy_value(self):
        values = iter([None, [], {'ready': True}])
        self.assertEqual(e2e.wait_for(lambda: next(values), 'x', timeout=5, interval=0), {'ready': True})

    def test_retries_transient_errors(self):
        calls = []

        def check():
            calls.append(1)
            if len(calls) < 3:
                raise KeyError('status')
            return 'done'

        self.assertEqual(e2e.wait_for(check, 'x', timeout=5, interval=0), 'done')
        self.assertEqual(len(calls), 3)

    def test_timeout_reports_label_and_last_error(self):
        def check():
            raise OSError('connection refused')

        with self.assertRaises(AssertionError) as caught:
            e2e.wait_for(check, 'grafana up', timeout=0.05, interval=0.01)
        self.assertIn('grafana up', str(caught.exception))
        self.assertIn('connection refused', str(caught.exception))

    def test_programming_errors_are_not_retried(self):
        calls = []

        def check():
            calls.append(1)
            raise TypeError('bug')

        with self.assertRaises(TypeError):
            e2e.wait_for(check, 'x', timeout=5, interval=0)
        self.assertEqual(len(calls), 1)

    def test_checks_at_least_once_after_deadline(self):
        start = time.monotonic()
        self.assertEqual(e2e.wait_for(lambda: 'now', 'x', timeout=0), 'now')
        self.assertLess(time.monotonic() - start, 1)


class StaysTest(unittest.TestCase):
    def test_passes_while_true(self):
        e2e.stays(lambda: True, 'x', seconds=0.05, interval=0.01)

    def test_fails_when_it_turns_false(self):
        values = iter([True, True, False])
        with self.assertRaises(AssertionError) as caught:
            e2e.stays(lambda: next(values), 'claim unallocated', seconds=5, interval=0)
        self.assertIn('claim unallocated', str(caught.exception))


class KubectlTest(unittest.TestCase):
    """Drives Kubectl against a fake binary that echoes its arguments."""

    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.binary = Path(self.tmp.name) / 'kubectl'
        self.binary.write_text(textwrap.dedent('''\
            #!/usr/bin/env python3
            import json, sys
            data = sys.stdin.read()
            print(json.dumps({'argv': sys.argv[1:], 'stdin': data,
                              'items': [{'argv': sys.argv[1:]}]}))
            '''))
        self.binary.chmod(self.binary.stat().st_mode | stat.S_IXUSR)
        self.k = e2e.Kubectl(namespace='test-ns', context='kind-x', binary=str(self.binary))

    def tearDown(self):
        self.tmp.cleanup()

    def test_context_and_default_namespace(self):
        self.assertEqual(self.k.get('pod', 'p')['argv'],
                         ['--context', 'kind-x', 'get', 'pod', 'p', '-n', 'test-ns', '-o', 'json'])

    def test_namespace_none_omits_flag(self):
        self.assertEqual(self.k.get('nodes', namespace=None)['argv'],
                         ['--context', 'kind-x', 'get', 'nodes', '-o', 'json'])

    def test_all_namespaces(self):
        self.assertEqual(self.k.items('resourceclaims', all_namespaces=True)[0]['argv'],
                         ['--context', 'kind-x', 'get', 'resourceclaims', '-A', '-o', 'json'])

    def test_selector(self):
        self.assertEqual(self.k.items('pods', namespace='other', selector='app=x')[0]['argv'],
                         ['--context', 'kind-x', 'get', 'pods', '-l', 'app=x', '-n', 'other', '-o', 'json'])

    def test_apply_sends_json_on_stdin(self):
        with mock.patch.object(e2e, 'run', wraps=e2e.run) as run:
            self.k.apply({'kind': 'Pod'})
        self.assertEqual(json.loads(run.call_args.kwargs['input']), {'kind': 'Pod'})
        self.assertEqual(run.call_args.args[0][-4:], ['-n', 'test-ns', '-f', '-'])

    def test_exec_places_container_before_command(self):
        out = self.k.exec('ds/x', 'cat', '/f', namespace='amd-mock', container='agent').stdout
        self.assertEqual(json.loads(out)['argv'],
                         ['--context', 'kind-x', 'exec', '-n', 'amd-mock', 'ds/x', '-c', 'agent', '--', 'cat', '/f'])

    def test_allocation_of_unallocated_claim_is_empty(self):
        with mock.patch.object(self.k, 'get', return_value={'status': {}}):
            self.assertEqual(self.k.allocation('c'), [])

    def test_scheduling_rejected_matches_uid_not_name(self):
        pod = {'metadata': {'uid': 'new'}}
        events = [{'reason': 'FailedScheduling', 'involvedObject': {'name': 'p', 'uid': 'old'}}]
        with mock.patch.object(self.k, 'get', return_value=pod), mock.patch.object(self.k, 'items', return_value=events):
            self.assertFalse(self.k.scheduling_rejected('p'))
            events[0]['involvedObject']['uid'] = 'new'
            self.assertTrue(self.k.scheduling_rejected('p'))


class FileExistsTest(unittest.TestCase):
    def kubectl(self, stdout='', returncode=0):
        k = e2e.Kubectl()
        result = subprocess.CompletedProcess([], returncode, stdout, '')
        k.exec = mock.Mock(return_value=result)
        return k

    def test_present_and_absent(self):
        self.assertTrue(e2e.file_exists(self.kubectl('present\n'), 'p', '/f'))
        self.assertFalse(e2e.file_exists(self.kubectl('absent\n'), 'p', '/f'))

    def test_unexpected_output_raises(self):
        with self.assertRaises(e2e.CommandError):
            e2e.file_exists(self.kubectl(''), 'p', '/f')

    def test_script_reports_both_states(self):
        script = 'if test -e "$1"; then echo present; else echo absent; fi'
        with tempfile.NamedTemporaryFile() as f:
            self.assertEqual(e2e.run(['sh', '-c', script, 'sh', f.name]).stdout, 'present\n')
        self.assertEqual(e2e.run(['sh', '-c', script, 'sh', '/nonexistent/x']).stdout, 'absent\n')


class RecordingKubectl(e2e.Kubectl):
    """Records calls instead of running kubectl."""

    def __init__(self, exists=False):
        super().__init__()
        self.calls = []
        self.exists = mock.Mock(return_value=exists)
        self.diagnostics = mock.Mock()

    def __call__(self, *args, check=True, **kwargs):
        self.calls.append((args, check))
        return subprocess.CompletedProcess(args, 0, '', '')


class ScratchNamespaceTest(unittest.TestCase):
    def kubectl(self, exists=False):
        k = RecordingKubectl(exists)
        return k, k.calls

    def test_refuses_existing_namespace(self):
        k, calls = self.kubectl(exists=True)
        with self.assertRaises(SystemExit):
            with e2e.scratch_namespace(k, 'ns'):
                self.fail('body ran')
        self.assertEqual(calls, [])

    def test_creates_and_deletes(self):
        k, calls = self.kubectl()
        with e2e.scratch_namespace(k, 'ns') as name:
            self.assertEqual(name, 'ns')
        self.assertEqual(calls[0][0], ('create', 'namespace', 'ns'))
        self.assertEqual(calls[-1][0][:3], ('delete', 'namespace', 'ns'))

    def test_keep_skips_delete(self):
        k, calls = self.kubectl()
        with e2e.scratch_namespace(k, 'ns', keep=True):
            pass
        self.assertEqual([c[0][0] for c in calls], ['create'])

    def test_failure_dumps_state_and_cleanup_cannot_mask_it(self):
        k, calls = self.kubectl()
        with self.assertRaises(ValueError):
            with e2e.scratch_namespace(k, 'ns', wait=True):
                raise ValueError('check failed')
        k.diagnostics.assert_called_once_with(namespace='ns')
        delete, check = calls[-1]
        self.assertIn('--wait=true', delete)
        self.assertFalse(check)

    def test_waited_delete_is_checked_on_success(self):
        k, calls = self.kubectl()
        with e2e.scratch_namespace(k, 'ns', wait=True):
            pass
        self.assertTrue(calls[-1][1])


class DeviceProbeTest(unittest.TestCase):
    """Runs the probe with /dev rewritten to a scratch tree."""

    def run_probe(self, script, renders, cards=0, plain=False):
        with tempfile.TemporaryDirectory() as tmp:
            dev = Path(tmp)
            (dev / 'dri').mkdir()
            # A fifo stands in for a character device; test -c must reject it,
            # so the probe is rewritten to test -p instead.
            os.mkfifo(dev / 'kfd')
            for i in range(renders):
                path = dev / 'dri' / f'renderD{128 + i}'
                path.touch() if plain else os.mkfifo(path)
            for i in range(cards):
                os.mkfifo(dev / 'dri' / f'card{i}')
            rewritten = script.replace('/dev/', str(dev) + '/').replace('test -c', 'test -p')
            return subprocess.run(['sh', '-c', rewritten], check=False).returncode

    def test_exact_count_passes(self):
        self.assertEqual(self.run_probe(e2e.device_probe(2), renders=2), 0)

    def test_wrong_count_fails(self):
        self.assertNotEqual(self.run_probe(e2e.device_probe(1), renders=2), 0)
        self.assertNotEqual(self.run_probe(e2e.device_probe(2), renders=1), 0)

    def test_no_devices_fails(self):
        self.assertNotEqual(self.run_probe(e2e.device_probe(1), renders=0), 0)

    def test_plain_file_fails(self):
        self.assertNotEqual(self.run_probe(e2e.device_probe(1), renders=1, plain=True), 0)

    def test_cards_counted_when_asked(self):
        self.assertEqual(self.run_probe(e2e.device_probe(1, cards=True), renders=1, cards=1), 0)
        self.assertNotEqual(self.run_probe(e2e.device_probe(1, cards=True), renders=1, cards=0), 0)


class GrafanaTest(unittest.TestCase):
    def test_numbers_reads_only_numeric_fields(self):
        frames = {'results': {'A': {'frames': [{
            'schema': {'fields': [{'type': 'time'}, {'type': 'number'}, {'type': 'string'}]},
            'data': {'values': [[1700000000], [105, 7], ['gpu0']]}}]}}}
        grafana = e2e.Grafana('http://g', 'pw')
        with mock.patch.object(e2e, 'http_json', return_value=frames):
            self.assertEqual(grafana.numbers('expr'), [105, 7])

    def test_query_error_is_an_assertion(self):
        grafana = e2e.Grafana('http://g', 'pw')
        with mock.patch.object(e2e, 'http_json', return_value={'results': {'A': {'error': 'bad expr'}}}):
            with self.assertRaises(AssertionError):
                grafana.query('expr')

    def test_basic_auth(self):
        self.assertEqual(e2e.basic_auth('admin', 'amdmock'), 'Basic YWRtaW46YW1kbW9jaw==')


class CommonShTest(unittest.TestCase):
    def bash(self, script):
        return subprocess.run(['bash', '-c', 'set -euo pipefail; source "$0"; ' + script, str(COMMON)],
                              capture_output=True, text=True, check=False)

    def test_has_and_lacks(self):
        with tempfile.NamedTemporaryFile('w', suffix='.yaml') as f:
            f.write('kind: DeviceClass\nhostPort: 8080\n')
            f.flush()
            self.assertEqual(self.bash(f'has {f.name} "hostPort: 8080"').returncode, 0)
            self.assertEqual(self.bash(f'lacks {f.name} "kind: Secret"').returncode, 0)
            missing = self.bash(f'has {f.name} "kind: Secret"; echo after')
            self.assertEqual(missing.returncode, 1)
            self.assertNotIn('after', missing.stdout)
            present = self.bash(f'lacks {f.name} "kind: DeviceClass"; echo after')
            self.assertEqual(present.returncode, 1)
            self.assertIn('unexpectedly contains', present.stderr)

    def test_fixed_strings_not_regex(self):
        with tempfile.NamedTemporaryFile('w') as f:
            f.write('a.b\n')
            f.flush()
            self.assertEqual(self.bash(f'lacks {f.name} "a*b"').returncode, 0)

    def test_fail_prints_detail(self):
        result = self.bash('fail "broken" "line one"')
        self.assertEqual(result.returncode, 1)
        self.assertIn('FAIL: broken', result.stderr)
        self.assertIn('| line one', result.stderr)

    def test_expect(self):
        self.assertIn('PASS: label', self.bash('expect "x RETIRED: 2 y" "RETIRED: 2" label').stdout)
        self.assertEqual(self.bash('expect "nothing" "RETIRED: 2" label').returncode, 1)

    def test_shows_survives_pipefail_with_large_output(self):
        # grep -q on a pipe would stop reading early and fail the producer.
        self.assertEqual(self.bash('shows "line 1" seq -f "line %g" 1 200000').returncode, 0)
        self.assertEqual(self.bash('shows "absent" echo present').returncode, 1)
        self.assertEqual(self.bash('shows "x" sh -c "echo x; exit 1"').returncode, 1)

    def test_eventually(self):
        script = 'n=0; step() { n=$((n + 1)); [ "$n" -ge 2 ]; }; eventually 5 step && echo "ok $n"'
        self.assertIn('ok 2', self.bash(script).stdout)
        self.assertEqual(self.bash('eventually 1 false').returncode, 1)

    def test_is_int(self):
        self.assertEqual(self.bash('is_int 42').returncode, 0)
        for value in ['', '-1', '4.2', 'abc', '1 2']:
            self.assertNotEqual(self.bash(f'is_int "{value}"').returncode, 0, value)


class SysRemapTest(unittest.TestCase):
    def remap(self, manifest, env=None):
        return subprocess.run([str(SYS_REMAP)], input=manifest, capture_output=True, text=True, check=False,
                              env={**os.environ, **(env or {})})

    MANIFEST = textwrap.dedent('''\
        volumes:
          - name: sys
            hostPath:
              path: /sys
          - name: sysfs-extra
            hostPath:
              path: /sys
          - name: dev
            hostPath:
              path: /dev
        ''')

    def test_rewrites_only_the_sys_volume(self):
        result = self.remap(self.MANIFEST)
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(result.stdout, self.MANIFEST.replace('path: /sys\n', 'path: /var/lib/fake-gpu/sys\n', 1))

    def test_honours_mock_sys(self):
        result = self.remap(self.MANIFEST, {'MOCK_SYS': '/custom/sys'})
        self.assertIn('path: /custom/sys', result.stdout)

    def test_quoted_values(self):
        result = self.remap('- name: "sys"\n  hostPath:\n    path: "/sys"\n')
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertIn('path: /var/lib/fake-gpu/sys', result.stdout)

    def test_missing_volume_fails(self):
        result = self.remap('volumes:\n  - name: dev\n    hostPath:\n      path: /dev\n')
        self.assertEqual(result.returncode, 1)
        self.assertIn('no "sys" hostPath volume found', result.stderr)


if __name__ == '__main__':
    unittest.main()
