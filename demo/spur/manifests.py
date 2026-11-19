#!/usr/bin/env python3
"""Print the Spur controller and Kubernetes operator for the demo, as a List.

Both run the unchanged upstream image named by SPUR_IMAGE (versions.env).
The operator may read nodes and manage pods in the demo namespace only.
"""
import json
import os

NS = 'amd-demo-spur'
IMAGE = os.environ['SPUR_IMAGE']
ALL_VERBS = ['get', 'list', 'watch', 'create', 'update', 'patch', 'delete']


def rbac():
    return [
        {'apiVersion': 'v1', 'kind': 'ServiceAccount', 'metadata': {'name': 'spur-operator', 'namespace': NS}},
        {'apiVersion': 'rbac.authorization.k8s.io/v1', 'kind': 'ClusterRole',
         'metadata': {'name': 'amd-mock-spur-operator'}, 'rules': [
             {'apiGroups': [''], 'resources': ['nodes'], 'verbs': ['get', 'list', 'watch']},
             {'apiGroups': ['spur.amd.com'], 'resources': ['spurjobs', 'spurjobs/status', 'spurjobs/finalizers'], 'verbs': ALL_VERBS},
             {'apiGroups': [''], 'resources': ['pods', 'pods/status', 'pods/log', 'services', 'configmaps'], 'verbs': ALL_VERBS},
             {'apiGroups': [''], 'resources': ['events'], 'verbs': ['create', 'patch']},
             {'apiGroups': ['policy'], 'resources': ['poddisruptionbudgets'], 'verbs': ALL_VERBS}]},
        {'apiVersion': 'rbac.authorization.k8s.io/v1', 'kind': 'ClusterRoleBinding',
         'metadata': {'name': 'amd-mock-spur-operator'},
         'roleRef': {'apiGroup': 'rbac.authorization.k8s.io', 'kind': 'ClusterRole', 'name': 'amd-mock-spur-operator'},
         'subjects': [{'kind': 'ServiceAccount', 'name': 'spur-operator', 'namespace': NS}]},
    ]


def service(name, ports):
    return {'apiVersion': 'v1', 'kind': 'Service', 'metadata': {'name': name, 'namespace': NS},
            'spec': {'selector': {'app': name},
                     'ports': [{'name': f'p{p}', 'port': p, 'targetPort': p} for p in ports]}}


def deployment(name, args, ready_port, pod):
    container = {'name': name, 'image': IMAGE, 'command': [name], 'args': args,
                 # Small CPU requests: the demo's one job asks for a whole CPU on a 2-CPU kind node.
                 'resources': {'requests': {'cpu': '10m', 'memory': '128Mi'}, 'limits': {'memory': '512Mi'}},
                 'readinessProbe': {'tcpSocket': {'port': ready_port}, 'initialDelaySeconds': 3, 'periodSeconds': 5}}
    container.update(pod.pop('container', {}))
    spec = {'containers': [container], 'securityContext': {'fsGroup': 1001}, 'tolerations': [{'operator': 'Exists'}], **pod}
    return {'apiVersion': 'apps/v1', 'kind': 'Deployment', 'metadata': {'name': name, 'namespace': NS},
            'spec': {'replicas': 1, 'selector': {'matchLabels': {'app': name}},
                     'template': {'metadata': {'labels': {'app': name}}, 'spec': spec}}}


def controller():
    """spurctld: the scheduler, with its config, spool and auth key."""
    return deployment('spurctld', ['-D', '--config=/etc/spur/spur.conf', '--state-dir=/var/spool/spur', '--listen=0.0.0.0:6817'], 6817, {
        'container': {'volumeMounts': [{'name': 'config', 'mountPath': '/etc/spur', 'readOnly': True},
                                       {'name': 'spool', 'mountPath': '/var/spool/spur'},
                                       {'name': 'auth', 'mountPath': '/etc/spur-auth', 'readOnly': True}]},
        'volumes': [{'name': 'config', 'configMap': {'name': 'spur-config'}},
                    {'name': 'spool', 'emptyDir': {}},
                    {'name': 'auth', 'secret': {'secretName': 'spur-auth'}}]})


def operator():
    """spur-k8s-operator: turns Spur jobs into pods on managed nodes."""
    return deployment('spur-k8s-operator', [
        '--controller-addr=http://spurctld:6817', '--listen=0.0.0.0:6818', '--health-addr=0.0.0.0:8080',
        f'--address=spur-k8s-operator.{NS}.svc.cluster.local', '--node-selector=spur.amd.com/managed=true',
        '--auth-mode=required'], 6818, {
        'container': {'env': [{'name': 'SPUR_JWT_KEY', 'valueFrom': {'secretKeyRef': {'name': 'spur-auth', 'key': 'jwt-key'}}}]},
        'serviceAccountName': 'spur-operator'})


if __name__ == '__main__':
    items = [*rbac(), service('spurctld', [6817]), controller(), service('spur-k8s-operator', [6818, 8080]), operator()]
    print(json.dumps({'apiVersion': 'v1', 'kind': 'List', 'items': items}))
