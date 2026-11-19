# kind node for the mock: kind's image plus AMD's container runtime and CDI
# toolkit (built by scripts/build-images.sh), wired into containerd.
ARG BASE_IMAGE=docker.io/kindest/node:v1.37.0
FROM ${BASE_IMAGE}

COPY bin/amd-container-runtime bin/amd-ctk /usr/bin/
COPY containerd-config.toml /etc/containerd/config.toml
COPY amd-container-runtime.toml /etc/amd-container-runtime/config.toml
COPY TOOLKIT-LICENSE /licenses/AMD-CONTAINER-TOOLKIT-LICENSE
# The node agent writes its CDI specs into both directories.
RUN chmod 0755 /usr/bin/amd-container-runtime /usr/bin/amd-ctk && mkdir -p /etc/cdi /var/run/cdi
