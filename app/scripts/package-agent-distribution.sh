#!/usr/bin/env bash
set -euo pipefail
APP_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
source "$APP_ROOT/scripts/package-identity.sh"
OUT_DIR="${1:-.cache/agent-distribution}"
# Resolve relative output before changing directories.
mkdir -p "$(dirname "$OUT_DIR")"
OUT_DIR="$(cd "$(dirname "$OUT_DIR")" && pwd)/$(basename "$OUT_DIR")"
cd "$APP_ROOT"
resolve_package_identity
require_empty_package_directory "$OUT_DIR"
mkdir -p "$OUT_DIR"
for arch in amd64 arm64; do
  GOOS=linux GOARCH="$arch" CGO_ENABLED=0 go build -buildvcs=false -trimpath \
    -ldflags "$BUILD_LDFLAGS" -o "$OUT_DIR/setpoint-agent-linux-$arch" ./cmd/setpoint-agent
done
printf '%s\n' "$VERSION" > "$OUT_DIR/VERSION"
printf '%s\n' "$SOURCE_SHA" > "$OUT_DIR/SOURCE_SHA"
(cd "$OUT_DIR"; sha256sum -b setpoint-agent-linux-amd64 setpoint-agent-linux-arm64 VERSION SOURCE_SHA > SHA256SUMS)
