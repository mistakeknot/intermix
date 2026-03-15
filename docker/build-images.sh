#!/usr/bin/env bash
# Build Docker images for SWE-bench evaluation.
# Usage: ./docker/build-images.sh [PYTHON_VERSIONS...]
# Default: builds all versions needed for SWE-bench Lite
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_DIR="$(dirname "$SCRIPT_DIR")"
SKAFFEN_BIN="${SKAFFEN_BIN:-$(command -v skaffen 2>/dev/null || echo "")}"
IMAGE_PREFIX="${IMAGE_PREFIX:-intermix-swebench}"

# Python versions needed for SWE-bench Lite
DEFAULT_VERSIONS=(3.6 3.8 3.9 3.10 3.11)
VERSIONS=("${@:-${DEFAULT_VERSIONS[@]}}")

if [[ -z "$SKAFFEN_BIN" ]]; then
    echo "Error: skaffen binary not found. Set SKAFFEN_BIN or add to PATH." >&2
    exit 1
fi

# Resolve symlinks to get actual binary
SKAFFEN_REAL="$(readlink -f "$SKAFFEN_BIN")"

# Create temp build context with skaffen binary
BUILD_CTX=$(mktemp -d)
trap "rm -rf $BUILD_CTX" EXIT
cp "$SKAFFEN_REAL" "$BUILD_CTX/skaffen"
cp "$SCRIPT_DIR/Dockerfile.swebench" "$BUILD_CTX/Dockerfile"

for ver in "${VERSIONS[@]}"; do
    tag="${IMAGE_PREFIX}:py${ver}"
    echo "Building ${tag}..."
    docker build \
        --build-arg "PYTHON_VERSION=${ver}" \
        -t "$tag" \
        -f "$BUILD_CTX/Dockerfile" \
        "$BUILD_CTX"
    echo "  Done: ${tag}"
done

echo ""
echo "Built ${#VERSIONS[@]} images:"
for ver in "${VERSIONS[@]}"; do
    echo "  ${IMAGE_PREFIX}:py${ver}"
done
