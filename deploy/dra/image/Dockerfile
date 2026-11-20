# AMD's DRA driver binaries, built unmodified from ROCm/k8s-gpu-dra-driver by
# scripts/build-images.sh, on the base image AMD publishes them on.
FROM registry.access.redhat.com/ubi9/ubi-minimal:9.8
ARG UPSTREAM_COMMIT
LABEL org.opencontainers.image.source="https://github.com/ROCm/k8s-gpu-dra-driver" \
      org.opencontainers.image.revision="${UPSTREAM_COMMIT}"
COPY gpu-kubeletplugin webhook /usr/bin/
COPY LICENSE /licenses/AMD-DRA-LICENSE
