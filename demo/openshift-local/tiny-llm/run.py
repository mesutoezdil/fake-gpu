#!/usr/bin/env python3
"""Tiny LLM on OpenShift Local (CRC) under the restricted-v2 SCC.

    run.py render | install | verify | cleanup

The published chart is installed into its own namespace with AMD's device
plugin. Only the mock's node agents get the privileged SCC; the application
pod runs restricted-v2 and asks for one amd.com/gpu.
"""
import argparse
import json
import subprocess
import time
from pathlib import Path

import yaml

HERE = Path(__file__).resolve().parent
NS = 'amd-mock-crc-llm'
RELEASE = 'fake-gpu'
CHART, CHART_VERSION = 'oci://ghcr.io/mesutoezdil/fake-gpu', '0.1.0'
NODE_SA = 'mock-node'
GPUS = 8


def oc(*args, capture=False, stdin=None):
    return subprocess.run(['oc', *args], check=True, text=True, capture_output=capture, input=stdin).stdout


def apply(docs):
    oc('apply', '-f', '-', stdin=yaml.safe_dump_all(docs))


def render():
    """The chart's objects, adapted to CRI-O and OpenShift's SCCs."""
    out = subprocess.run(['helm', 'template', RELEASE, CHART, '--version', CHART_VERSION, '-n', NS,
                          '-f', str(HERE / 'values.yaml')], check=True, text=True, capture_output=True).stdout
    docs = [d for d in yaml.safe_load_all(out) if d]
    for d in docs:
        d['metadata']['namespace'] = NS
        if d['kind'] != 'DaemonSet':
            continue
        pod = d['spec']['template']['spec']
        pod['serviceAccountName'] = NODE_SA
        pod.pop('priorityClassName', None)
        if d['metadata']['name'].endswith('-device-plugin'):
            # CRI-O wants real /dev entries; the agent already staged the GPU
            # nodes in host /dev. Only sysfs discovery stays synthetic.
            for v in pod['volumes']:
                if v['name'] == 'dev':
                    v['hostPath']['path'] = '/dev'
    daemonsets = {d['metadata']['name'] for d in docs if d['kind'] == 'DaemonSet'}
    if daemonsets != {RELEASE, RELEASE + '-device-plugin'}:
        raise RuntimeError(f'Unexpected daemonsets: {daemonsets}')
    rbac = {'apiGroup': 'rbac.authorization.k8s.io', 'kind': 'Role', 'name': 'mock-privileged-scc'}
    return docs + [
        {'apiVersion': 'v1', 'kind': 'ServiceAccount', 'metadata': {'name': NODE_SA, 'namespace': NS}},
        {'apiVersion': 'rbac.authorization.k8s.io/v1', 'kind': 'Role', 'metadata': {'name': rbac['name'], 'namespace': NS},
         'rules': [{'apiGroups': ['security.openshift.io'], 'resources': ['securitycontextconstraints'],
                    'resourceNames': ['privileged'], 'verbs': ['use']}]},
        {'apiVersion': 'rbac.authorization.k8s.io/v1', 'kind': 'RoleBinding', 'metadata': {'name': rbac['name'], 'namespace': NS},
         'roleRef': rbac, 'subjects': [{'kind': 'ServiceAccount', 'name': NODE_SA, 'namespace': NS}]},
    ]


def node():
    """The one CRC node; refuse anything else."""
    nodes = json.loads(oc('get', 'nodes', '-o', 'json', capture=True))['items']
    if len(nodes) != 1:
        raise RuntimeError('Requires a dedicated single-node CRC cluster')
    if not nodes[0]['status']['nodeInfo']['containerRuntimeVersion'].startswith('cri-o://'):
        raise RuntimeError('Requires OpenShift CRI-O')
    return nodes[0]


def gpus(n, field):
    return int(n['status'].get(field, {}).get('amd.com/gpu', 0))


