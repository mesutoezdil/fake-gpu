# fake-gpu

AMD Instinct GPUs for machines that have none. fake-gpu renders the sysfs
tree, device nodes, AMD SMI library and telemetry that AMD's Kubernetes
stack expects, so schedulers, device plugins, exporters and dashboards run
against eight "MI300X" cards on a laptop or a CI runner.

It fakes what software reads about a GPU, never what a GPU computes. Use it
to test discovery, allocation, monitoring and fault handling. Use real
hardware for anything that runs a kernel, moves data or measures speed.

The idea follows NVIDIA's [Mokka](https://github.com/NVIDIA/k8s-test-infra),
which does the same for NVIDIA devices.

![fake-gpu dashboard](docs/img/dashboard.png)

## Good fit, bad fit

| Works | Needs real hardware |
|---|---|
| Device plugin and DRA scheduling, claims, CDI injection | HIP or ROCm kernels |
| Software that reads AMD SMI, `amd-smi` or the AMD metrics exporter | VRAM allocation, DMA |
| Dashboards, alerts and remediation driven by GPU faults | KFD ioctls |
| Code that reads PCI, NUMA, xGMI or partition topology | SR-IOV passthrough, performance numbers |

## Quick start

Needs kind v0.33+, kubectl, Helm and Docker. Run from this checkout:

```bash
kind create cluster --name fake-gpu --image kindest/node:v1.37.0 \
    --config deployments/kind-node/kind-config.yaml

docker build -t fake-gpu:dev .
kind load docker-image fake-gpu:dev --name fake-gpu

helm install fake-gpu deployments/helm/fake-gpu -n amd-mock --create-namespace \
    --set image.repository=fake-gpu --set image.tag=dev --set image.pullPolicy=Never \
    --set dra.image.repository=docker.io/rocm/k8s-gpu-dra-driver --set dra.image.tag=v1.0.0 \
    --set monitoring.enabled=false --set metricsExporter.enabled=false
```

