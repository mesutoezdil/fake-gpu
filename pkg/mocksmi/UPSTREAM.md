# AMD SMI mock: provenance

## Header

`amdsmi.h` is AMD's AMD SMI header, unchanged and under its own MIT notice. It
comes from ROCm/gpu-agent `release-v1.5.2`, commit
`4ce653089c09ae9be2503425d63d3e4452992813`, file
`sw/nic/third-party/rocm/amd_smi_lib/include/amd_smi/amdsmi.h`, and defines
AMD SMI ABI 27.0.

## Consumers

The collector binaries are AMD's, unchanged, from
`docker.io/rocm/device-metrics-exporter:v1.5.2` pinned to
`sha256:d4f2706cc42a526aeba36da082f07bbdd74be9b4d6988ae16d914fb474de9f75`.
GPU Agent loads `libamd_smi.so.27` and needs the GNU symbol version
`AMDSMI_1`; `version.map` provides both.

Exporter v1.4.1 was tried during development. Its GPU Agent does not read
ECC counters, so it cannot carry the dashboard's ECC fault through to
Prometheus, and it is not the supported image.

## What is implemented

`mock.c` implements the identity, enumeration and telemetry calls the pinned
collector and discovery clients make, from the agent's snapshots. Every other
function of the ABI (control, events, profiling, CPU and NIC) is a stub in
`unsupported.c` that returns `AMDSMI_STATUS_NOT_SUPPORTED`, so nothing
pretends to succeed without doing the work.

## Regenerating the stubs

After adding or removing a function in `mock.c`:

```bash
cc -E -P amdsmi.h > /tmp/amdsmi.i
./generate-stubs.py /tmp/amdsmi.i
```
