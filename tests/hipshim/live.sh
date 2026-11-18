#!/usr/bin/env bash
# End to end: a HIP client in its own container allocates device memory, the
# agent reports it as used VRAM, and killing the client releases it.
set -euo pipefail
cd "$(dirname "$0")/../.."
read -r -a DOCKER <<< "${DOCKER:-docker}"
P=fakehip-live-$$
cleanup() { "${DOCKER[@]}" rm -f "$P-agent" "$P-client" >/dev/null 2>&1; "${DOCKER[@]}" volume rm "$P" >/dev/null 2>&1; }
trap cleanup EXIT

"${DOCKER[@]}" build -q -t fake-gpu:hip-test . >/dev/null
"${DOCKER[@]}" volume create "$P" >/dev/null
"${DOCKER[@]}" run -d --name "$P-agent" -v "$P:/var/lib/fake-gpu" fake-gpu:hip-test \
  node-agent --config /etc/fake-gpu/profiles/mi300x.yaml >/dev/null
sleep 3
used() { "${DOCKER[@]}" exec "$P-agent" amd-smi metric --gpu 3 | awk '/USED_VRAM:/ {print $2}'; }
base=$(used)

# The client builds against the header and library the agent staged in the volume.
"${DOCKER[@]}" run -d --name "$P-client" -v "$P:/var/lib/fake-gpu" -e HIP_VISIBLE_DEVICES=3 docker.io/library/gcc:14 sh -c '
  cat > /tmp/hold.c <<SRC
#include <fakehip.h>
#include <unistd.h>
int main(void) {
  void *p;
  if (hipMalloc(&p, (size_t)64 << 30) != hipSuccess) return 1;
  pause();
  return 0;
}
SRC
  gcc -I/var/lib/fake-gpu/driver/usr/include /tmp/hold.c -L/var/lib/fake-gpu/driver/usr/lib64 -lfakehip -o /tmp/hold &&
  LD_LIBRARY_PATH=/var/lib/fake-gpu/driver/usr/lib64 exec /tmp/hold' >/dev/null

for _ in $(seq 1 60); do held=$(used); [ "$held" = $((base + 65536)) ] && break; sleep 1; done
[ "$held" = $((base + 65536)) ] || { echo "FAIL: card 3 reports $held MB, want $((base + 65536))"; "${DOCKER[@]}" logs "$P-client" 2>&1 | tail -5; exit 1; }
echo "PASS client allocation of 64 GiB shows as used VRAM ($base -> $held MB)"

"${DOCKER[@]}" kill "$P-client" >/dev/null
for _ in $(seq 1 20); do after=$(used); [ "$after" = "$base" ] && break; sleep 1; done
[ "$after" = "$base" ] || { echo "FAIL: VRAM still $after MB after the client was killed"; exit 1; }
echo "PASS killing the client releases its VRAM"
