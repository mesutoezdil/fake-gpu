# Documentation guide

A searchable Material for MkDocs site now brings these guides and demo READMEs
together. See [building and publishing the site](site/maintaining.md) for local
preview and GitHub Pages setup.

The current published setup uses chart **0.1.0**, mock image **v0.1.0**,
node image **0.1.0 / Kubernetes v1.37.0**, AMD DRA **v1.0.0-fake.1** and
exporter **v1.5.2-fake.1**. These components have independent versions.
The root README contains the short installation path; these guides explain
behavior, validation and limits.

| Guide | What it covers |
| --- | --- |
| [How it works](how-it-works.md) | Mock interfaces, allocation and telemetry paths |
| [Architecture](architecture.md) | ASCII component diagrams and fault flow |
| [GPU profiles](profiles.md) | Model inventory, capacities and fidelity limits |
| [Native device/library backend](guides/driver-mock.md) | Optional CUSE devices, libdrm/AMD SMI queries, published images and fidelity limits |
| [DRA](guides/dra.md) | ResourceClaims, allocation, CDI and lifecycle checks |
| [Same-GPU partition allocation](guides/partition-allocation.md) | Fixed MI300X DPX/NPS2, two containers sharing a physical parent, each layer's responsibilities |
| [Device plugin](guides/device-plugin.md) | Alternative whole-GPU allocator setup |
| [Telemetry](guides/telemetry.md) | Mock AMD SMI, real exporter, Prometheus and Grafana |
| [Spur scheduling](../demo/spur/README.md) | Unchanged upstream batch scheduler, queueing, GPU injection, completion and release |
| [Testing](guides/testing.md) | Local contracts and complete CI suites |
| [Demo room](../demo/README.md) | Presentations, commands and cleanup |

Default SPX supports the original eight presentations. Fixed DPX/NPS2 requires
a separate fresh startup configuration and provides the ninth, same-GPU
partition-allocation presentation. Virtual dashboard partition changes do not
create schedulable slices.

