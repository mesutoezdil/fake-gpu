#!/usr/bin/env python3
"""Presenter demos against an installed mock, each in a namespace of its own.

    python3 demo/run.py list
    python3 demo/run.py <demo> [--context ...] [--config demo/config.yaml]

Every demo prints EVIDENCE lines for what it checked and PASS when the
check held; cleanup removes only the namespaces these demos created.
"""
import argparse
import base64
import json
import os
import subprocess
import sys
import time
import urllib.request
from pathlib import Path

ROOT = Path(__file__).resolve().parent.parent
LABEL = "fake-gpu/demo"
BUSYBOX = "docker.io/library/busybox:1.36"
DEMOS = {}


def demo(summary):
    """Register a demo under its function name (underscores become dashes)."""
    def register(fn):
        DEMOS[fn.__name__.replace("_", "-")] = (summary, fn)
        return fn
    return register


class Failed(RuntimeError):
    pass


class Cluster:
    """kubectl bound to the demo's context."""

    def __init__(self, context):
        self.context = context

    def __call__(self, *args, obj=None, check=True):
        result = subprocess.run(["kubectl", "--context", self.context, *args], capture_output=True, text=True,
                                input=None if obj is None else json.dumps(obj), check=False)
        if check and result.returncode:
            raise Failed(result.stderr.strip())
        return result.stdout

    def json(self, *args):
        return json.loads(self(*args, "-o", "json"))

    def apply(self, obj):
        return self("apply", "-f", "-", obj=obj)

    def namespace(self, name):
        """A namespace labelled as the demos', so cleanup finds it."""
        name = "amd-demo-" + name
        self.apply({"apiVersion": "v1", "kind": "Namespace", "metadata": {"name": name, "labels": {LABEL: "true"}}})
        return name

    def exists(self, kind, name, namespace):
        return bool(self("get", kind + "/" + name, "-n", namespace, "--ignore-not-found").strip())


