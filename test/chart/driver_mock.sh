#!/usr/bin/env bash
# The CUSE device backend is opt-in: the default chart never asks for it.
set -euo pipefail
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
source "$ROOT/test/lib/common.sh"
WORK="$(mktemp -d)"
trap 'rm -rf "$WORK"' EXIT
chart() { helm template fake-gpu "$ROOT/charts/fake-gpu" -n amd-mock "$@"; }
chart > "$WORK/default"
lacks "$WORK/default" '--device-backend=cuse'
chart --set driverMock.enabled=true > "$WORK/cuse"
has "$WORK/cuse" '--device-backend=cuse'
pass 'CUSE is opt-in; ordinary quick start unchanged'
