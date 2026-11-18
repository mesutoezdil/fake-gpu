#!/usr/bin/env bash
# Builds libfakehip and runs its contract test in a compiler container.
set -euo pipefail
ROOT="$(cd "$(dirname "$0")/../.." && pwd)"
FIXTURE="$(mktemp -d)"
trap 'rm -rf "$FIXTURE"' EXIT
mkdir -p "$FIXTURE/smi" && chmod 777 "$FIXTURE"
printf '8\n' > "$FIXTURE/smi/count"
for i in 0 1 2 3 4 5 6 7; do
  printf '45 120 750 0 300 196608 0 500 1300\nAMD Instinct MI300X\n00000000-0000-0000-0000-00000000000%d\n0000:05:00.0\nhealthy 0 0\n' "$i" > "$FIXTURE/smi/gpu$i"
done
"${CONTAINER_RUNTIME:-docker}" run --rm -v "$ROOT:/src:ro" -v "$FIXTURE:/state" \
  -e FAKE_GPU_STATE_DIR=/state -e HIP_VISIBLE_DEVICES=5,2 docker.io/library/gcc:14 sh -c '
    cd /tmp && gcc -O2 -Wall -Wextra -Werror -fPIC -shared -o libfakehip.so /src/pkg/hipshim/fakehip.c -lpthread &&
    gcc -Wall -Wextra -Werror -I/src/pkg/hipshim /src/tests/hipshim/alloc.c -L. -lfakehip -o alloc &&
    LD_LIBRARY_PATH=. ./alloc &&
    env -u HIP_VISIBLE_DEVICES FAKE_GPU_STATE_DIR=/nowhere LD_LIBRARY_PATH=. sh -c "./alloc 2>/dev/null; test \$? -ne 0" &&
    echo "fakehip: missing state directory is reported, not crashed through PASS"'
