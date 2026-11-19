#!/usr/bin/env bash
# Every render-only chart contract. Needs helm and python3, no cluster.
set -euo pipefail
cd "$(dirname "${BASH_SOURCE[0]}")"
for check in dra.sh demo.sh driver_mock.sh; do
    echo "== test/chart/$check"
    "./$check"
done
