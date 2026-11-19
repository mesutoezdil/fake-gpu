# Presenter guide

Everything a presenter needs, from an empty machine to a cleaned-up cluster.
The demos run on CPU-only Kubernetes workers from published images: no AMD
source, compiler or image build. They show scheduling and device injection,
not GPU inference or hardware isolation.

## 1. Set up once

You need kind v0.33.0 or newer, kubectl, Helm, Python 3 and a running Docker
or Podman.

```bash
./demo/setup.sh
```

This creates the kind cluster and installs the mock with DRA, the node agent,
AMD's real exporter, Prometheus and Grafana with the dashboard provisioned:

| Endpoint | Address |
| --- | --- |
| Mock dashboard | http://localhost:8080 |
| Grafana | http://localhost:3000 (`admin` / `amdmock`) |

No port-forward is involved; both stay up as long as the cluster does.

- `setup.sh` never deletes a cluster. `--skip-cluster` upgrades an existing
  compatible one, `--teardown` removes it.
- `CONTAINER_RUNTIME=docker` or `=podman` overrides the runtime it detects.
- A cluster created before the Grafana port mapping existed has to be
  recreated to expose Grafana on the host.
- Prometheus and Grafana keep their data in ephemeral storage: this is a
  local demo environment.

## 2. Configure ports, dashboards and the allocator

[config.yaml](config.yaml) is the single source for both the kind host ports
and the Helm values, so a port is never mapped on one side and served on
another. Edit a copy **before** the cluster exists; kind cannot change host
port mappings afterwards (`--skip-cluster` still updates the Helm side).

```bash
cp demo/config.yaml /tmp/my-demo.yaml   # edit, then:
./demo/setup.sh --config /tmp/my-demo.yaml
python3 demo/run.py telemetry --config /tmp/my-demo.yaml
```

| Setting | Effect |
| --- | --- |
| `kind.mockDashboardPort`, `kind.grafanaPort` | Host ports; they must differ |
| `dashboard.enabled: false` | No host exposure of the mock dashboard and API |
| `monitoring.enabled: false` | No bundled Prometheus and Grafana |
| `monitoring.grafana.enabled: false` | Prometheus without Grafana |
| `dra.enabled` / `devicePlugin.enabled` | The allocator; exactly one is true |

The node agent's API keeps running inside the cluster when its host dashboard
is off, but the fault, partitioning and profile demos need the host
exposure. The DRA and workload demos need neither dashboard.

For the device plugin, set `dra.enabled: false` and `devicePlugin.enabled:
true`. Delete demo workloads before switching; never enable both. The
workload demos detect the installed allocator on their own.

For another cluster, set `CLUSTER_NAME=my-demo` for setup and pass `--context
kind-my-demo` to the runner. A separate `KUBECONFIG` leaves your current
kubectl context untouched.

## 3. Run demos

```bash
python3 demo/run.py list
```

| Demo | What to show | Command |
| --- | --- | --- |
| [llm](llm/README.md) | Tiny LLM scheduled with one GPU, scripted output | `python3 demo/run.py llm` |
| [dra](dra/README.md) | ResourceSlice, claim allocation and CDI devices | `python3 demo/run.py dra` |
| [multi-gpu](multi-gpu/README.md) | Two GPUs in one pod, or several independent consumers | `python3 demo/run.py multi-gpu --count 2` |
| [allocation](allocation/README.md) | Delete a consumer, see the release, allocate again | `python3 demo/run.py allocation` |
| [telemetry](telemetry/README.md) | Real AMD metrics and consumer labels in Grafana | `python3 demo/run.py telemetry` |
| [faults](faults/README.md) | Overheat, ECC, crash and recovery through real telemetry | `python3 demo/run.py faults --action overheat` |
| [partitioning](partitioning/README.md) | SPX/DPX/QPX/CPX as virtual dashboard state | `python3 demo/run.py partitioning --mode CPX` |
| [profiles](profiles/README.md) | Model catalog and the installed inventory | `python3 demo/run.py profiles` |
| [partition-allocation](partition-allocation/README.md) | Two containers on distinct 96 GiB slices of one MI300X; needs a DPX/NPS2 cluster | `./demo/partition-allocation/run.sh` |
| [spur](spur/README.md) | Unchanged Spur: submission, queueing, injection, completion, reuse; its own cluster | `demo/spur/setup.sh`, `demo/spur/run.py` |

