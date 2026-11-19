# Optional openable devices and libdrm capacity queries

This guide installs the optional CUSE/library backend and runs a probe that
opens its allocated DRM character devices and queries capacity through
`libdrm_amdgpu`. It proves that consumers which open devices and call a small
subset of `libdrm_amdgpu` (as HAMi does) work against an allocated mock GPU.

## Overview

The default mock provides discovery, allocation and telemetry surfaces. The
optional CUSE/library backend additionally supports consumers that open DRM
character devices and call a small subset of `libdrm_amdgpu`. Real DRA or the
real AMD device plugin allocates the devices; the ordinary quick start
continues to use the lightweight backend.

```text
[1] Existing profile YAML -> original Go renderer / live state
                |                 |                   |
                v                 v                   v
[2] Mock sysfs topology      Mock AMD SMI        Mock libdrm_amdgpu
    identity and RAS         identity/telemetry  device initialize,
                |                 |             CU count / VRAM / name
                +-----------------+-------------------+
                                  |
[3] Real consumer discovery -> real DRA OR device-plugin allocation
                                  |
[4] CDI / kubelet injects assigned CUSE-backed character devices
                                  |
[5] Consumer opens device -> kernel CUSE -> userspace device handler
    Unsupported reads/writes/ioctls fail; no GPU execution is supplied.
```

This is a library API simulator, not an implementation of the real AMD kernel
ioctl protocol. Applications issuing raw AMDGPU/KFD ioctls are not covered. No
HIP computation, DMA, physical isolation or performance fidelity is claimed.

Choose this guide only when a consumer must open the devices or call libdrm.
For allocation alone, use [AMD DRA](dra.md) or the
[AMD device plugin](device-plugin.md).

## Requirements

- A dedicated CPU-only Linux node with a running Docker or Podman runtime.
  Real AMDGPU nodes are rejected.
- CUSE in the host kernel. For a VM-backed runtime, load it in that Linux VM.
  A container image cannot supply a missing host kernel module.
- Kind v0.33.0+, kubectl v1.37, Helm and Python 3.
- No compiler or image build: the published driver image is used. See the
  [build recipe](../../deploy/driver-mock/Dockerfile).
- One cluster per allocator: use separate clusters for DRA and the device
  plugin.

## Steps

### 1. Load CUSE

Run this in the runtime's Linux kernel (the host, or the runtime's VM), then
check that `/dev/cuse` exists:

```bash
sudo modprobe cuse
ls -l /dev/cuse
```

### 2. Create a dedicated cluster

Use the normal node image:

```bash
# For Podman: export KIND_EXPERIMENTAL_PROVIDER=podman
kind create cluster --name amd-mock \
  --image ghcr.io/mesutoezdil/amd-mock-kind-node:0.1.0 \
  --config deploy/kind-node/kind-config.yaml
```

### 3. Install the chart with the driver backend

```bash
helm install fake-gpu oci://ghcr.io/mesutoezdil/fake-gpu \
  --version 0.1.0 -n amd-mock --create-namespace \
  --set driverMock.enabled=true \
  --set image.repository=ghcr.io/mesutoezdil/fake-gpu-driver \
  --set image.tag=v0.1.0 --wait --timeout 5m
```

For the AMD device-plugin allocator, also add `--set dra.enabled=false` and
`--set devicePlugin.enabled=true`.

### 4. Run the allocated-workload probe

```bash
DRIVER_IMAGE=ghcr.io/mesutoezdil/fake-gpu-driver:v0.1.0 \
  python3 -u test/e2e/driver/allocated.py
```

On a device-plugin installation, set `ALLOCATOR=device-plugin` when running
the probe (the default is `dra`).

## Verify

`test/e2e/driver/allocated.py` requests a real DRA (or device-plugin)
allocation, opens only its injected render/KFD nodes, and queries the staged
libdrm library. It checks profile capacity, both memory query layouts, short
buffers, unsupported operations and invalid handles. On success it prints:

```text
PASS: allocated workload native KFD/render access and libdrm capacity
```

It then cleans up its test namespace. The cluster and dashboards stay
available.

Without a cluster, `test/e2e/driver/native.py` runs native CUSE checks against
a locally built image on a Linux host with CUSE loaded:

```bash
docker build -f deploy/driver-mock/Dockerfile -t local/fake-gpu-driver:test .
CONTAINER_RUNTIME=docker DRIVER_IMAGE=local/fake-gpu-driver:test \
  python3 -u test/e2e/driver/native.py
```

