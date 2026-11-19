#!/usr/bin/env bash
# Reject configurations that can double-allocate devices or discover real sysfs.
set -euo pipefail
REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
source "$REPO_ROOT/test/lib/common.sh"
CHART="$REPO_ROOT/charts/fake-gpu"
WORK="$(mktemp -d)"
trap 'rm -rf "$WORK"' EXIT
render() { helm template fake-gpu "$CHART" -n amd-mock "$@"; }
reject() {
    local expected="$1"; shift
    if render "$@" >"$WORK/output" 2>"$WORK/error"; then
        fail "invalid configuration rendered successfully: $*"
    fi
    grep -Fq -- "$expected" "$WORK/error" || fail "wrong rejection, want: $expected" "$(cat "$WORK/error")"
    pass "rejects $expected"
}
render >"$WORK/default.yaml"
has "$WORK/default.yaml" 'kind: DeviceClass'
render --set dra.enabled=false --set devicePlugin.enabled=true >"$WORK/device-plugin.yaml"
lacks "$WORK/device-plugin.yaml" 'kind: DeviceClass'
has "$WORK/device-plugin.yaml" 'name: fake-gpu-device-plugin'
render --set dra.enabled=true --set devicePlugin.enabled=false >"$WORK/dra.yaml"
has "$WORK/dra.yaml" 'kind: DeviceClass'
has "$WORK/dra.yaml" 'path: "/var/lib/fake-gpu/sys"'
lacks "$WORK/dra.yaml" 'name: fake-gpu-device-plugin'
reject 'set devicePlugin.enabled=false' --set dra.enabled=true --set devicePlugin.enabled=true
reject 'dra.mockSysPath must equal' --set dra.enabled=true --set devicePlugin.enabled=false --set dra.mockSysPath=/sys
reject 'dra.kubeletPlugin.nodeSelector must match' --set dra.enabled=true --set devicePlugin.enabled=false --set-string nodeSelector.mock=true
render --set dra.enabled=true --set devicePlugin.enabled=false \
    --set-string nodeSelector.mock=true --set-string dra.kubeletPlugin.nodeSelector.mock=true \
    --set mockRootDir=/custom/mock --set dra.mockSysPath=/custom/mock/sys >"$WORK/custom.yaml"
has "$WORK/custom.yaml" 'path: "/custom/mock/sys"'
pass 'default, DRA, and custom-root configurations render correctly'
reject 'gpu.partition must be SPX or DPX' --set gpu.partition=CPX
reject 'schedulable DPX/NPS2 partitions require gpu.profile=mi300x' --set gpu.partition=DPX --set gpu.profile=mi210
render --set gpu.partition=DPX >"$WORK/partitions.yaml"
has "$WORK/partitions.yaml" '--partition=DPX'
pass 'MI300X DPX startup configuration renders correctly'
