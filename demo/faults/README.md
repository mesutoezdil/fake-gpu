# Faults

Break a card, watch what notices, put it back. The shipped scenario does all
of it on card 0 and checks every step in the API and in the files the agent
renders:

```bash
kubectl -n amd-mock exec ds/fake-gpu -c node-agent -- \
    fakegpuctl run /usr/share/fake-gpu/scenarios/faults.yaml
```

```text
scenario: every fault on card 0, down to sysfs (10 steps)
[   0.0s] gpu 0 overheat -> overheating
[   0.0s] gpu 0 ok: status overheating, sys/class/drm/card0/device/hwmon/hwmon0/temp1_input 105000, temperature_c 105
[   2.0s] gpu 0 recover -> healthy
...
scenario passed in 16.0s
```

For a presentation, open the console on port 8080 next to Grafana on port
3000 and step through by hand instead. Leave each fault on for one scrape
interval, or Prometheus never samples it.

```bash
python3 demo/run.py faults --gpu 0 --action throttle
python3 demo/run.py faults --gpu 0 --action recover
```

`--action` takes `overheat`, `ecc-error`, `throttle`, `retire-page`,
`xgmi-down`, `crash`, `busy`, `idle` and `recover`. The console buttons call
the same API and toggle.

## Where each fault shows up

| Action | In the mock | In AMD's exporter |
| --- | --- | --- |
| Overheat | `temperature_c: 105`, clocks halved | `amd_gpu_edge_temperature` reaches 105 |
| ECC | `ecc_errors` rises, `ras/fatal_error` is 1 | `amd_gpu_ecc_uncorrect_total` rises |
| Throttle | `throttled: true`, power at most half the cap | `amd_gpu_violation_processor_hot_residency_percentage` reaches 100 |
| Retire page | `retired_pages` rises, pages listed in `ras/gpu_vram_bad_pages` | none; the agent exports `gpu_retired_pages` |
| xGMI down | `xgmi_error` is 1, xGMI `io_links` disabled on both ends | none; the agent exports `gpu_xgmi_link_up` |
| Busy | Load, power, clocks and used VRAM rise | `amd_gpu_gfx_activity`, `amd_gpu_package_power`, `amd_gpu_used_vram` |
| Crash | Device nodes and driver binding removed | Activity, power and clocks drop to zero |
| Recover | Profile defaults, counters zero | Counters return to zero |

AMD's exporter v1.5.2 has no metric for retired pages or xGMI link state. The
mock `libamd_smi.so` reports both to AMD's GPU Agent, and the bundled
Prometheus scrapes them from the node agent so they still reach Grafana.

## What a fault does not do

A crash removes the card from discovery; it does not make AMD SMI return
"device lost", and it does not kill a workload that already holds the card.
The device plugin notices only when it rediscovers, so the tests restart it.
DRA does not revoke a claim on a faulted card. An xGMI link comes back only
when both of its ends are recovered. Nothing here installs alert rules or
remediation: those are what you bring to test.

Fault propagation between the two halves of a partitioned card is not
modelled; see [partition allocation](../partition-allocation/README.md).