Use a cluster dedicated to the demo. Workloads keep running for inspection
and hold their GPUs until cleanup. The runner puts each demo in a namespace
labelled `fake-gpu/demo=true`, such as `amd-demo-llm`; `kubectl get pods -A`
shows them. The profile, fault, telemetry and partitioning demos change or
read state and create no pods.

### Reading the output

The runner prints `EVIDENCE` for what it inspected and `PASS` only for checks
it actually made. Each demo's guide explains what to watch, the layers
involved and the limits of the proof. [EVIDENCE.md](EVIDENCE.md) shows how to
trace one allocation and one fault through every layer.

### A ten-minute presentation

1. Open both dashboards; point out the eight physical MI300X GPUs.
2. Run `llm` and show the injected card and render nodes and the scripted log.
3. Run `dra` and show the allocated device names in the claim.
4. In Grafana, show the pod, namespace and container labels.
5. Trigger Overheat or ECC: the dashboard changes at once, Grafana after the
   next collection and scrape. Recover GPU 0.
6. Clean up workloads, then show virtual CPX state and reset to SPX.
7. On request, show `multi-gpu` or `allocation`.

Faults do not revoke claims, evict pods or restart workloads. CPX entries are
not schedulable hardware slices. The [architecture
diagrams](../docs/reference/architecture.md) help explain both boundaries.

## 4. Clean up

```bash
python3 demo/run.py cleanup      # demo namespaces only
./demo/setup.sh --teardown       # the whole demo cluster
```

`cleanup` deletes the demo-labelled namespaces, resets SPX and recovers every
mock GPU when the host API is enabled. Pass the `--config` and context used
at setup. The Helm release stays until the cluster is deleted.

## The same-GPU partition presentation

The standard presentation uses whole SPX GPUs. [Partition
allocation](partition-allocation/README.md) needs its own fresh cluster,
created by the same script from the same artifacts: copy `config.yaml`, set
`gpu.partition: DPX`, keep `gpu.profile: mi300x` and DRA, and pick free host
ports if the standard cluster is still running.

```bash
cp demo/config.yaml /tmp/partition-demo.yaml   # gpu.partition: DPX, free host ports
KUBECONFIG=/tmp/partition-demo.kubeconfig CLUSTER_NAME=amd-partition-demo \
  ./demo/setup.sh --config /tmp/partition-demo.yaml
KUBECONFIG=/tmp/partition-demo.kubeconfig ./demo/partition-allocation/run.sh
```

Show the printed ResourceClaim and ResourceSlice evidence and both container
logs: two distinct render devices, each with 96 GiB advertised, from one
physical GPU. The run also covers exhaustion, CDI cleanup, reuse and a driver
restart, and leaves the final two-container pod running. It is a separate
runner, so `demo/run.py list` does not show it.

Its namespace, `amd-demo-partition-allocation`, is not labelled for
`cleanup`, and a fixed DPX node refuses the SPX reset. Delete the namespace,
then the cluster:

```bash
kubectl --kubeconfig /tmp/partition-demo.kubeconfig delete namespace amd-demo-partition-allocation
KUBECONFIG=/tmp/partition-demo.kubeconfig kind delete cluster --name amd-partition-demo
```

Never change a live cluster's partition mode while claims exist.

## Published inputs

| Component | Version |
| --- | --- |
| Chart (dashboards on by default) | 0.1.0 |
| kind node / Kubernetes | 0.1.0 / v1.37.0 |
| Mock node agent image | v0.1.0 |
| AMD exporter runtime | v1.5.2-fake.1 |

The versions are independent: a chart configuration change does not need a
new node image. `setup.sh` uses the pins in
[`scripts/release.env`](../scripts/release.env). [Testing](../docs/contributing/testing.md#presenter-demos-and-default-dashboards)
covers how the presentation is checked in CI, and the [telemetry
guide](../docs/guides/telemetry.md) its limits.
