# Two containers with slices of one GPU

Install chart 0.1.0 with `--set gpu.partition=DPX` on a fresh quick-start
cluster. The same published node image, AMD DRA driver and normal dashboard
port mappings are used. See [installation and architecture](../../docs/guides/partition-allocation.md).

For a dedicated cluster with an isolated kubeconfig and custom ports, follow
the [demo-room setup](../README.md#same-gpu-allocation-presentation). This runner
uses the current kubeconfig, so select the DPX cluster explicitly.

```bash
./demo/partition-allocation/run.sh
```

The script runs the checks, prints real pod logs and allocation evidence,
and leaves `amd-demo-partition-allocation/two-containers` Running. It does not
reuse or delete an existing demo namespace. Each container requests one named
partition from the shared ResourceClaim; the scheduler's same-parent constraint
ensures both slices come from one physical MI300X. Each gets 96 GiB of
advertised capacity and a different render device.

```bash
kubectl -n amd-demo-partition-allocation get pod two-containers
kubectl -n amd-demo-partition-allocation logs two-containers -c left
kubectl -n amd-demo-partition-allocation logs two-containers -c right
kubectl -n amd-demo-partition-allocation get resourceclaim two-slices -o yaml
```

The evidence comes from AMD's unchanged DRA driver publishing the mock's
DRM/KFD topology, Kubernetes allocating the claims, and AMD's NodePrepare
creating CDI edits which the container runtime applies. The test correlates
claim device names with ResourceSlice parent identity, profile and capacity,
and checks the actual character nodes inside each container. It also checks
exhaustion, reuse after NodeUnprepare removes the old claim CDI file, sibling
continuity, and a real driver pod replacement during restart.

This proves allocation and device visibility. The fake character devices do
not execute GPU kernels or prove hardware memory isolation. Per-partition
exporter workload attribution and physical fault propagation are outside
this allocation test.

The equivalent [two-container claim and pod](two-containers.json) can also
be applied in your own namespace.

Clean up with `kubectl delete namespace amd-demo-partition-allocation`.
