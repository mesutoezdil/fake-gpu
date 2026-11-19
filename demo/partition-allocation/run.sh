#!/usr/bin/env bash
set -euo pipefail
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
[ "$#" -eq 0 ] || { echo "usage: $0" >&2; exit 1; }
exec python3 -u "$ROOT/test/e2e/dra/partitions.py"