class Demo:
    def __init__(self, args):
        self.args = args
        self.k = Cluster(args.context)

    # --- Installation ----------------------------------------------------

    def allocator(self):
        """dra or device-plugin, as installed, unless --allocator says."""
        if self.args.allocator != "auto":
            return self.args.allocator
        if self.k.exists("ds", "fake-gpu-dra-kubeletplugin", "amd-mock"):
            return "dra"
        # The chart's plugin counts as soon as it is installed: capacity shows
        # up seconds later, and a workload stays Pending until then.
        if self.k.exists("ds", "fake-gpu-device-plugin", "kube-system"):
            return "device-plugin"
        if any(int(n["status"].get("allocatable", {}).get("amd.com/gpu", "0")) for n in self.k.json("get", "nodes")["items"]):
            return "device-plugin"
        raise Failed("No allocator detected. Run demo/setup.sh first.")

    def settings(self):
        """Host URLs and toggles, rendered from the same values as the install."""
        out = subprocess.check_output(["helm", "template", "kind", str(ROOT / "deploy/kind-node/config-chart"),
                                       "-f", self.args.config, "--show-only", "templates/runtime.json"], text=True)
        return json.loads(out[out.index("{"):])

    # --- Host APIs -------------------------------------------------------

    @staticmethod
    def http(url, method=None, data=None, password=None):
        headers = {"Content-Type": "application/json"}
        if password is not None:
            headers["Authorization"] = "Basic " + base64.b64encode(("admin:" + password).encode()).decode()
        body = None if data is None else json.dumps(data).encode()
        with urllib.request.urlopen(urllib.request.Request(url, body, headers, method=method), timeout=20) as r:
            return json.load(r)

    def mock(self, path, method=None):
        cfg = self.settings()
        if not cfg["mockDashboardEnabled"]:
            raise Failed("The mock host dashboard/API is disabled in this config.")
        return self.http(os.environ.get("MOCK_URL", cfg["mockURL"]) + path, method)

    # --- Workloads -------------------------------------------------------

    def consumer(self, namespace, name, mode, count=1):
        """Run a pod that asks for count GPUs and prove what it got."""
        container = {"name": "consumer", "image": BUSYBOX,
                     "command": ["sh", "-c", "test -c /dev/kfd && ls -l /dev/kfd /dev/dri && sleep 3600"]}
        spec = {"restartPolicy": "Never", "terminationGracePeriodSeconds": 0, "containers": [container]}
        if mode == "dra":
            self.k.apply({"apiVersion": "resource.k8s.io/v1", "kind": "ResourceClaimTemplate",
                          "metadata": {"name": name, "namespace": namespace},
                          "spec": {"spec": {"devices": {"requests": [
                              {"name": "gpu", "exactly": {"deviceClassName": "gpu.amd.com", "count": count}}]}}}})
            spec["resourceClaims"] = [{"name": "gpu", "resourceClaimTemplateName": name}]
            container["resources"] = {"claims": [{"name": "gpu"}]}
        else:
            container["resources"] = {"limits": {"amd.com/gpu": count}}
        self.k.apply({"apiVersion": "v1", "kind": "Pod", "metadata": {"name": name, "namespace": namespace}, "spec": spec})
        self.k("wait", "pod/" + name, "-n", namespace, "--for=condition=Ready", "--timeout=180s")
        devices = self.evidence(namespace, name, mode, count)
        print(self.k("logs", name, "-n", namespace))
        if mode == "dra":
            print(self.k("get", "resourceclaims", "-n", namespace, "-o", "wide"))
        return devices

    def evidence(self, namespace, name, mode, count=1):
        """Check and print what a pod received; returns its render devices."""
        pod = self.k.json("get", "pod", name, "-n", namespace)
        devices = self.k("exec", name, "-n", namespace, "--", "sh", "-c",
                         'test -c /dev/kfd || exit 1; for d in /dev/dri/renderD*; do test -c "$d" || exit 1; done; '
                         "ls /dev/dri/renderD*").splitlines()
        if len(devices) != count:
            raise Failed(f"Expected {count} render devices, got {devices!r}")
        print(f"EVIDENCE pod {namespace}/{name} scheduled on {pod['spec']['nodeName']}; "
              f"/dev/kfd is a character device; render devices: {', '.join(devices)}")
        if mode == "dra":
            self.claim_evidence(namespace, pod, devices, count)
            print("PASS AMD DRA advertised device -> allocated claim -> matching injected render device")
        else:
            print(f"EVIDENCE request amd.com/gpu={count} -> scheduler placement -> injected render devices")
            print("PASS device-plugin allocation/injection; no DRA claim is involved")
        print("This proves mock device allocation and injection, not GPU execution or hardware isolation.")
        return devices

    def claim_evidence(self, namespace, pod, devices, count):
        slices = self.k.json("get", "resourceslices")["items"]
        injected = 0
        for ref in pod["status"].get("resourceClaimStatuses", []):
            claim = self.k.json("get", "resourceclaim", ref["resourceClaimName"], "-n", namespace)
            for result in claim["status"]["allocation"]["devices"]["results"]:
                advertised = [d for s in slices if s["spec"]["driver"] == result["driver"]
                              and s["spec"]["pool"]["name"] == result["pool"]
                              for d in s["spec"].get("devices", []) if d["name"] == result["device"]]
                if not advertised:
                    raise Failed("Allocated device absent from current ResourceSlices: " + str(result))
                print(f"EVIDENCE claim {claim['metadata']['name']} -> driver={result['driver']} pool={result['pool']} "
                      f"device={result['device']}; advertised capacity={advertised[0].get('capacity', {})}")
                # AMD's driver names devices gpu-<card>-<render minor>.
                render = "/dev/dri/renderD" + result["device"].split("-")[-1]
                if render not in devices:
                    raise Failed("Allocated render device was not injected: " + render)
                injected += 1
        if injected != count:
            raise Failed("Claim allocation count does not match the requested GPU count")

    # --- Demos -----------------------------------------------------------

    @demo("Scripted Tiny LLM: GPU scheduling and device injection (both allocators)")
    def llm(self):
        mode = self.allocator()
        namespace = self.k.namespace("llm")
        self.k("apply", "-n", namespace, "-f", str(ROOT / f"deploy/workloads/tiny-llm-{mode}.yaml"))
        deployment = "tiny-llm-dra-demo" if mode == "dra" else "tiny-llm-demo"
        self.k("rollout", "status", "deployment/" + deployment, "-n", namespace, "--timeout=180s")
        time.sleep(5)  # let the scripted model-loading lines appear
        print(self.k("logs", "deployment/" + deployment, "-n", namespace, "--tail=70"))
        for pod in self.k.json("get", "pods", "-n", namespace)["items"]:
            if not pod["metadata"].get("deletionTimestamp"):
                self.evidence(namespace, pod["metadata"]["name"], mode)
        print("Scripted output only: no model weights or GPU inference.")

    @demo("SPX/DPX/QPX/CPX dashboard state; physical allocation stays unchanged")
    def partitioning(self):
        print(json.dumps(self.mock("/api/partitions/set?mode=" + self.args.mode, "POST"), indent=2))
        print("Virtual dashboard entries only; allocator capacity remains physical. Reset with --mode SPX.")
        print("Evidence boundary: this action exercises the mock API/display; it does not create DRA slices.")

    @demo("ResourceSlice -> claim -> CDI -> one-GPU consumer")
    def dra(self):
        if self.allocator() != "dra":
            raise Failed("This demo requires the DRA installation.")
        namespace = self.k.namespace("dra")
        self.k("apply", "-n", namespace, "-f", str(ROOT / "deploy/workloads/dra-claim.yaml"))
        self.k("wait", "pod/dra-gpu-demo", "-n", namespace, "--for=condition=Ready", "--timeout=180s")
        print(self.k("get", "resourceslices"))
        print(self.k("get", "resourceclaims", "-n", namespace, "-o", "yaml"))
        print(self.k("logs", "dra-gpu-demo", "-n", namespace))
        self.evidence(namespace, "dra-gpu-demo", "dra")

    @demo("Overheat, ECC, throttle, page retirement, xGMI link down, crash, busy, idle and recover from the dashboard API")
    def faults(self):
        print(json.dumps(self.mock(f"/api/actions/{self.args.action}?gpu={self.args.gpu}", "POST"), indent=2))
        print("Watch the mock dashboard and Grafana. Recover with --action recover.")
        print("Fault injection does not automatically revoke claims or restart workloads.")
        print("The JSON above confirms mock state only. Run telemetry and inspect the exporter/Prometheus")
        print("as described in demo/faults/README.md to prove downstream propagation after collection.")

    @demo("One container with two GPUs, or concurrent independent consumers")
    def multi_gpu(self):
        if self.args.count < 1 or self.args.replicas < 1:
            raise Failed("--count and --replicas must be positive")
        mode = self.allocator()
        namespace = self.k.namespace("multi-gpu")
        taken = set()
        for i in range(self.args.replicas):
            devices = self.consumer(namespace, f"consumer-{i}", mode, self.args.count)
            if taken & set(devices):
                raise Failed("Independent consumers received overlapping render devices")
            taken |= set(devices)
        print(f"PASS {self.args.replicas} consumers use {len(taken)} distinct render devices on this single-node demo cluster")
        print("Each independent request receives its own physical GPU allocation.")

    @demo("Delete a consumer, release its generated claim, then allocate again")
    def allocation(self):
        mode = self.allocator()
        namespace = self.k.namespace("allocation")
        self.consumer(namespace, "first", mode)
        print("Deleting first consumer; returning allocation to the pool...")
        self.k("delete", "pod/first", "-n", namespace, "--wait=true", "--timeout=120s")
        if mode == "dra":
            # The generated claim goes with its owner pod, by garbage collection.
            deadline = time.monotonic() + 120
            while self.k.json("get", "resourceclaims", "-n", namespace)["items"]:
                if time.monotonic() > deadline:
                    raise Failed("Generated claim cleanup timed out")
                time.sleep(2)
        self.consumer(namespace, "second", mode)
        print("PASS: allocation works after release (not a promise of the same GPU).")

    @demo("Real AMD exporter metrics queried through the provisioned Grafana")
    def telemetry(self):
        cfg = self.settings()
        if not cfg["grafanaEnabled"]:
            raise Failed("Grafana is disabled in this config.")
        url = os.environ.get("GRAFANA_URL", cfg["grafanaURL"])
        password = os.environ.get("GRAFANA_ADMIN_PASSWORD", cfg["grafanaPassword"])
        print("Dashboard:", self.http(url + "/api/dashboards/uid/amd-real-exporter", password=password)["dashboard"]["title"])
        print("PATH: mocked AMD SMI -> real AMD GPU Agent -> real AMD exporter -> Prometheus -> Grafana")
        print("These queries prove Grafana datasource results; use the guide's snapshot/exporter checks")
        print("to correlate a controlled change across every layer. DRA is the allocation path, not a metric hop.")
        for metric in ["amd_gpu_edge_temperature", "amd_gpu_ecc_uncorrect_total", "amd_gpu_gfx_activity", "amd_gpu_used_vram"]:
            result = self.http(url + "/api/ds/query", data={"from": "now-5m", "to": "now", "queries": [{
                "refId": "A", "expr": metric, "instant": True, "format": "table",
                "datasource": {"type": "prometheus", "uid": "prometheus"}, "intervalMs": 1000, "maxDataPoints": 1000}]},
                password=password)["results"]["A"]
            if result.get("error"):
                raise Failed(str(result))
            frames = result.get("frames", [])
            if not frames:
                raise Failed("Metrics not collected yet; retry after the next scrape.")
            print(metric)
            for frame in frames:
                for field, values in zip(frame["schema"]["fields"], frame["data"]["values"], strict=False):
                    if field["type"] == "number" and values:
                        labels = field.get("labels", {})
                        owner = "/".join(labels.get(k, "") for k in ("namespace", "pod", "container"))
                        print(f"  GPU {labels.get('gpu_id', '?')}: {values[-1]}" + (f"  consumer={owner}" if owner != "//" else ""))

    @demo("Show the GPU profile catalog and installed physical inventory")
    def profiles(self):
        print("PROFILE    MODEL                     DEVICES  GiB/DEVICE  TDP/W")
        for p in self.mock("/api/profiles"):
            print(f"{p['slug']:<10} {p['name']:<25} {p['gpu_count']:7d} {p['memory_gb']:11d} {p['tdp_w']:6d}")
        print("\nInstalled inventory:")
        for g in self.mock("/api/gpus")["gpus"]:
            print(f"GPU {g['index']:<2d} {g['pci_bdf']:<13} NUMA={g['numa_node']} {g['name']:<25} "
                  f"VRAM={g['memory_total_mb']} MiB status={g['status']}")
        print("Select a different profile at installation; avoid live switches with active consumers.")

    @demo("Delete only namespaces owned by these demos and recover mock state")
    def cleanup(self):
        print(self.k("delete", "namespaces", "-l", LABEL + "=true", "--wait=true", "--timeout=120s"))
        if self.settings()["mockDashboardEnabled"]:
            self.mock("/api/partitions/set?mode=SPX", "POST")
            for g in self.mock("/api/gpus")["gpus"]:
                self.mock(f"/api/actions/recover?gpu={g['index']}", "POST")
        print("Demo workloads removed. Mock faults/partition state reset when API is enabled.")


