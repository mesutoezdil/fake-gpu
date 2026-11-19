#!/usr/bin/env bash
# Start OpenShift Local (CRC) for the Tiny LLM demo. Run it as your macOS
# user. The pull secret is read from a file and never printed.
set -euo pipefail
HERE="$(cd "$(dirname "$0")" && pwd)"
REPO="$(cd "$HERE/../../.." && pwd)"
# Locally a crc folder next to the checkout; CI points this at a protected
# file it writes from its secret store.
PULL_SECRET_FILE="${PULL_SECRET_FILE:-$REPO/../crc/pull-secret.txt}"
if [ ! -r "$PULL_SECRET_FILE" ]; then
    echo "Set PULL_SECRET_FILE to a readable Red Hat pull-secret file: $PULL_SECRET_FILE" >&2
    exit 1
fi
crc setup
crc start --pull-secret-file "$PULL_SECRET_FILE"
echo 'CRC started. Use crc oc-env, log in as admin, then run run.py install.'
