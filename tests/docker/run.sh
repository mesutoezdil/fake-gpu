#!/usr/bin/env bash
# Single-machine mode end to end: agent in one container, a second container
# reading its state. Needs only docker compose; override the command with
# COMPOSE, e.g. COMPOSE="sudo docker-compose".
set -euo pipefail
cd "$(dirname "$0")/../.."
read -r -a COMPOSE <<< "${COMPOSE:-docker compose}"
export COMPOSE_PROJECT_NAME="${COMPOSE_PROJECT_NAME:-fake-gpu-test}" FAKE_GPU_PORT="${FAKE_GPU_PORT:-18080}"
compose() { "${COMPOSE[@]}" -f deployments/docker/compose.yaml --profile tools "$@"; }
trap 'compose down -v --remove-orphans >/dev/null 2>&1' EXIT

compose up -d --build --wait fake-gpu
post() { compose exec -T fake-gpu wget -qO- --post-data= "http://127.0.0.1:8080/api/actions/$1" >/dev/null; }
smi() { compose run --rm -T amd-smi "$@"; }
expect() { grep -q -- "$2" <<< "$1" || { echo "FAIL: expected '$2' in:"; echo "$1"; exit 1; }; echo "PASS $3"; }

out=$(smi list)
[ "$(grep -c '^GPU: ' <<< "$out")" = 8 ] || { echo "FAIL: expected 8 GPUs"; echo "$out"; exit 1; }
echo "PASS second container lists 8 mock GPUs"

# The shipped scenario drives every fault and checks the API and sysfs.
out=$(compose exec -T fake-gpu fakegpuctl run /usr/share/fake-gpu/scenarios/faults.yaml) || { echo "FAIL: fault scenario"; echo "$out"; exit 1; }
expect "$out" "scenario passed" "fault scenario passes inside the agent container"

# A second container sees the same state through the shared volume.
post "throttle?gpu=1"
post "retire-page?gpu=2&count=2"
post "xgmi-down?gpu=3"
sleep 3
expect "$(smi metric --gpu 1)" "THROTTLE_STATUS: THROTTLED" "throttle reaches amd-smi in another container"
expect "$(smi bad-pages --gpu 2)" "RETIRED: 2" "page retirement reaches amd-smi in another container"
expect "$(smi monitor --gpu 3)" "xgmi_link_down" "xGMI fault reaches amd-smi in another container"
expect "$(compose exec -T fake-gpu rocm-smi)" "GPU_TEMP" "rocm-smi alias works"
expect "$(compose exec -T fake-gpu fakegpuctl recover -gpu 3)" "GPU 3: healthy" "fakegpuctl drives the API"
expect "$(compose exec -T fake-gpu fakegpuctl list)" "page_retired" "fakegpuctl lists fleet state"
echo "docker mode OK"
