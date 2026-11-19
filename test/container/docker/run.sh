#!/usr/bin/env bash
# Single-machine mode end to end: agent in one container, a second container
# reading its state. Needs only docker compose; override the command with
# COMPOSE, e.g. COMPOSE="sudo docker-compose".
set -euo pipefail
cd "$(dirname "$0")/../../.."
source test/lib/common.sh
read -r -a COMPOSE <<< "${COMPOSE:-docker compose}"
export COMPOSE_PROJECT_NAME="${COMPOSE_PROJECT_NAME:-fake-gpu-test}" FAKE_GPU_PORT="${FAKE_GPU_PORT:-18080}"
compose() { "${COMPOSE[@]}" -f deploy/docker/compose.yaml --profile tools "$@"; }
trap 'compose down -v --remove-orphans >/dev/null 2>&1 || true' EXIT

compose up -d --build --wait fake-gpu
post() { compose exec -T fake-gpu wget -qO- --post-data= "http://127.0.0.1:8080/api/actions/$1" >/dev/null; }
smi() { compose run --rm -T amd-smi "$@"; }

out=$(smi list)
[ "$(grep -c '^GPU: ' <<< "$out")" = 8 ] || fail "expected 8 GPUs" "$out"
pass "second container lists 8 mock GPUs"

# The shipped scenario drives every fault and checks the API and sysfs.
out=$(compose exec -T fake-gpu fakegpuctl run /usr/share/fake-gpu/scenarios/faults.yaml) || fail "fault scenario" "$out"
expect "$out" "scenario passed" "fault scenario passes inside the agent container"

# A second container sees the same state through the shared volume, once the
# agent's next simulator tick has written it.
post "throttle?gpu=1"
post "retire-page?gpu=2&count=2"
post "xgmi-down?gpu=3"
eventually 30 shows "THROTTLE_STATUS: THROTTLED" smi metric --gpu 1 || fail "throttle never reached amd-smi" "$(smi metric --gpu 1)"
pass "throttle reaches amd-smi in another container"
eventually 30 shows "RETIRED: 2" smi bad-pages --gpu 2 || fail "page retirement never reached amd-smi" "$(smi bad-pages --gpu 2)"
pass "page retirement reaches amd-smi in another container"
eventually 30 shows "xgmi_link_down" smi monitor --gpu 3 || fail "xGMI fault never reached amd-smi" "$(smi monitor --gpu 3)"
pass "xGMI fault reaches amd-smi in another container"
expect "$(compose exec -T fake-gpu rocm-smi)" "GPU_TEMP" "rocm-smi alias works"
expect "$(compose exec -T fake-gpu fakegpuctl recover -gpu 3)" "GPU 3: healthy" "fakegpuctl drives the API"
expect "$(compose exec -T fake-gpu fakegpuctl list)" "page_retired" "fakegpuctl lists fleet state"
echo "docker mode OK"
