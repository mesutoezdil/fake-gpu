# Testing

The checks live under `test/`, grouped by what they need to run: nothing,
Helm, a container runtime, or a kind cluster with the mock installed. Most run
in CI on every pull request; this page lists the commands to run them locally,
the conditions each one assumes, and which workflow runs it.

## Layout

| Path | Holds | Needs |
| --- | --- | --- |
| `test/lib` | Shared helpers (`common.sh`, `e2e.py`) and their unit tests (`test_e2e.py`). | Nothing beyond Python 3. |
| `test/chart` | Render-only chart contracts: `dra.sh`, `demo.sh`, `driver_mock.sh`, all run by `run.sh`. | Helm and Python 3, no cluster. |
| `test/container/<area>` | Checks that build and run containers: `docker` (single-machine compose mode), `hipshim`, `hipcpu`, `smi_abi` (AMD SMI ABI), `dra_discovery` (AMD's discovery code). | Docker or Podman. `dra_discovery` also needs git and Go 1.24 with user namespaces or sudo on Linux. |
| `test/e2e/<area>` | End-to-end checks per area: `dra`, `driver`, `telemetry`, `demo`, `profile`. | A kind cluster with the mock installed. `test/e2e/driver/native.py` needs only a container runtime and host CUSE. |

## Without a cluster

The `lint` workflow runs these checks:

```bash
git ls-files '*.sh' | xargs shellcheck -x
ruff check .                                   # CI pins ruff 0.16.9
python3 -m unittest discover -s test/lib -v
test/chart/run.sh                              # dra.sh, demo.sh, driver_mock.sh
```

Go tests and the link check:

```bash
go test -race ./...
scripts/check-links.py
```

The Go tests compare all seven YAML profiles with rendered KFD names, SIMD
counts, GFX targets, vendor/device IDs, render minors, and memory sizes. They
verify the DMI UUID mount destination survives profile switches; native AMD64
kind needs this path when a consumer mounts the mock over `/sys`. They also
exercise API fault/recovery operations and repeated MI300X DPX/CPX/QPX/SPX
transitions, checking unique identities, physical PCI addresses, memory
division, restoration, and rejection of invalid modes.

`scripts/check-links.py` checks relative links and anchors in every tracked
Markdown file the way GitHub renders them.

The chart contracts:

- `test/chart/dra.sh` rejects configurations that enable two allocators,
  point at the wrong sysfs root, or place the mock and driver on different
  nodes.
- `test/chart/demo.sh` covers default/custom host ports, both dashboard
  switches, external-monitoring mode, invalid configuration rejection, and
  consistency between demo manifests and existing tested assets.
- `test/chart/driver_mock.sh` checks that the CUSE device backend is opt-in:
  the default chart never asks for it.

The container checks:

```bash
test/container/dra_discovery/run.sh
PARTITION_MODE=DPX test/container/dra_discovery/run.sh mi300x
test/container/smi_abi/run.sh # Podman; CONTAINER_RUNTIME=docker also supported
test/container/docker/run.sh
test/container/hipshim/run.sh
test/container/hipshim/live.sh
test/container/hipcpu/run.sh
```

- `dra_discovery/run.sh` runs AMD's unchanged discovery package against the
  sysfs tree rendered for each profile and validates all seven profiles / 60
  GPUs. Pass a profile name to run one; `DRA_REF` selects the AMD driver ref
  (default `v1.0.0`). On Linux it needs git, Go 1.24, and unprivileged user
  namespaces or sudo, because the rendered tree is bind-mounted over `/sys`
  inside a private mount namespace. On macOS it runs the same check in the
  container runtime's Linux VM (`CONTAINER_RUNTIME`, default Podman). The
  script passes the selected `PARTITION_MODE` explicitly into the probe inside
  its private mount namespace, including the Linux `sudo` fallback, so
  rendering and comparison use the same partition configuration.
- `smi_abi/run.sh` checks the mock AMD SMI library's enumeration, buffers,
  units, ECC, graphics clocks, unsupported APIs and concurrent readers.
  `ABI_PLATFORM` selects the container platform (default `linux/amd64`).
- `docker/run.sh` runs the single-machine mode end to end with Docker Compose:
  the agent in one container, a second container reading its state. Override
  the command with `COMPOSE`, for example `COMPOSE="sudo docker-compose"`.
- `hipshim/run.sh` builds `libfakehip` and runs its contract test in a compiler
  container. `hipshim/live.sh` has a HIP client in its own container allocate
  device memory, checks the agent reports it as used VRAM, and checks killing
  the client releases it.
- `hipcpu/run.sh` runs a real HIP kernel on the CPU through the HIP CPU Runtime
  and checks the mock card reports the load, then returns to idle.

`docker/run.sh`, `hipshim/live.sh` and `hipcpu/run.sh` source
`test/lib/common.sh`; `hipshim/live.sh` and `hipcpu/run.sh` take the container
command from `DOCKER` (default `docker`).

## On a cluster

Release 0.1.0 targets Kubernetes 1.37. Use kind v0.33.0 or newer and kubectl
v1.37. Older kind versions can generate kubeadm APIs that Kubernetes 1.37
rejects before any GPU test runs. Chart 0.1.0 uses mock image v0.1.0 and node
image 0.1.0; the node image still runs Kubernetes 1.37.

Set `KUBECONFIG` to the intended cluster before running any check. Run each
suite on a dedicated test cluster without other GPU consumers, and do not run
competing fault-injection suites simultaneously.

Scope: the virtual-partition tests cover the dashboard API's display state; the
same-GPU allocation suite separately covers fixed MI300X DPX/NPS2 topology. The
API does not increase Kubernetes capacity when CPX is selected. Fault tests use
an explicit device-plugin restart; they do not promise continuous device-health
detection without rediscovery. The telemetry suite validates the real AMD
exporter. The real amd-smi CLI, CVS/RVS, node reboot/failure and multi-node
placement remain separate consumer-validation tasks. GPU computation is outside
the mock's control-plane scope.

### DRA

See the [DRA guide](../guides/dra.md#tests) for the case-by-case assertions.
Against the driver installed by the published chart:

```bash
INSTALL_DRIVER=0 DRA_NS=amd-mock test/e2e/dra/run.sh
python3 test/e2e/dra/constraints.py
DRA_NS=amd-mock python3 test/e2e/dra/lifecycle.py
```

- `run.sh` checks ResourceSlice publication and that a pod claiming a GPU
  starts with its device nodes. It needs kubectl, Helm 3 or 4, git and
  Python 3, and the mock installed without its bundled device plugin. Without
  `INSTALL_DRIVER=0` it installs AMD's driver itself. Other variables: `KEEP=1` leaves the driver and workloads, `DRA_REF`
  (default `v1.0.0`), `DRA_IMAGE`, `DRA_IMAGE_TAG`, `EXPECTED_GPUS` (default
  8), `TEST_NS` (default `amd-dra-test`), `MOCK_SYS` (default
  `/var/lib/fake-gpu/sys`). Selector scenarios require MI300X.
- `constraints.py` covers capacity, init/main sharing and PCIe-root
  constraints. It requires two free devices sharing a PCIe root on one healthy
  mock node.
- `lifecycle.py` requires a dedicated single-node cluster with at least two
  mock GPUs and the whole pool free at the start: delete the demo pod and
  earlier test namespaces first.

Each check creates a separate unused test namespace and cleans up its own
workloads unless `KEEP=1` is set. CI runs the basic and lifecycle suites, plus
`python3 test/e2e/dra/constraints.py`, against both the source mock with AMD's
official driver and the published chart/images. These allocators run in
separate clusters.

#### Same-GPU partition allocation

On a dedicated fixed MI300X DPX/NPS2 cluster, run:

```bash
python3 test/e2e/dra/partitions.py
```

The `same-gpu-partition-allocation` workflow runs source and published-image
paths. It checks sixteen partition devices across eight parents, capacities,
same-parent allocations, per-container device visibility, exhaustion, CDI
cleanup, released-device reuse, sibling continuity, and driver pod replacement.
The final two-container pod remains Running; delete its namespace explicitly.
See the [allocation guide](../guides/partition-allocation.md) and
[demo](../../demo/partition-allocation/README.md).

This suite proves allocation/injection through real layers, not hardware
compute or memory isolation. Per-partition exporter attribution and physical
fault propagation remain outside this allocation test.

### Driver

The optional CUSE/library backend: see the
[driver backend guide](../guides/driver-mock.md) for native device-open/libdrm
checks and the compatibility matrix. The existing default-backend tests remain
required; optional-backend results do not substitute for them.

Native checks, with no cluster, on a Linux host with CUSE loaded:

```bash
sudo modprobe cuse
docker build -f deploy/driver-mock/Dockerfile -t local/fake-gpu-driver:test .
CONTAINER_RUNTIME=docker DRIVER_IMAGE=local/fake-gpu-driver:test \
  python3 -u test/e2e/driver/native.py
```

On a cluster installed with `--set driverMock.enabled=true` and the driver
image:

```bash
DRIVER_IMAGE=ghcr.io/mesutoezdil/fake-gpu-driver:v0.1.0 \
  python3 -u test/e2e/driver/allocated.py
python3 -u test/e2e/driver/restart.py
python3 -u test/e2e/driver/allocated.py
PROVIDER_RESTART_MODE=graceful python3 -u test/e2e/driver/restart.py
```

`allocated.py` proves native opens and libdrm queries inside allocated Pods.
Set `ALLOCATOR=device-plugin` on a device-plugin installation, and
`PARTITION_MODE=DPX ALLOCATOR=dra` on a DPX installation. `restart.py` kills
the real provider process and requires an actual container restart
(`PROVIDER_RESTART_MODE`, default `unclean`; `graceful` restarts it cleanly).

### Telemetry

See the [telemetry guide](../guides/telemetry.md) for monitoring installation
and ports. With Prometheus on localhost:9090, Grafana on localhost:3000 and the
mock on localhost:8080:

```bash
./test/container/smi_abi/run.sh # Podman; CONTAINER_RUNTIME=docker also supported
python3 test/e2e/telemetry/pipeline.py
ALLOCATOR=dra python3 test/e2e/telemetry/attribution.py
# Or ALLOCATOR=device-plugin on that installation.
```

`PROMETHEUS_URL`, `GRAFANA_URL`, `GRAFANA_ADMIN_PASSWORD` (default `amdmock`)
and `MOCK_URL` override those addresses.

The ABI suite checks enumeration, buffers, units, ECC, graphics clocks,
unsupported APIs and concurrent readers. The live suite checks GPU identity,
VRAM units, ServiceMonitor health, every per-GPU dashboard action (Overheat,
Busy, Idle, Crash, Recover and ECC), and every Grafana panel query. It restores
GPU 0 after fault testing. Run on a dedicated cluster with GPU 0 healthy and
no concurrent fault actions. Healthy readings fluctuate under dynamic
simulation.

The attribution suite creates and cleans up a GPU consumer and checks its
pod/namespace/container labels from the real kubelet pod-resources socket.
`telemetry-e2e` CI covers AMD64 and ARM64 runners with both allocators and
repeats telemetry tests after exporter restart. ARM64 runs AMD's unchanged
x86 collector under QEMU; the surrounding cluster and node agent run natively.
Local validation passed on ARM64 Podman; CI results are the evidence for the
native AMD64 path. The tests invoke dashboard endpoints, not browser clicks.

### Demo

#### Tiny LLM and virtual partition demos

On the default published quick start, run:

```bash
MODE=dra python3 test/e2e/demo/workloads.py
```

For the alternative device-plugin installation (DRA disabled), run:

```bash
MODE=device-plugin python3 test/e2e/demo/workloads.py
```

Run on a dedicated MI300X cluster with no other GPU consumers. The script
creates and deletes the named default-namespace demo deployments and restores
SPX mode. Both paths check one-GPU injection and scripted LLM output, then
DPX/CPX/QPX/SPX transitions, virtual identity/memory, and unchanged physical
allocator capacity. The device-plugin path also checks four simultaneous
replicas receive four distinct physical GPUs. DRA multi-GPU requests and
shared explicit claims are covered by the basic/lifecycle suites.

These demos execute scripted Python output; they do not download model weights
or perform LLM inference. Virtual partition entries are not independently
schedulable slices. AMD compute partitions and NVIDIA MIG hardware isolation
are not implemented by this mock.

#### Automatic dashboard access

The quick-start kind configuration maps host port 8080 to the chart's
NodePort 30080. After Helm installation, no port-forward process is needed:

```bash
python3 test/e2e/demo/dashboard.py
```

This checks dashboard HTML, GPU identity/count, profile API, and metrics through
the host URL. Use `DASHBOARD_URL=http://127.0.0.1:9090` when testing a custom
kind host port; `EXPECTED_GPU_COUNT` (default 8) sets the expected count.
Change that mapping before creating the cluster; container port mappings
cannot be added to an existing kind node by a Helm upgrade. The `dra-e2e`,
`telemetry-e2e`, `demo-e2e`, `driver-backend` and `driver-backend-published`
workflows run this check.

#### Presenter demos and default dashboards

`demo/` contains runnable LLM, DRA, virtual partitioning, fault injection,
multi-GPU, allocation/release, telemetry, profile and fixed same-GPU partition
presentations. See its [presenter guide](../../demo/README.md). Chart 0.1.0
installs built-in Prometheus/Grafana and the real exporter by default; the
shared kind config exposes Grafana at localhost:3000 and the mock dashboard at
localhost:8080.

```bash
test/chart/demo.sh
python3 test/e2e/demo/grafana.py
python3 test/e2e/demo/presenter.py
python3 test/e2e/demo/dashboards_disabled.py # after disabling both dashboards
```

The chart checks are listed under [Without a cluster](#without-a-cluster). The
Grafana check verifies host access without forwarding, dashboard provisioning,
real AMD GPU count and every panel query. The published DRA CI path runs it
from a fresh cluster. `presenter.py` exercises the presenter commands on a
dedicated freshly installed demo cluster (`DEMO_CONTEXT`, default
`kind-amd-mock`; `DEMO_CONFIG`, default `demo/config.yaml`; `ALLOCATOR`).
`dashboards_disabled.py` ensures disabled dashboards have no host exposure or
Grafana workload.

`demo-e2e` CI runs the presenter scripts on fresh published-image clusters
with both allocators. It covers the LLM, multi-GPU consumers, claim release,
fault recovery and Grafana, and verifies disabling/re-enabling both dashboards.
Do not run competing fault-injection suites simultaneously. The demos leave
workloads for inspection; `demo/run.py cleanup` removes labelled namespaces.

#### Spur batch scheduling

The optional `spur-demo` workflow runs unchanged Spur 0.14.0 with the published
mock node and device-plugin chart on a dedicated cluster. Run locally with:

```bash
demo/spur/setup.sh
python3 -u demo/spur/run.py
```

It verifies whole-pool allocation, queueing without a competing Pod, character
device injection, completion and subsequent GPU reuse. The operator launch
endpoint requires controller-signed credentials. No HIP computation, DRA
allocation or distributed workload is asserted. See the
[Spur demo](../../demo/spur/README.md).

### Profile

Physical GPU device-plugin tests. On a dedicated single-node Kubernetes 1.37
cluster, install the mock chart with its device plugin enabled and DRA
disabled. Set the profile at install time (`--set gpu.profile=mi250x`, for
example), then run:

```bash
PROFILE=mi250x python3 test/e2e/profile/scheduling.py
```

The script requires kubectl and Python 3, creates an unused test namespace
(`PROFILE_TEST_NS`, default `amd-profile-test`), and deletes its workloads
unless `KEEP=1` is set. It mutates GPU 0's mock health and restarts the device
plugin for explicit rediscovery. Run it only on a dedicated test cluster
without other GPU consumers.

Each profile checks:

1. API model, memory, physical identities, and exact allocatable count.
2. One-GPU scheduling and exactly one render character device in the consumer.
3. Full-pool exhaustion: an additional request remains pending.
4. Release: the pending consumer starts after capacity becomes available.
5. Crash: RAS state and driver bindings change; rediscovery reduces capacity.
6. Recovery: rediscovery restores capacity.
7. Overheat reaches the sysfs temperature sensor.
8. ECC injection and recovery reach the sysfs error counter.

`profile-e2e` runs these checks independently for MI210, MI250X, MI300A,
MI300X, MI325X, MI350X, and MI355X on native AMD64 GitHub runners. Each
profile gets a fresh cluster; the suite does not assume live profile changes
recreate device nodes. Failure artifacts include events, pod status,
allocatable resources, agent/device-plugin logs, and kind logs.

## CI workflows

| Workflow | Trigger | Runs |
| --- | --- | --- |
| `lint.yaml` | Every PR, push to main, manual | Shebang scripts are executable, `shellcheck -x` on every `*.sh`, ruff 0.16.9, `py_compile` on every Python file, `python -m unittest discover -s test/lib -v`, `test/chart/run.sh`. |
| `unit-and-docker.yaml` | Every PR, push to main, manual | `gofmt`, `go vet ./...`, `go test -race -count=1 ./...`, root-only device-node tests (`cmd/node-agent`, `pkg/mockdriver`), `test/container/docker/run.sh`, `test/container/hipshim/run.sh` and `live.sh`, `test/container/hipcpu/run.sh`. |
| `dra-discovery.yaml` | Every PR, push to main | `test/chart/dra.sh`, `go test ./pkg/gpu/kfd`, `test/container/dra_discovery/run.sh` for all profiles and with `PARTITION_MODE=DPX` for MI300X. |
| `dra-e2e.yaml` | Every PR, push to main, manual | Matrix `source-upstream` (source mock with AMD's driver v1.0.0 on `kindest/node:v1.37.0`) and `published` (chart 0.1.0 on the mock node image). KFD unclean-shutdown regression, `test/e2e/demo/dashboard.py`, `test/e2e/demo/grafana.py` (published only), `test/e2e/dra/run.sh` with `INSTALL_DRIVER=0`, `lifecycle.py`, `constraints.py`, `MODE=dra test/e2e/demo/workloads.py`. |
| `profile-e2e.yaml` | Every PR, push to main, manual | One fresh cluster per profile (all seven), source image with the device plugin: `test/e2e/profile/scheduling.py`; MI300X also runs `MODE=device-plugin test/e2e/demo/workloads.py`. |
| `telemetry-e2e.yaml` | Every PR, push to main, manual | AMD64 and ARM64 runners, both allocators. `go test -race ./...`, `test/container/smi_abi/run.sh`, then this checkout's mock and collector with kube-prometheus-stack 92.0.0: `test/e2e/telemetry/pipeline.py`, `attribution.py`, exporter restart, `pipeline.py` again, `test/e2e/demo/dashboard.py`. |
| `demo-e2e.yaml` | Every PR, push to main, manual | Both allocators on published images via `demo/setup.sh`: `test/chart/demo.sh`, `grafana.py`, `presenter.py`, then dashboards disabled (`dashboards_disabled.py`) and restored (`dashboard.py`, `grafana.py`). |
| `partition-allocation.yaml` (`same-gpu-partition-allocation`) | Every PR, push to main, manual | Source and published paths with `gpu.partition=DPX`: `test/e2e/dra/partitions.py`. |
| `driver-backend.yaml` | PRs and pushes to main touching the driver, chart or test paths; manual | `native` (AMD64 and ARM64): `go test -race ./...`, `test/chart/run.sh`, `smi_abi/run.sh`, `test/e2e/driver/native.py`. `profiles`: all seven profiles with the CUSE backend and device plugin, `scheduling.py`. `integration` (`dra`, `device-plugin`, `partition`): DRA suites, `allocated.py` and unclean/graceful `restart.py`, demo checks, telemetry with exporter restart; `partition` runs `partitions.py` and `allocated.py` with DPX. |
| `driver-backend-published.yaml` | Manual | Published chart 0.1.0 and images, no build, on AMD64 and ARM64 with the `nodes` and `cuse` backends: `allocated.py`/`restart.py` cycles (CUSE), graceful `restart.py` (nodes), `MODE=dra workloads.py`, `dashboard.py`, `grafana.py`. |
| `spur-demo.yaml` | Manual | `demo/spur/setup.sh` and `demo/spur/run.py`. |
| `docs-site.yaml` | PRs and pushes to main touching docs, demo, README or the docs build; manual | `scripts/docs/build.py`; deploys GitHub Pages from main when the repository is public. |
| `release-images.yaml` | Manual | `scripts/build-images.sh --push`, `scripts/build-telemetry-images.sh --push`, `scripts/build-driver-mock-images.sh --push`. |

## Helpers

`test/lib/common.sh` is sourced by the bash checks
(`source "$ROOT/test/lib/common.sh"`). Every helper fails loudly, so a check
never passes because a command was skipped, returned nothing, or matched the
wrong thing:

- `pass`, `fail MESSAGE [DETAIL...]` (prints and exits 1).
- `has FILE TEXT` / `lacks FILE TEXT`: fixed-string checks on a rendered file.
- `expect OUTPUT TEXT LABEL`, `shows TEXT COMMAND...`.
- `eventually SECONDS COMMAND...`: rerun once a second until it succeeds.
- `is_int VALUE`.

`test/lib/e2e.py` holds the primitives shared by the Python end-to-end checks,
so every check gets the same timeouts, error messages and tolerance for errors
that only mean "not yet":

- `run`, `wait_for` (poll until truthy; transient errors count as "not yet"
  and the last one is reported on timeout), `stays`, `ok`, `summary`,
  `env_flag`.
- `Kubectl`: kubectl bound to an optional context and default namespace, with
  `get`, `items`, `apply`, `exec`, `allocation`, `rollout_restart`,
  `diagnostics` and more.
- `scratch_namespace`: creates an unused namespace, refuses one that exists,
  dumps its state on failure and deletes it afterwards unless kept.
- `file_exists`, `device_probe`.
- `http`, `http_json`, `basic_auth`, `Prometheus`, `Grafana`, and `MockAPI`
  (the node agent API through `kubectl exec`, with no port-forward).

One kubectl call times out after `E2E_COMMAND_TIMEOUT` seconds (default 300);
HTTP requests after 15 seconds. `test/lib/test_e2e.py` unit-tests these helpers
and the small deploy filters.

A new check at `test/e2e/<area>/<name>.py` imports the module after putting
`test/lib` on `sys.path`:

```python
import sys
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parents[2] / 'lib'))
import e2e
```

A new bash check sources `common.sh` relative to the repository root, as
`test/chart/dra.sh` does.

## Chart refactors

`scripts/chart-snapshot.py CHART` renders the chart under many value sets
(defaults, device plugin, monitoring and dashboard switches, ServiceMonitors,
driver mock, DPX, custom roots, image overrides, a renamed release and every
profile) and prints a normalized JSON snapshot. Resources are keyed by kind,
namespace and name with template source comments dropped, so two snapshots are
equal exactly when the charts render the same objects, whatever template files
they come from. Invalid value sets must fail to render; their error message is
part of the snapshot. It needs Helm and PyYAML.

To prove a template refactor renders identical objects, snapshot the chart
before and after and compare:

```bash
scripts/chart-snapshot.py charts/fake-gpu > /tmp/before.json
# apply the refactor
scripts/chart-snapshot.py charts/fake-gpu > /tmp/after.json
cmp /tmp/before.json /tmp/after.json
```
