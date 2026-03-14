#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
BINARY="${SCRIPT_DIR}/intermix-mcp"
PROJECT_ROOT="$(dirname "$SCRIPT_DIR")"

if [[ ! -x "$BINARY" ]]; then
    if ! command -v go &>/dev/null; then
        echo '{"error":"go not found — cannot build intermix-mcp. Install Go 1.23+."}' >&2
        exit 0
    fi
    cd "$PROJECT_ROOT"
    go build -o "$BINARY" ./cmd/intermix-mcp/ 2>&1 >&2
fi

exec "$BINARY" "$@"
