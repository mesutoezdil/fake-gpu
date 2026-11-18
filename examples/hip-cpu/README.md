# A HIP kernel on the CPU, on a fake card

fake-gpu does not execute GPU code. The
[HIP CPU Runtime](https://github.com/ROCm/HIP-CPU) does: it is a header-only
implementation of HIP that runs kernels on the processor. This example builds
a small HIP program against it and starts it through `fakegpuctl exec`, so the
mock card reports load for exactly as long as the kernel runs.

```bash
docker build -t fake-gpu:local .                         # repository root
docker build -t fake-gpu-hip-cpu examples/hip-cpu

docker network create fake
docker run -d --name fake-gpu --network fake -p 8080:8080 fake-gpu:local \
    node-agent --config /etc/fake-gpu/profiles/mi300x.yaml
docker run --rm --network fake fake-gpu-hip-cpu 16777216 400
```

```text
saxpy ok: 16777216 elements, 400 rounds, y = 802
```

While it runs, card 0 sits at about 90 percent on the console and in
`amd-smi`; when it exits the card is idle again. `fakegpuctl exec` returns the
program's exit code, so a failing job fails the container.

Three things to know before building on this:

- The HIP CPU Runtime is archived upstream and describes itself as incomplete.
  The Dockerfile pins the last commit.
- It lets consecutive launches on the default stream overlap, which a GPU does
  not. `saxpy.cpp` waits after every launch for that reason; without the wait
  the result is wrong.
- Its `hipMalloc` is its own, not `libfakehip`, so memory used by a HIP CPU
  program is not counted as VRAM. The load figure is whatever `util=` asks for,
  not a measurement.
