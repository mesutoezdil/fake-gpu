# Dashboard failure injection

The fault/telemetry demo below is for the SPX whole-GPU setup.
Physical fault propagation across sibling partitions is outside the
[same-GPU allocation proof](../partition-allocation/README.md).

Open the mock dashboard and Grafana side by side. Use the buttons directly,
or run the equivalent actions:

```bash
python3 demo/run.py faults --gpu 0 --action overheat
python3 demo/run.py faults --gpu 0 --action recover
python3 demo/run.py faults --gpu 0 --action ecc-error
python3 demo/run.py faults --gpu 0 --action recover
python3 demo/run.py faults --gpu 0 --action busy
python3 demo/run.py faults --gpu 0 --action idle
python3 demo/run.py faults --gpu 0 --action throttle
python3 demo/run.py faults --gpu 0 --action recover
python3 demo/run.py faults --gpu 0 --action retire-page
python3 demo/run.py faults --gpu 0 --action recover
python3 demo/run.py faults --gpu 0 --action xgmi-down
python3 demo/run.py faults --gpu 0 --action recover
python3 demo/run.py faults --gpu 0 --action crash
python3 demo/run.py faults --gpu 0 --action recover
```

Overheat and ECC buttons toggle; Recover explicitly resets healthy state.
Dashboard changes reach the same device state read by mock AMD SMI, real AMD
GPU Agent and real exporter. Grafana follows the collection/scrape interval.
Busy/idle also change activity, clocks, power and used VRAM.

Crash removes mock discovery bindings and host card/render nodes and zeroes
power/activity/clocks. It does not simulate an AMD SMI device-lost return or
kill an existing workload. DRA does not promise live health propagation or
claim revocation; device-plugin fault tests use explicit restart/rediscovery.
No automatic alert rules or remediation are installed. See the
[ASCII fault path](../../docs/architecture.md#dashboard-failures-two-paths-different-consequences).

## Reading the output

The action prints the changed GPU JSON immediately. Then inspect the same GPU
through the [layer-by-layer evidence commands](../EVIDENCE.md): mock snapshot,
real exporter output, Prometheus value, and Grafana graph. Keep one controlled
state until the next collection/scrape; toggling and recovering immediately
may remove the condition before Prometheus samples it.

| Action | Immediate mock evidence | Downstream AMD metric to watch |
| --- | --- | --- |
| Overheat | `temperature_c: 105` | `amd_gpu_edge_temperature` reaches 105 |
| ECC | `ecc_errors` increases | `amd_gpu_ecc_uncorrect_total` increases |
| Busy | Activity/VRAM/power increase | `amd_gpu_gfx_activity`, `amd_gpu_used_vram`, `amd_gpu_package_power` |
| Idle | Low/background activity | Activity falls; simulated noise may remain |
| Throttle | `throttled: true`, power at most half the cap, clocks halved | `amd_gpu_violation_processor_hot_residency_percentage` reaches 100 |
| Retire page | `retired_pages` increases, `ras/gpu_vram_bad_pages` lists the pages | none; agent metric `gpu_retired_pages` |
| xGMI down | `xgmi_error` is 1, xGMI `io_links` flags are 0 on both ends | none; agent metric `gpu_xgmi_link_up` |
| Crash | Status crashed, device nodes removed | Activity/power/clocks reach zero |
| Recover | Healthy defaults, ECC zero | ECC returns to zero and baseline readings resume |

An API response alone proves only injection into the mock. A matching change
at each stage establishes mocked AMD SMI -> real GPU Agent -> real exporter
-> Prometheus -> Grafana propagation. DRA is a separate allocation path,
not an intermediate telemetry stage. Recovery deliberately resets simulated
ECC counters; do not describe them as durable real-hardware counters.

```bash
kubectl -n amd-mock logs ds/fake-gpu --tail=30
kubectl -n amd-mock logs ds/fake-gpu-dra-kubeletplugin --tail=30
kubectl get pods -A
```

The mock log records actions such as `set BUSY` or `ECC ERROR`; it does not
prove scraping. Existing consumers need not exit on crash. A newly prepared
consumer can fail when its selected device node is absent, then retry after
recovery. That demonstrates a preparation failure, not automatic scheduler
health filtering. Read pod Events with `kubectl describe pod` to distinguish
these cases. Always finish with Recover before running other workloads.

See the [shared evidence checklist](../EVIDENCE.md) for commands, provenance,
and how to distinguish direct observations from inferred intermediate steps.

The mock `libamd_smi.so` reports retired pages, xGMI error status and
throttling to AMD's GPU Agent. Exporter v1.5.2 turns only throttling into a
Prometheus metric, so the bundled Prometheus scrapes `gpu_retired_pages` and
`gpu_xgmi_link_up` from the node agent instead. A GPU also throttles by itself
once it reaches 100 C, which is why Overheat halves its clocks. An xGMI link
comes back only when both of its ends are recovered. Busy holds the requested
utilization until Idle or Recover.