def main(argv):
    parser = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    parser.add_argument("command", choices=["list", *DEMOS])
    parser.add_argument("--config", default=str(ROOT / "demo/config.yaml"))
    parser.add_argument("--context", default=os.environ.get("DEMO_CONTEXT", "kind-" + os.environ.get("CLUSTER_NAME", "amd-mock")))
    parser.add_argument("--allocator", choices=["auto", "dra", "device-plugin"], default="auto")
    parser.add_argument("--gpu", type=int, default=0)
    parser.add_argument("--action", default="overheat",
                        choices=["overheat", "ecc-error", "throttle", "retire-page", "xgmi-down", "crash", "busy", "idle", "recover"])
    parser.add_argument("--mode", choices=["SPX", "DPX", "QPX", "CPX"], default="CPX")
    parser.add_argument("--count", type=int, default=2)
    parser.add_argument("--replicas", type=int, default=1)
    args = parser.parse_args(argv)
    if args.command == "list":
        for name, (summary, _) in DEMOS.items():
            print(f"{name:<14} {summary}")
        return
    summary, run = DEMOS[args.command]
    print("DEMO: " + summary, flush=True)
    if args.command != "cleanup":
        print(f"Guide: demo/{args.command}/README.md; inspect pods across namespaces with kubectl get pods -A", flush=True)
    try:
        run(Demo(args))
    except (Failed, OSError, subprocess.CalledProcessError) as error:
        parser.exit(1, f"Demo failed: {error}\n")


if __name__ == "__main__":
    main(sys.argv[1:])