`test/e2e/driver/restart.py` covers provider restarts (see
[Rolling updates and mounted state](#rolling-updates-and-mounted-state)).

## How it works

### Host safety

The provider is privileged infrastructure; allocated workload Pods do not need
privileged mode. Existing DRM nodes inherited by CPU VMs (for example virtual
displays) are covered by reversible bind mounts inside the dedicated node; the
underlying nodes are never deleted. Bidirectional propagation exposes that
overlay to the node's kubelet. Cleanup removes the overlay and restores the
underlying device. Do not use the simulated node for unrelated DRM workloads
during the demo.

Native-open tests currently use the container's default UID 0 without
privileged mode; non-root and OpenShift SCC access are separate validation
work.

### Using the staged library

Consumer containers must explicitly use the staged library at
`/var/lib/fake-gpu/driver/usr/lib64/libdrm_amdgpu.so.1`: mount the overlay
read-only and select it with `LD_LIBRARY_PATH`. Set `AMD_DRIVER_MOCK_ROOT` when
that overlay is mounted elsewhere. Ordinary workloads are not globally
preloaded with a replacement libdrm.

### Supported interfaces and lifecycle

- Native opens of KFD, card and render nodes, using dynamically registered CUSE
  numbers rather than a `/dev/null` substitute.
- `amdgpu_device_initialize/deinitialize`, GPU CU count/device ID, VRAM
  capacity, and profile-derived marketing name. Other GPU-info fields are
  zero/unspecified.
- `AMDGPU_INFO_VRAM_GTT` returns its capacity structure; `AMDGPU_INFO_MEMORY`
  returns the full heap structure or a requested prefix, including HAMi's short
  query. Zero/oversized buffers are rejected. Heap usage follows the existing
  simulated SMI state; all profile VRAM is modeled as CPU-accessible and GTT is
  unmodeled (zero). These are simulated values, not host allocations. See the
  [Linux UAPI](https://github.com/torvalds/linux/blob/master/include/uapi/drm/amdgpu_drm.h).
- Unknown queries and firmware queries return `EOPNOTSUPP`; malformed handles
  or buffers are rejected. Raw ioctls return `ENOTTY`, reads/writes
  `EOPNOTSUPP`.
- Multiple GPUs and fixed partition inventory use the same original renderer.
  Profile changes reconcile endpoints.
- Simulated crashes withdraw driver bindings and remove unbound owned host
  paths. Protected bind overlays remain in place while live RAS state rejects
  libdrm calls; recovery restores missing paths using registered endpoints,
  not legacy dummy numbers.
- Fatal RAS state rejects libdrm capacity/initialization calls. This does not
  promise automatic workload eviction or real hardware failure behavior.
- The driver image supplies the existing ABI 27 AMD SMI shim, including BDF
  lookup, using the original renderer's same SMI state.
- Provider shutdown removes owned nodes. An unclean restart re-registers the
  endpoints and replaces stale nodes. Existing open handles across provider
  death are not guaranteed to recover; applications must reopen/retry.

### Rolling updates and mounted state

The node agent preserves staged state during shutdown so collectors and
allocators retain their mounted directory inodes. Startup reconciles inventory
inside the existing sysfs directory and replaces SMI state files atomically.
Both forced and graceful restart checks require stable sys/SMI directory
inodes, then repeat native calls from a newly allocated Pod. Final directory
removal belongs to dedicated cluster teardown. Existing open CUSE handles still
require application retry/reopening when their backing provider is replaced.

### Build images

Build recipes live in `deploy/driver-mock/`; the default mock image is not
changed. Both Linux AMD64 and ARM64 are build targets. Windows is out of scope.

```bash
scripts/build-driver-mock-images.sh
# Maintainer publication, after validation:
scripts/build-driver-mock-images.sh --push
```

### CI coverage

The `driver-backend` workflow runs native CUSE checks on both architectures,
all seven profile/device-plugin suites, DRA basic/lifecycle/focused suites,
fixed DPX allocation, unclean Kubernetes provider restart followed by a fresh
allocated native-open/libdrm check, scripted Tiny LLM/presenter demos,
dashboard, real exporter telemetry and attribution. The existing
default-backend workflows remain regression gates.

## Troubleshooting

| Symptom | Cause | Fix |
| --- | --- | --- |
| `/dev/cuse` is missing | CUSE is not loaded in the runtime's Linux kernel | Run `sudo modprobe cuse` on the host, or inside the VM for a VM-backed runtime; a container image cannot supply it |
| The node is rejected | The node has real AMDGPU devices | Use a dedicated CPU-only Linux node |
| A consumer does not see mock capacity through libdrm | It is not using the staged library | Mount the overlay read-only and set `LD_LIBRARY_PATH`, or `AMD_DRIVER_MOCK_ROOT` if mounted elsewhere |
| libdrm initialization/capacity calls fail after a fault | Fatal RAS state rejects these calls | Use the Recover action (see [Telemetry](telemetry.md#other-dashboard-actions)) |
| Open handles fail after a provider restart | Existing handles are not guaranteed to survive provider death | Reopen/retry in the application |
| Raw ioctls fail with `ENOTTY` | Raw AMDGPU/KFD ioctls are not implemented | Use the supported libdrm calls |

## Clean up

```bash
helm uninstall fake-gpu -n amd-mock
kind delete cluster --name amd-mock
```

## Related

- [AMD DRA](dra.md)
- [AMD device plugin](device-plugin.md)
- [Testing](../contributing/testing.md#driver)
- [Simulation depth](../reference/simulation-depth.md)
- [Architecture](../reference/architecture.md)
