#!/usr/bin/env bash
# A real HIP kernel runs on the CPU through the HIP CPU Runtime while the mock
# card it was "given" reports the load, then goes back to idle.
set -euo pipefail
cd "$(dirname "$0")/../.."
read -r -a DOCKER <<< "${DOCKER:-docker}"
P=hipcpu-$$
cleanup() { "${DOCKER[@]}" rm -f "$P-agent" "$P-job" >/dev/null 2>&1; "${DOCKER[@]}" network rm "$P" >/dev/null 2>&1; }
trap cleanup EXIT

"${DOCKER[@]}" build -q -t fake-gpu:hipcpu-test . >/dev/null
"${DOCKER[@]}" build -q --build-arg FAKE_GPU_IMAGE=fake-gpu:hipcpu-test -t fake-gpu-hip-cpu:test examples/hip-cpu >/dev/null
"${DOCKER[@]}" network create "$P" >/dev/null
"${DOCKER[@]}" run -d --name "$P-agent" --network "$P" --network-alias fake-gpu fake-gpu:hipcpu-test \
  node-agent --config /etc/fake-gpu/profiles/mi300x.yaml >/dev/null
sleep 3
load() { "${DOCKER[@]}" exec "$P-agent" amd-smi metric --gpu 0 | awk '/GFX_ACTIVITY:/ {print $2}'; }

# Enough work to keep the kernel running for several seconds.
"${DOCKER[@]}" run -d --name "$P-job" --network "$P" fake-gpu-hip-cpu:test 16777216 400 >/dev/null
peak=0
while [ "$("${DOCKER[@]}" inspect -f '{{.State.Running}}' "$P-job")" = true ]; do
  now=$(load); [ "$now" -gt "$peak" ] && peak=$now
  sleep 1
done
code=$("${DOCKER[@]}" inspect -f '{{.State.ExitCode}}' "$P-job")
out=$("${DOCKER[@]}" logs "$P-job" 2>&1)
[ "$code" = 0 ] && grep -q '^saxpy ok: 16777216 elements, 400 rounds, y = 802$' <<< "$out" || { echo "FAIL: exit $code"; echo "$out"; exit 1; }
echo "PASS HIP kernel computed the right result on the CPU: $out"
[ "$peak" -ge 85 ] || { echo "FAIL: card 0 peaked at $peak % while the kernel ran"; exit 1; }
echo "PASS card 0 reported $peak % load while the kernel ran"
sleep 3
[ "$(load)" -lt 8 ] || { echo "FAIL: card 0 still at $(load) % after the job"; exit 1; }
echo "PASS card 0 went back to idle"
