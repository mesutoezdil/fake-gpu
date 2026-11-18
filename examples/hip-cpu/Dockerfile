# A HIP program compiled against the HIP CPU Runtime, started through
# fakegpuctl so the mock card shows the load while the kernel runs.
#   docker build -t fake-gpu:local .            (from the repository root)
#   docker build -t fake-gpu-hip-cpu examples/hip-cpu
ARG FAKE_GPU_IMAGE=fake-gpu:local
FROM ${FAKE_GPU_IMAGE} AS tools

FROM docker.io/library/gcc:14
RUN apt-get update && apt-get install -y --no-install-recommends libtbb-dev \
    && rm -rf /var/lib/apt/lists/*
# Header-only and archived upstream, so a pinned commit is all there is to track.
RUN git clone -q https://github.com/ROCm/HIP-CPU.git /opt/hip-cpu \
    && git -C /opt/hip-cpu checkout -q e112c935057434897bb12d9ab3910380a8bd5f58
COPY saxpy.cpp /src/saxpy.cpp
RUN g++ -std=c++17 -O2 -I/opt/hip-cpu/include /src/saxpy.cpp -ltbb -pthread -o /usr/local/bin/saxpy
COPY --from=tools /usr/local/bin/fakegpuctl /usr/local/bin/fakegpuctl
ENV FAKE_GPU_API=http://fake-gpu:8080
ENTRYPOINT ["fakegpuctl", "exec", "-gpu", "0", "util=90", "--", "saxpy"]