Within about a minute the node offers eight mock MI300X GPUs through AMD's DRA
driver (`kubectl get resourceslices`), and the dashboard is at
[localhost:8080](http://localhost:8080). Request a GPU with the
[DRA guide](docs/guides/dra.md), or switch to `amd.com/gpu` scheduling with the
[device-plugin guide](docs/guides/device-plugin.md).

Clean up with `kind delete cluster --name fake-gpu`.

The Prometheus and Grafana stack with AMD's real exporter needs two more
images; the [telemetry guide](docs/guides/telemetry.md) builds them.

## Without Kubernetes

One unprivileged container runs the agent, the dashboard and the mock
`amd-smi`. It changes nothing on the host:

```bash
docker compose -f deployments/docker/compose.yaml up -d --build
docker compose -f deployments/docker/compose.yaml exec fake-gpu amd-smi monitor
```

Other containers read the same GPUs by mounting the `state` volume at
`/var/lib/fake-gpu`: the sysfs tree is under `sys/`, the SMI snapshots under
`smi/`. Pick another card with `GPU_PROFILE=mi325x`. There is no device plugin
or CDI in this mode, so it suits tools that read telemetry or sysfs, not ones
that open `/dev/kfd`.

## Fault injection

Every fault is one POST, from the dashboard buttons, `curl` or the bundled
`fakegpuctl`:

```bash
fakegpuctl list
fakegpuctl throttle -gpu 1
fakegpuctl busy -gpu 0 util=95
fakegpuctl recover -gpu 1
curl -X POST 'http://localhost:8080/api/actions/throttle?gpu=1'   # same thing
```

| Action | What changes |
|---|---|
| `crash` | Device nodes and driver binding disappear, power and clocks drop to zero |
| `overheat` | Edge temperature pinned at 105 C, the GPU throttles itself |
| `ecc-error` | Uncorrectable ECC count rises, `ras/fatal_error` is set |
| `throttle` | Power budget and clocks halved, reported as a thermal violation |
| `retire-page` | Pages listed in `ras/gpu_vram_bad_pages` and in AMD SMI bad-page info |
| `xgmi-down` | `xgmi_error` set, xGMI `io_links` disabled on both ends |
| `busy?util=N` | Holds N% utilization; power and temperature follow with thermal lag |
| `idle`, `recover` | Back to the profile defaults |

Faults reach sysfs, the mock AMD SMI library, `amd-smi`, and Prometheus. The
[fault demo](demo/faults/README.md) lists which metric carries each one.

With `--set kernelLog.enabled=true` each transition also writes a line such as
`amdgpu 0000:05:00.0: fake-gpu: 2 VRAM page(s) retired` to the node's kernel
log. It is off by default because kind nodes share the host kernel, so the
lines show up in the host's `dmesg`. Outside Kubernetes, point
`node-agent --kernel-log` at a file instead.

## Telemetry

Utilization is the input. Power follows utilization, temperature drifts toward
the steady state for that power, and a GPU at 100 C throttles. AMD's unmodified
GPU Agent and metrics exporter read these values through the mock
`libamd_smi.so` (ABI 27), so Prometheus and Grafana show real exporter metric
names. Retired pages and xGMI link state have no exporter metric; the bundled
Prometheus scrapes them from the agent as `gpu_retired_pages` and
`gpu_xgmi_link_up`.

## amd-smi and rocm-smi

The image ships a mock `amd-smi` with `list`, `static`, `metric`, `monitor`,
`bad-pages` and `version`, each taking `--json` and `--gpu N`. `rocm-smi` prints
the `monitor` table. The layout follows the real tool but covers only the
fields the mock tracks, so do not parse it as a byte-exact replacement.

## GPU profiles

Set with `--set gpu.profile=<name>`. Profiles are plain YAML in `profiles/`;
[docs/profiles.md](docs/profiles.md) describes the fields.

| Profile | GPU | Memory | Architecture | TDP |
|---|---|---|---|---|
| `mi210` | AMD Instinct MI210 | 64 GB HBM2e | CDNA 2 | 300 W |
| `mi250x` | AMD Instinct MI250X | 64 GB HBM2e per GCD (16 devices) | CDNA 2 | 500 W |
| `mi300a` | AMD Instinct MI300A | 128 GB HBM3 | CDNA 3 | 550 W |
| `mi300x` (default) | AMD Instinct MI300X | 192 GB HBM3 | CDNA 3 | 750 W |
| `mi325x` | AMD Instinct MI325X | 256 GB HBM3E | CDNA 3 | 1000 W |
| `mi350x` | AMD Instinct MI350X | 288 GB HBM3E | CDNA 4 | 1000 W |
| `mi355x` | AMD Instinct MI355X | 288 GB HBM3E | CDNA 4 | 1400 W |

### Capture a profile from real hardware

On a machine with AMD GPUs and the `amdgpu` driver, `capture-profile` reads
sysfs and writes a profile for the card with the most VRAM:

```bash
go run ./cmd/capture-profile --name "AMD Radeon RX 9070 XT" -o rx9070xt.yaml
node-agent --config rx9070xt.yaml --profiles profiles
```

It needs no root. Values it cannot read are listed as `# NOTE` lines at the top
of the file; a runtime-suspended GPU hides its power caps and sensors, so
capture while the card is active. Per-card unique IDs are replaced unless you
pass `--keep-ids`. A profile holds one model, so other cards are skipped and
named with the `--gpu` flag that selects them.

## Partitioning

`--set gpu.partition=DPX` publishes two schedulable 96 GB slices per MI300X;
see [same-GPU partition allocation](docs/guides/partition-allocation.md). The
dashboard API also models SPX, DPX, QPX and CPX as display state only: CPX
shows 64 entries for 8 cards without changing Kubernetes capacity.

## What gets rendered

| Surface | Content |
|---|---|
| KFD topology | `/sys/class/kfd/kfd/topology/nodes/*`, including xGMI `io_links` |
| Device nodes | `/dev/kfd`, `/dev/dri/renderD*`, `/dev/dri/card*` |
| PCI and DRM sysfs | IDs, NUMA nodes, xGMI hive, `mem_info_*`, link speed, `pp_dpm_*`, hwmon sensors and power caps, RAS files |
| Driver module | `/sys/module/amdgpu/` version, state and bindings |
| AMD SMI | `libamd_smi.so` for AMD's GPU Agent, plus the `amd-smi` CLI |
| CDI | Specs in the format `amd-ctk` writes |
| HTTP | Dashboard, control API and `/metrics` on port 8080 |

[How it works](docs/how-it-works.md) and the
[architecture notes](docs/architecture.md) explain the data flow.
[Simulation depth by area](docs/simulation-depth.md) says what each surface
proves and what it does not.

## Tested consumers

| Consumer | What is covered |
|---|---|
| AMD K8s Device Plugin | Discovery and `amd.com/gpu` scheduling on all seven profiles |
| AMD GPU DRA Driver v1.0.0 | ResourceSlices, claims, selectors, CDI injection, lifecycle, restarts |
| AMD Device Metrics Exporter v1.5.2 | Unmodified collector reading the mock SMI library |
| Prometheus and Grafana | Exporter metrics and every dashboard fault action |
| ROCm Spur | Job submission, queueing and GPU release |

## Tests

```bash
go test ./...            # unit and contract tests
tests/docker/run.sh      # single-machine mode, needs docker compose
```

Cluster suites and their prerequisites are in the
[testing guide](docs/guides/testing.md).

## Images

Nothing is published yet. `scripts/build-images.sh` builds the mock, the kind
node image and the DRA driver image for AMD64 and ARM64 from the commits pinned
in `scripts/release.env`; `scripts/build-telemetry-images.sh` adds the exporter
runtime. Both take `--push`.

| Component | Version |
|---|---|
| Kubernetes | v1.37.0 |
| AMD SMI library ABI | 27.0.0 |
| [Device Metrics Exporter](https://github.com/ROCm/device-metrics-exporter) | v1.5.2 |
| [K8s Device Plugin](https://github.com/ROCm/k8s-device-plugin) | v1.31.0.11 |
| [GPU DRA Driver](https://github.com/ROCm/k8s-gpu-dra-driver) | v1.0.0 |
| [Container Toolkit](https://github.com/ROCm/container-toolkit) | v1.3.0 |

## License

GNU Affero General Public License v3.0, see [LICENSE](LICENSE). Parts of the
code were first released by third parties under the Apache License 2.0 and stay
available under it; [NOTICE](NOTICE) has the details.
