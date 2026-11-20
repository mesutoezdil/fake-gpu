"""Tiny LLM stand-in for OpenShift Local: a scripted reply, plus the proof of
what Kubernetes gave the pod: exactly one render node and /dev/kfd, both real
character devices. No model, no ROCm, no computation.
"""
import glob
import json
import os
import stat
from http.server import BaseHTTPRequestHandler, HTTPServer

MAX_BODY = 4096


def allocation():
    renders = glob.glob('/dev/dri/renderD*')
    assert len(renders) == 1, f'Expected exactly one allocated render device: {renders}'
    devices = {}
    for path in ['/dev/kfd', *renders]:
        st = os.stat(path)
        assert stat.S_ISCHR(st.st_mode), f'Not a character device: {path}'
        devices[path] = {'major': os.major(st.st_rdev), 'minor': os.minor(st.st_rdev)}
    return {'node': os.environ['NODE_NAME'], 'devices': devices, 'simulation': True}


class Handler(BaseHTTPRequestHandler):
    def send(self, body):
        data = json.dumps(body).encode()
        self.send_response(200)
        self.send_header('Content-Type', 'application/json')
        self.send_header('Content-Length', str(len(data)))
        self.end_headers()
        self.wfile.write(data)

    def do_GET(self):  # any path, the readiness probe's included
        self.send(allocation())

    def do_POST(self):
        size = int(self.headers.get('Content-Length', '0'))
        try:
            if not 0 < size <= MAX_BODY:
                raise ValueError(size)
            prompt = json.loads(self.rfile.read(size))['prompt']
            if not isinstance(prompt, str):
                raise ValueError(prompt)
        except (ValueError, KeyError, TypeError):
            self.send_error(400)
            return
        reply = {'text': prompt + ' bright and full of possibilities', 'allocation': allocation()}
        print('SIMULATED_RESPONSE ' + json.dumps(reply), flush=True)
        self.send(reply)


if __name__ == '__main__':
    print('DEVICE_EVIDENCE ' + json.dumps(allocation()), flush=True)
    print('AMD device plugin -> kubelet/CRI-O -> allocated mock character devices.', flush=True)
    print('Scripted response; no model weights or ROCm computation.', flush=True)
    HTTPServer(('0.0.0.0', 8000), Handler).serve_forever()
