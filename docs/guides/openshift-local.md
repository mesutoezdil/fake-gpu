# Tiny LLM on OpenShift Local with restricted-v2 SCC

This guide deploys eight mock MI300X GPUs and a scripted Tiny LLM service on
OpenShift Local (CRC) using published images. It proves OpenShift scheduling,
device injection and an application response while the application runs
under OpenShift's `restricted-v2` SCC.

## Overview

**The Tiny LLM application explicitly requires OpenShift's `restricted-v2`
SCC.** The verifier checks the admitted Pod's `openshift.io/scc` annotation.
Only the mock agent and AMD device plugin receive namespace-scoped permission
to use the existing `privileged` SCC; the application receives no privileged
grant.

The demo uses the real AMD device plugin, rather than DRA or the GPU Operator.
No model weights, ROCm computation, or NIC is involved. It is separate from the
Kind-based guides: choose it to check OpenShift admission, CRI-O device
injection and SCC behavior. For the Kind device-plugin path, see
[AMD device plugin](device-plugin.md).

On macOS, OpenShift Local (CRC) runs its own VM through vfkit. It is not a Kind
cluster inside the Podman machine. Podman can remain installed alongside CRC;
OpenShift uses CRI-O inside its VM.

## Requirements

- macOS Apple Silicon host (CRC runs an ARM64 OpenShift VM).
- CRC from the official macOS installer, and CRC's bundled `oc` CLI. See the
  [CRC installation](https://crc.dev/docs/installing/) and
  [usage](https://crc.dev/docs/using/) documentation. CRC does not support
  nested virtualization.
- Helm and Python 3.
- A Red Hat pull secret downloaded to a private local file. Do not paste it
  into logs or commit it.
- The existing local CRC configuration: 8 CPUs, 32 GiB RAM and a 100 GiB disk.
  It is not necessary to resize or recreate the Podman machine for this demo.

## Steps

### 1. Start CRC

The setup script runs `crc setup` and `crc start` with the pull-secret file.
The default pull-secret path is `../crc/pull-secret.txt` relative to the
repository root (the local development CRC folder). `PULL_SECRET_FILE`
overrides it for another local folder or a CI-provided protected file. Never
add the secret to source control or artifacts.

```bash
./demo/openshift-local/tiny-llm/setup.sh
# Override for another local folder or a CI-provided protected file:
# PULL_SECRET_FILE=/absolute/path/to/pull-secret.json ./demo/openshift-local/tiny-llm/setup.sh
```

The setup script uses the existing CRC configuration; it does not create a
hosted CI deployment.

### 2. Log in as kubeadmin

Load CRC's `oc` into the shell, then follow the kubeadmin login instruction
printed by `crc start`. Confirm the selected API is
`https://api.crc.testing:6443`.

```bash
eval "$(crc oc-env --shell bash)"
# Use the kubeadmin login instruction printed by crc start.
oc get nodes
oc get clusteroperators
```

### 3. Create a Python environment

From the repository root:

```bash
python3 -m venv /tmp/amd-crc-demo-venv
/tmp/amd-crc-demo-venv/bin/pip install PyYAML
```

### 4. Render and install

`render` prints the adapted manifests; `install` applies them and waits for the
Tiny LLM Pod.

```bash
/tmp/amd-crc-demo-venv/bin/python demo/openshift-local/tiny-llm/run.py render
/tmp/amd-crc-demo-venv/bin/python demo/openshift-local/tiny-llm/run.py install
```

## Verify

```bash
/tmp/amd-crc-demo-venv/bin/python demo/openshift-local/tiny-llm/run.py verify
oc -n amd-mock-crc-llm get pods -o wide
oc -n amd-mock-crc-llm logs tiny-llm
```

`run.py verify` asserts that the Pod requests one `amd.com/gpu`, runs on the
mock node, and was admitted with `openshift.io/scc: restricted-v2`. It prints
`APPLICATION_SCC restricted-v2`, the response body, and ends with:

```text
PASS: OpenShift scheduler -> AMD device plugin -> CRI-O device injection -> scripted Tiny LLM response
```

In the Pod log, `DEVICE_EVIDENCE` must show `/dev/kfd` and exactly one
`/dev/dri/renderD*`, each as a character device. `SIMULATED_RESPONSE` shows the
prompt, scripted output, node and allocation evidence.

A passing verify command establishes scheduling, device injection and
application response; it does not establish GPU computation.

## How it works

### Architecture

```text
macOS Apple Silicon
 |
 +-- Podman machine (separate; not used to host this cluster)
 |
 +-- [1] CRC / vfkit -> ARM64 OpenShift VM
       |
       +-- [2] Mock node agent creates synthetic GPU discovery and device files
       +-- [3] AMD device plugin discovers them and advertises amd.com/gpu: 8
       +-- [4] OpenShift scheduler places Tiny LLM Pod requesting one GPU
       +-- [5] Kubelet / CRI-O inject allocated KFD + one render device
       +-- [6] App verifies character devices and returns scripted HTTP response
```

### What the adapter changes

The adapter (`demo/openshift-local/tiny-llm/run.py`) renders published chart
0.1.0 with `demo/openshift-local/tiny-llm/values.yaml` and changes only the
namespace, the OpenShift service-account/SCC settings, and the plugin's `/dev`
host mount. CRI-O requires normal `/dev/null`; the mock agent already stages
GPU device files on host `/dev`. The plugin retains the synthetic sysfs
discovery mount.

### SCC model

- The mock and plugin use a dedicated service account with namespace-scoped
  access to the existing privileged SCC.
- The application uses the namespace's default service account and requests
  GPU resources normally. No global SCC or default service-account grants are
  added.
- The application explicitly requires `restricted-v2` through
  `openshift.io/required-scc`, runs non-root, drops capabilities and disables
  privilege escalation. It receives no privileged SCC grant. No cluster-wide
  SELinux policy change is needed.

The demo also works under CRC admin's `anyuid` SCC. Pinning `restricted-v2` is
an application security/portability check, not a GPU-allocation mechanism. The
dedicated mock/node-plugin account still needs the privileged SCC. See
[OpenShift SCC configuration](https://docs.redhat.com/en/documentation/openshift_container_platform/4.22/html/authentication_and_authorization/managing-pod-security-policies).

## Troubleshooting

| Symptom | Cause | Fix |
| --- | --- | --- |
| The VM unexpectedly stops | CRC's vfkit process can stop when the terminal session that started it ends; this is separate from GPU allocation | Check `crc status` and restart CRC before running the demo |
| `setup.sh` reports an unreadable pull-secret file | The default `../crc/pull-secret.txt` is missing or unreadable | Set `PULL_SECRET_FILE` to a readable Red Hat pull-secret file |
| `install` fails with `Select the CRC admin context before running this demo` | The current `oc` context does not point at `api.crc.testing` | Run `eval "$(crc oc-env --shell bash)"` and log in as kubeadmin again |
| The demo namespace is in a bad state | Leftover demo workloads | Reinstall: this resets the demo namespace, not the whole CRC VM. For a complete reset, see [Clean up](#clean-up) |

## Clean up

```bash
/tmp/amd-crc-demo-venv/bin/python demo/openshift-local/tiny-llm/run.py cleanup
crc stop
```

Namespace cleanup removes the demo workloads, not files staged on the VM host.
For a complete reset, `crc delete` deletes the whole CRC VM and all its cluster
data; use it only when you intend to remove the entire local cluster.

## Related

- [AMD device plugin](device-plugin.md)
- [Tiny LLM presentation](../../demo/llm/README.md)
- [Scenarios](index.md)
- [CRC documentation](https://crc.dev/docs/using/)
