# Tracing a demo through every layer

The runner's `PASS` lines say a check held. This page shows how to look at the
same facts yourself, layer by layer, for an allocation and for a fault. The
commands assume the quick-start release `fake-gpu` in namespace `amd-mock`
and the context `kind-amd-mock`:

```bash
kubectl config use-context kind-amd-mock
kubectl get pods -A
kubectl get resourceclaims -A
```

Demo workloads live in `amd-demo-*` namespaces and stay until
`python3 demo/run.py cleanup`, which also resets faults and SPX; do not clean
up before you have inspected them. Keep others from changing GPU state while
you trace.

## The paths

```text
Allocation (DRA, the default):
  profile -> rendered sysfs/KFD and device nodes -> AMD DRA discovery -> ResourceSlice
          -> scheduler and claim -> kubelet, AMD NodePrepareResources -> CDI -> container devices

Allocation (device plugin):
  rendered topology -> AMD device plugin -> amd.com/gpu -> scheduler -> kubelet Allocate -> container devices

Telemetry:
  dashboard action -> agent state -> SMI snapshot -> mock AMD SMI library
                   -> AMD GPU Agent -> AMD Device Metrics Exporter -> Prometheus -> Grafana

Attribution:
  allocated workload -> kubelet pod-resources -> exporter's consumer labels
```

Every arrow after the mock is AMD's or Kubernetes' own code. DRA is not part
of the telemetry path: the scheduler allocates what the driver advertises and
neither reads Grafana nor reacts to faults.

## Before presenting: what is running

```bash
helm list -n amd-mock
kubectl -n amd-mock get pods -o jsonpath='{range .items[*]}{.metadata.name}{"\n"}{range .status.containerStatuses[*]}{.name}{" "}{.image}{" "}{.imageID}{"\n"}{end}{end}'
kubectl -n amd-mock get ds/fake-gpu-metrics-exporter -o yaml
```

Record image digests, not tags: a name alone proves nothing about the
binaries. The exporter manifest shows the SMI state mount and the kubelet
pod-resources mount. The published exporter runtime holds AMD's v1.5.2 GPU
Agent and exporter binaries unchanged, with the mock AMD SMI library in place
of the real one; the [telemetry guide](../docs/guides/telemetry.md) and the
[exporter image source](../deploy/metrics-exporter/) pin both. On ARM64 the
x86 collectors run under QEMU.

## An allocation

```bash
kubectl -n amd-mock exec ds/fake-gpu -- ls /var/lib/fake-gpu/sys/class/kfd/kfd/topology/nodes
kubectl get resourceslices -o yaml
kubectl -n amd-demo-dra get resourceclaims -o yaml
kubectl -n amd-demo-dra get pod dra-gpu-demo -o yaml
kubectl -n amd-demo-dra logs dra-gpu-demo
kubectl -n amd-demo-dra exec dra-gpu-demo -- ls -l /dev/kfd /dev/dri
kubectl -n amd-demo-dra describe pod dra-gpu-demo
kubectl -n amd-mock logs ds/fake-gpu-dra-kubeletplugin --tail=80
```

1. The claim's `status.allocation.devices.results` names a driver, pool and
   device; find that device and its capacity in a ResourceSlice.
2. The slice's node is the pod's `spec.nodeName`.
3. AMD's pinned driver names devices after card and render node: `gpu-0-128`
   is card0 and renderD128. That render node, and `/dev/kfd`, must be
   character devices in the container.

The runner checks the same three things: the device is advertised, the
allocation count is right, and the matching render nodes are injected.

The CDI spec behind the injection is on the kind node:

```bash
docker exec amd-mock-control-plane ls -l /var/run/cdi   # or podman exec
```

`cat` the claim's spec the same way and compare its device edits with the
container's listing. A successful claim plus matching device nodes is the
allocation and injection contract; driver logs, CDI files and pod events show
the steps in between, and ordinary success logs do not trace every call. A
running pod alone, a claim alone or the scripted inference text proves no GPU
access.

With the device plugin there are no claims: check the node's `amd.com/gpu`
capacity, the pod's limits, then the injected nodes. AMD's device plugin runs
inside the mock's DaemonSet; its logs and the [device plugin
guide](../docs/guides/device-plugin.md) cover that path.

## A fault, held while you look

ECC or a fixed overheat is easiest to follow. Recover GPU 0, inject ECC once,
and leave it while you check each layer:

```bash
python3 demo/run.py faults --gpu 0 --action recover
python3 demo/run.py faults --gpu 0 --action ecc-error
curl -fsS http://localhost:8080/api/gpus
kubectl -n amd-mock exec ds/fake-gpu -- cat /var/lib/fake-gpu/smi/gpu0
kubectl get --raw /api/v1/namespaces/amd-mock/services/fake-gpu-metrics-exporter:5000/proxy/metrics
kubectl get --raw '/api/v1/namespaces/amd-mock/services/fake-gpu-prometheus:9090/proxy/api/v1/query?query=amd_gpu_ecc_uncorrect_total'
python3 demo/run.py telemetry
```

| Layer | Look for |
| --- | --- |
| Agent API | `ecc_errors` above 0 on GPU 0 |
| SMI snapshot | The seventh number of the first line |
| AMD exporter `/metrics` | `amd_gpu_ecc_uncorrect_total{gpu_id="0",...}` |
| Prometheus | The same series and value |
| Grafana | The ECC panel, and the runner's datasource query |

The snapshot's first line is temperature (C), power (W), power cap (W),
activity (%), used and total memory (MiB), ECC count, graphics and memory
clock (MHz); then model, SMI UUID and PCI address, then status, throttling
and retired pages. It is what the mock AMD SMI library reads, not a made-up
Prometheus answer; [the format](../pkg/smistate/smistate.go) and [the
library](../pkg/mocksmi/) mark that boundary. The exporter endpoint above is
AMD's exporter, not the agent's own diagnostic `/metrics`.

The value rising at every layer is the propagation evidence. Busy readings
differ a little between layers, because each samples a moving simulation.
Grafana refreshes every 5 seconds, after collection and scrape; reload a page
opened before provisioning changed. If data stops:

```bash
kubectl -n amd-mock get pods
kubectl -n amd-mock describe deployment/fake-gpu-grafana
kubectl -n amd-mock logs deployment/fake-gpu-grafana --tail=40
kubectl -n amd-mock logs ds/fake-gpu-metrics-exporter --tail=40
```

A restarting or OOM-killed pod is a failure, not a fault the library ignored.
Restore the GPU afterwards:

```bash
python3 demo/run.py faults --gpu 0 --action recover
```

## What the demos do not show

They establish discovery, scheduling, allocation, preparation, device
injection and telemetry against mocked device interfaces. They do not show
GPU computation, performance, hardware isolation, AMD hardware partition
management, alerting or eviction, or schedulable CPX slices.

The [same-GPU partition demo](partition-allocation/README.md) does verify
fixed MI300X DPX/NPS2 slices through unchanged AMD DRA, the scheduler, kubelet
and CDI; its [layer
responsibilities](../docs/guides/partition-allocation.md#responsibilities-of-each-layer)
are about what each layer sees, not isolation. It leaves
`amd-demo-partition-allocation` for inspection: that namespace is not labelled
for `cleanup`, and a fixed DPX node refuses the SPX reset and profile changes,
so delete it yourself ([setup](README.md#the-same-gpu-partition-presentation)).
