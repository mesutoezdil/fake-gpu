#!/usr/bin/env bash
# Delete the Spur demo's own cluster; the main amd-mock cluster is not touched.
set -euo pipefail
kind delete cluster --name "${SPUR_CLUSTER:-amd-spur}"
