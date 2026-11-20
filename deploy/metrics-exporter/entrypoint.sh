#!/bin/sh
# Run AMD's GPU Agent and metrics server, unchanged, against the AMD SMI mock
# in /mock. They are x86-64 binaries: on arm64 nodes they run under qemu.
# The container fails as soon as either of them stops.
set -eu

SOCKET=/var/run/gpuagent.sock

amd() {
    if [ "$(uname -m)" = aarch64 ]; then
        /usr/bin/qemu-x86_64-static -L /opt/amd64 -E LD_LIBRARY_PATH=/mock "$@"
    else
        LD_LIBRARY_PATH=/mock "$@"
    fi
}

amd /home/amd/bin/gpuagent -s "$SOCKET" &
agent=$!
server=
trap 'kill $agent $server 2>/dev/null || true' EXIT INT TERM

# The server connects to the agent's socket; give the agent up to 60 s to
# open it (emulated arm64 nodes are slow), and start the server regardless.
i=0
while [ ! -S "$SOCKET" ] && [ "$i" -lt 60 ] && kill -0 "$agent" 2>/dev/null; do
    sleep 1
    i=$((i + 1))
done

amd /home/amd/bin/server "$@" &
server=$!

while kill -0 "$agent" 2>/dev/null && kill -0 "$server" 2>/dev/null; do
    sleep 2
done
echo "AMD collector stopped" >&2
exit 1
