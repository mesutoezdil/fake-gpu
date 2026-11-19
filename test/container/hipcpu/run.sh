#!/usr/bin/env bash
# A real HIP kernel runs on the CPU through the HIP CPU Runtime while the mock
# card it was "given" reports the load, then goes back to idle.
set -euo pipefail
cd "$(dirname "$0")/../../.."
source test/lib/common.sh
read -r -a DOCKER <<< "${DOCKER:-docker}"
P=hipcpu-$$
cleanup() { "${DOCKER[@]}" rm -f "$P-agent" "$P-job" >/dev/null 2>&1 || true; "${DOCKER[@]}" network rm "$P" >/dev/null 2>&1 || true; }
trap cleanup EXIT

"${DOCKER[@]}" build -q -t fake-gpu:hipcpu-test . >/dev/null
"${DOCKER[@]}" build -q --build-arg FAKE_GPU_IMAGE=fake-gpu:hipcpu-test -t fake-gpu-hip-cpu:test examples/hip-cpu >/dev/null
"${DOCKER[@]}" network create "$P" >/dev/null
"${DOCKER[@]}" run -d --name "$P-agent" --network "$P" --network-alias fake-gpu fake-gpu:hipcpu-test \
  node-agent --config /etc/fake-gpu/profiles/mi300x.yaml >/dev/null
load() { "${DOCKER[@]}" exec "$P-agent" amd-smi metric --gpu 0 2>/dev/null | awk '/GFX_ACTIVITY:/ {print $2}' || true; }
agent_up() { is_int "$(load)"; }
idle() { local now; now=$(load); is_int "$now" && [ "$now" -lt 8 ]; }
eventually 30 agent_up || fail "agent never reported card 0 load" "$("${DOCKER[@]}" logs "$P-agent" 2>&1 | tail -20)"

# Enough work to keep the kernel running for several seconds.
"${DOCKER[@]}" run -d --name "$P-job" --network "$P" fake-gpu-hip-cpu:test 16777216 400 >/dev/null
peak=0
while [ "$("${DOCKER[@]}" inspect -f '{{.State.Running}}' "$P-job")" = true ]; do
  now=$(load)
  if is_int "$now" && [ "$now" -gt "$peak" ]; then peak=$now; fi
  sleep 1
done
code=$("${DOCKER[@]}" inspect -f '{{.State.ExitCode}}' "$P-job")
out=$("${DOCKER[@]}" logs "$P-job" 2>&1)
if [ "$code" != 0 ] || ! grep -q '^saxpy ok: 16777216 elements, 400 rounds, y = 802$' <<< "$out"; then
  fail "kernel exited $code" "$out"
fi
pass "HIP kernel computed the right result on the CPU: $out"
[ "$peak" -ge 85 ] || fail "card 0 peaked at $peak % while the kernel ran"
pass "card 0 reported $peak % load while the kernel ran"
eventually 15 idle || fail "card 0 still at $(load) % after the job"
pass "card 0 went back to idle"
