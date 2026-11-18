# Simulation depth by area

What each mocked surface is good for, and where it stops. "Checked by" names
the test that fails if the surface breaks.

| Area | What is simulated | What is not | Checked by |
|---|---|---|---|
| KFD topology | Node properties, memory banks, PCIe and xGMI `io_links`, link flags that follow xGMI faults | KFD ioctls, queues, process state | `go test ./pkg/gpu/kfd`, `tests/dra/discovery-check.sh` |
| Device nodes | `/dev/kfd`, card and render character devices with the expected major and minor | Any working open, read or ioctl; the CUSE backend adds open and libdrm init only | `tests/profile-e2e.py`, `tests/driver-mock/` |
| PCI and DRM sysfs | IDs, NUMA node, root complex, product name, xGMI hive, `mem_info_*`, link speed and width, `pp_dpm_sclk` and `pp_dpm_mclk`, hwmon temperatures, clocks, power and power caps with the driver's labels, refreshed every second, RAS counters, retired page list | Writable attributes (setting a power cap or clock level has no effect), fan, voltage, firmware and PCI resource files; about 45 of the 121 attributes a Radeon RX 9070 XT exposes are rendered | `TestLiveTelemetryReachesSysfs`, `contracts_test.go` |
| Kubernetes allocation | AMD device plugin capacity and health, AMD DRA driver ResourceSlices, claims, CDI injection, fixed MI300X DPX slices | Runtime partition changes as schedulable devices, continuous health without rediscovery | `tests/profile-e2e.py`, `tests/dra/*.py` |
| AMD SMI library | The calls AMD GPU Agent v1.5.2 makes: identity, power, temperature, clocks, VRAM, ECC, bad pages, xGMI error status, thermal violations | Control calls, events, profiler counters; they return `NOT_SUPPORTED` | `tests/telemetry/abi.sh` |
| Metrics | AMD's unmodified exporter through Prometheus and Grafana; retired pages and xGMI link state from the agent | Exporter metrics for retired pages and xGMI state (v1.5.2 has none); health service | `tests/telemetry/e2e.py`, `tests/demo/presenter-e2e.py` |
| Telemetry model | Utilization sets power, power sets temperature with lag, throttling at 100 C or on request | Fan curves, per-rail power, timing that matches a physical card | `TestTelemetryFollowsLoad`, `TestBusyHoldsRequestedLoad` |
| Faults | Crash, overheat, ECC, throttle, page retirement, xGMI link down, with recovery | Fault timing, driver reset sequences, propagation between sibling partitions | `faults_test.go`, `tests/profile-e2e.py` |
| Kernel log | One line per fault transition, opt-in. ECC, xGMI, throttling and over-temperature use the amdgpu 7.0 driver's own message formats | Page retirement, device loss and recovery, which the driver has no single line for and which are tagged `fake-gpu` | `TestFaultTransitionsReachKernelLog` |
| CLI | `amd-smi` and `rocm-smi` subset over the same state | Byte-exact output of the real tools, write commands | `cmd/amd-smi/main_test.go` |
| Profiles | Seven Instinct models, plus capture from a real machine | Mixed models on one node | `TestCaptureRoundTripsRenderedProfile` |
| HIP memory | `libfakehip.so`: device enumeration, `HIP_VISIBLE_DEVICES`, allocation against the card's VRAM limit, copies, per-process usage reported as used VRAM and released on exit or crash | Kernel launch, streams, events, managed memory, loading binaries built against `libamdhip64` | `tests/hipshim/run.sh`, `tests/hipshim/live.sh`, `TestHIPClientMemoryCountsAsVRAM` |
| Compute | Nothing | HIP kernels, ROCm libraries, DMA, SR-IOV | |
