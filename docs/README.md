# Documentation

The same pages are published as a searchable site; see
[the documentation site](contributing/docs-site.md) for local preview and
publishing. The root [README](../README.md) has the short installation path.
These pages explain behavior, validation and limits.

The current published setup uses chart **0.1.0**, mock image **v0.1.0**,
node image **0.1.0 / Kubernetes v1.37.0**, AMD DRA **v1.0.0-fake.1** and
exporter **v1.5.2-fake.1**. These components have independent versions.

## Layout

| Directory | Contents |
| --- | --- |
| `docs/` | [Overview](index.md), [quick start](getting-started.md), [choose a demo](demos.md), [troubleshooting](troubleshooting.md) |
| `docs/guides/` | Task guides, all with the same sections: Overview, Requirements, Steps, Verify, How it works, Troubleshooting, Clean up, Related |
| `docs/reference/` | Architecture, mock interfaces, GPU profiles and simulation depth |
| `docs/contributing/` | Testing and the documentation site |
| `../demo/` | Presenter walkthroughs, each runnable on its own |

## Guides

| Guide | What it covers |
| --- | --- |
| [Overview](guides/index.md) | Which guide or demo fits which question |
| [AMD DRA driver](guides/dra.md) | ResourceSlices, ResourceClaims, allocation, CDI and lifecycle checks |
| [AMD device plugin](guides/device-plugin.md) | Alternative whole-GPU allocator through `amd.com/gpu` |
| [Same-GPU partition allocation](guides/partition-allocation.md) | Fixed MI300X DPX/NPS2, two containers sharing a physical parent, each layer's responsibilities |
| [Optional CUSE and libdrm](guides/driver-mock.md) | Openable devices, libdrm and AMD SMI queries, published images and fidelity limits |
| [OpenShift Local](guides/openshift-local.md) | Tiny LLM on CRC under the `restricted-v2` SCC |
| [Telemetry](guides/telemetry.md) | Mock AMD SMI, the real exporter, Prometheus and Grafana |

## Reference

| Page | What it covers |
| --- | --- |
| [Architecture](reference/architecture.md) | ASCII component diagrams and fault flow |
| [How it works](reference/how-it-works.md) | Mock interfaces, allocation and telemetry paths |
| [GPU profiles](reference/profiles.md) | Model inventory, capacities and fidelity limits |
| [Simulation depth](reference/simulation-depth.md) | What each area simulates, what it does not, and which check proves it |

## Contributing

| Page | What it covers |
| --- | --- |
| [Testing](contributing/testing.md) | Test layout, local checks, cluster suites and CI workflows |
| [Documentation site](contributing/docs-site.md) | Building, previewing and publishing these pages |

Also see the [presenter guide](../demo/README.md) and the
[Spur scheduling demo](../demo/spur/README.md).

Default SPX supports the original eight presentations. Fixed DPX/NPS2 requires
a separate fresh startup configuration and provides the ninth, same-GPU
partition-allocation presentation. Virtual dashboard partition changes do not
create schedulable slices.
