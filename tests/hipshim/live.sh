#!/usr/bin/env bash
# End to end: a HIP client in its own container allocates device memory, the
# agent reports it as used VRAM, and killing the client releases it.
set -euo pipefail
cd "$(dirname "$0")/../.."
source tests/lib/common.sh
read -r -a DOCKER <<< "${DOCKER:-docker}"
P=fakehip-live-$$
cleanup() { "${DOCKER[@]}" rm -f "$P-agent" "$P-client" >/dev/null 2>&1 || true; "${DOCKER[@]}" volume rm "$P" >/dev/null 2>&1 || true; }
trap cleanup EXIT

"${DOCKER[@]}" build -q -t fake-gpu:hip-test . >/dev/null
"${DOCKER[@]}" volume create "$P" >/dev/null
"${DOCKER[@]}" run -d --name "$P-agent" -v "$P:/var/lib/fake-gpu" fake-gpu:hip-test \
  node-agent --config /etc/fake-gpu/profiles/mi300x.yaml >/dev/null
used() { "${DOCKER[@]}" exec "$P-agent" amd-smi metric --gpu 3 2>/dev/null | awk '/USED_VRAM:/ {print $2}' || true; }
# used_is MB: card 3 reports exactly MB of used VRAM.
used_is() { [ "$(used)" = "$1" ]; }
agent_up() { is_int "$(used)"; }
eventually 30 agent_up || fail "agent never reported card 3 VRAM" "$("${DOCKER[@]}" logs "$P-agent" 2>&1 | tail -20)"
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

want=$((base + 65536))
eventually 60 used_is "$want" || fail "card 3 reports $(used) MB, want $want" "$("${DOCKER[@]}" logs "$P-client" 2>&1 | tail -5)"
pass "client allocation of 64 GiB shows as used VRAM ($base -> $want MB)"

"${DOCKER[@]}" kill "$P-client" >/dev/null
eventually 20 used_is "$base" || fail "VRAM still $(used) MB after the client was killed, want $base"
pass "killing the client releases its VRAM"
