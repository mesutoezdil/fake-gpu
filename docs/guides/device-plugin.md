# AMD device plugin

This guide advertises mock GPUs as `amd.com/gpu` and schedules a workload that
requests one. It proves that the real AMD device plugin discovers the mock's
staged GPU topology and that Kubernetes schedules and injects the devices.

## Overview

The chart stages a synthetic discovery tree and deploys AMD's device plugin
unmodified. The plugin discovers the staged topology; Kubernetes and kubelet
handle scheduling and allocation normally. The standard telemetry pipeline and
dashboards use the same installation.

What is mocked: the GPU discovery tree and the character devices. The result
proves scheduling and device injection over mock character devices, not GPU
computation or hardware isolation.

The default quick start uses DRA. Choose this guide for the device-plugin
allocator instead. The two allocators must not allocate the same GPU pool
simultaneously, so use a fresh cluster rather than overwriting an existing DRA
installation. For claim-based allocation, see [AMD DRA](dra.md).

## Requirements

- kind v0.33.0+, kubectl, Helm, and Docker or Podman.
- Kubernetes 1.37 (the supported version).
- A repository checkout as the working directory.
- A fresh local cluster. The published node image and chart are used; no
  custom image build is required.

Podman users should first run:

```bash
export KIND_EXPERIMENTAL_PROVIDER=podman
```

## Steps

### 1. Create a cluster

```bash
kind create cluster --name amd-mock \
  --image ghcr.io/mesutoezdil/amd-mock-kind-node:0.1.0 \
  --config deploy/kind-node/kind-config.yaml
```

The configuration maps the mock dashboard and Grafana ports to the host.

### 2. Install the mock and the real device plugin

Disable DRA and enable the device plugin when installing the chart:

```bash
helm install fake-gpu oci://ghcr.io/mesutoezdil/fake-gpu \
  --version 0.1.0 --namespace amd-mock --create-namespace \
  --set dra.enabled=false --set devicePlugin.enabled=true
kubectl -n amd-mock rollout status ds/fake-gpu --timeout=180s
kubectl -n kube-system rollout status ds/fake-gpu-device-plugin --timeout=180s
```

To use another GPU profile, add for example `--set gpu.profile=mi250x` on a
fresh installation. The expected resource count changes with the
[profile](../reference/profiles.md).

### 3. Wait for allocatable GPUs

Pod readiness can precede kubelet registration and the node-status update, so
wait for the resource count before starting the workload:

```bash
kubectl wait nodes --all \
  --for='jsonpath={.status.allocatable.amd\.com/gpu}=8' --timeout=180s
kubectl get nodes \
  -o custom-columns='NODE:.metadata.name,GPUS:.status.allocatable.amd\.com/gpu'
```

The default MI300X profile exposes eight GPUs. The wait above is specifically
for the default eight-GPU MI300X setup; adjust the count for other profiles.

### 4. Schedule a GPU workload

Create a Pod that requests one `amd.com/gpu`:

```bash
kubectl create namespace amd-device-plugin-demo
kubectl apply -n amd-device-plugin-demo -f - <<'YAML'
apiVersion: v1
kind: Pod
metadata:
  name: gpu-workload
spec:
  restartPolicy: Never
  containers:
  - name: app
    image: docker.io/busybox:1.36
    command: [sh, -c, 'ls -l /dev/kfd /dev/dri; sleep 3600']
    resources:
      limits:
        amd.com/gpu: 1
YAML
kubectl wait -n amd-device-plugin-demo pod/gpu-workload \
  --for=condition=Ready --timeout=180s
```

## Verify

```bash
kubectl logs -n amd-device-plugin-demo gpu-workload
kubectl exec -n amd-device-plugin-demo gpu-workload -- test -c /dev/kfd
```

The log lists KFD plus one allocated card/render pair inside the container, and
`test -c /dev/kfd` exits 0. For the device-plugin workload demo, see the
[Tiny LLM presentation](../../demo/llm/README.md).

The automated check is `test/e2e/profile/scheduling.py`. It asserts exact
allocatable counts, one-GPU scheduling, exhaustion, release, crash and recovery
for each profile on a dedicated single-node cluster. See
[Testing](../contributing/testing.md#profile).

```bash
PROFILE=mi300x python3 test/e2e/profile/scheduling.py
```

## Troubleshooting

| Symptom | Cause | Fix |
| --- | --- | --- |
| Allocatable capacity stays at zero | A discovery or registration problem | Inspect discovery and registration with the commands below |
| Chart install or upgrade fails with both allocators enabled | The chart rejects simultaneous DRA and device-plugin allocation | Set `--set dra.enabled=false --set devicePlugin.enabled=true` |
| Existing DRA workloads do not use the device plugin after switching | ResourceClaim workloads do not automatically become device-plugin workloads | Delete the GPU consumers first, then upgrade the release with the two allocator flags from step 2 |

```bash
kubectl -n amd-mock logs ds/fake-gpu --tail=40
kubectl -n kube-system logs ds/fake-gpu-device-plugin --tail=40
kubectl describe node
kubectl -n amd-device-plugin-demo describe pod gpu-workload
```

## Clean up

```bash
kubectl delete namespace amd-device-plugin-demo
# Remove the cluster only when finished with all workloads in it:
kind delete cluster --name amd-mock
```

## Related

- [AMD DRA](dra.md): claim-based allocation.
- [Spur](../../demo/spur/README.md): batch queueing and GPU reuse.
- [Telemetry](telemetry.md): fault injection and monitoring.
- [Testing](../contributing/testing.md): automated allocation checks.
- [Scenarios](index.md): more presentations.