def app_pod():
    return {'apiVersion': 'v1', 'kind': 'Pod',
            'metadata': {'name': 'tiny-llm', 'namespace': NS, 'annotations': {'openshift.io/required-scc': 'restricted-v2'}},
            'spec': {'securityContext': {'runAsNonRoot': True, 'seccompProfile': {'type': 'RuntimeDefault'}},
                     'containers': [{
                         'name': 'app', 'image': 'docker.io/library/python:3.11-slim',
                         'command': ['python3', '-u', '-c', (HERE / 'app.py').read_text()],
                         'securityContext': {'allowPrivilegeEscalation': False, 'capabilities': {'drop': ['ALL']}},
                         'env': [{'name': 'NODE_NAME', 'valueFrom': {'fieldRef': {'fieldPath': 'spec.nodeName'}}}],
                         'resources': {'limits': {'amd.com/gpu': 1, 'memory': '256Mi'},
                                       'requests': {'amd.com/gpu': 1, 'cpu': '100m', 'memory': '64Mi'}},
                         'ports': [{'containerPort': 8000}],
                         'readinessProbe': {'httpGet': {'path': '/healthz', 'port': 8000}}}]}}


def install():
    # Insist on CRC's API, so a mistaken context never touches another cluster.
    server = oc('config', 'view', '--minify', '-o', 'jsonpath={.clusters[0].cluster.server}', capture=True)
    if 'api.crc.testing' not in server:
        raise RuntimeError('Select the CRC admin context before running this demo')
    oc('get', 'clusterversion', 'version')
    n = node()
    if gpus(n, 'capacity') and not oc('-n', NS, 'get', 'ds', RELEASE, '--ignore-not-found', '-o', 'name', capture=True).strip():
        raise RuntimeError('Existing GPU capacity belongs to another installation')
    if not any(c['type'] == 'Ready' and c['status'] == 'True' for c in n['status']['conditions']):
        raise RuntimeError('CRC node is not Ready')

    apply([{'apiVersion': 'v1', 'kind': 'Namespace',
            'metadata': {'name': NS, 'labels': {'pod-security.kubernetes.io/enforce': 'privileged'}}}])
    docs = render()
    plugin = next(d for d in docs if d['kind'] == 'DaemonSet' and d['metadata']['name'].endswith('-device-plugin'))
    # The agent stages the devices before the plugin looks for them.
    apply([d for d in docs if d['kind'] != 'DaemonSet'])
    apply([d for d in docs if d['kind'] == 'DaemonSet' and d is not plugin])
    oc('-n', NS, 'rollout', 'status', 'ds/' + RELEASE, '--timeout=600s')
    apply([plugin])
    oc('-n', NS, 'rollout', 'status', 'ds/' + RELEASE + '-device-plugin', '--timeout=600s')
    deadline = time.monotonic() + 180
    while gpus(node(), 'allocatable') != GPUS:
        if time.monotonic() > deadline:
            raise RuntimeError('Expected eight mock GPUs in allocatable')
        time.sleep(3)
    apply([app_pod()])
    oc('-n', NS, 'wait', '--for=condition=Ready', 'pod/tiny-llm', '--timeout=600s')
    verify()


def verify():
    pod = json.loads(oc('-n', NS, 'get', 'pod', 'tiny-llm', '-o', 'json', capture=True))
    assert pod['spec']['containers'][0]['resources']['limits']['amd.com/gpu'] == '1'
    assert pod['spec']['nodeName'] == node()['metadata']['name']
    assert pod['metadata']['annotations']['openshift.io/scc'] == 'restricted-v2'
    print('APPLICATION_SCC restricted-v2')
    ask = ("import urllib.request; print(urllib.request.urlopen(urllib.request.Request('http://127.0.0.1:8000/generate', "
           "data=b'{\"prompt\":\"The future of AI is\"}', headers={'Content-Type':'application/json'})).read().decode())")
    reply = json.loads(oc('-n', NS, 'exec', 'tiny-llm', '--', 'python3', '-c', ask, capture=True))
    assert reply['allocation']['simulation'] is True
    assert reply['text'] == 'The future of AI is bright and full of possibilities'
    print(json.dumps(reply, indent=2))
    oc('-n', NS, 'logs', 'tiny-llm')
    print('PASS: OpenShift scheduler -> AMD device plugin -> CRI-O device injection -> scripted Tiny LLM response')


def cleanup():
    oc('delete', 'namespace', NS, '--ignore-not-found')
    print('Namespace removed. Mock host device files remain; use crc delete for a complete VM reset.')


if __name__ == '__main__':
    parser = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    parser.add_argument('action', choices=['render', 'install', 'verify', 'cleanup'])
    action = parser.parse_args().action
    if action == 'render':
        print(yaml.safe_dump_all(render()))
    else:
        {'install': install, 'verify': verify, 'cleanup': cleanup}[action]()
