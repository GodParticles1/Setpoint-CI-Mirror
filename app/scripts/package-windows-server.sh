#!/usr/bin/env bash
set -euo pipefail

VERSION="${SETPOINT_VERSION:-}"
OUT_DIR="${1:-.cache/windows-server-distribution}"
APP_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"

source "$APP_ROOT/scripts/package-identity.sh"
# Validate source before creating package artifacts.
(cd "$APP_ROOT"; resolve_package_identity)
require_empty_package_directory "$OUT_DIR"

mkdir -p "$OUT_DIR" "$OUT_DIR/agents" "$OUT_DIR/logs" "$OUT_DIR/data"
OUT_DIR="$(cd "$OUT_DIR" && pwd)"

(
  cd "$APP_ROOT"
  resolve_package_identity
  GOOS=windows GOARCH=amd64 CGO_ENABLED=0 \
    go build -buildvcs=false -trimpath \
      -ldflags "$BUILD_LDFLAGS" \
      -o "$OUT_DIR/setpoint-server.exe" \
      ./cmd/setpoint-server
  GOOS=windows GOARCH=amd64 CGO_ENABLED=0 \
    go build -buildvcs=false -trimpath -ldflags "$BUILD_LDFLAGS" -o "$OUT_DIR/setpoint-agent.exe" ./cmd/setpoint-agent
  printf '%s\n' "$SOURCE_SHA" > "$OUT_DIR/SOURCE_SHA"
  SETPOINT_VERSION="$VERSION" bash scripts/package-agent-distribution.sh "$OUT_DIR/agents"
)

cp "$APP_ROOT/packaging/windows/start.bat" "$OUT_DIR/start.bat"
cp "$APP_ROOT/packaging/windows/stop.bat" "$OUT_DIR/stop.bat"
cp "$APP_ROOT/packaging/windows/start.ps1" "$OUT_DIR/start.ps1"
cp "$APP_ROOT/packaging/windows/stop.ps1" "$OUT_DIR/stop.ps1"
printf '%s\n' "$VERSION" > "$OUT_DIR/VERSION"

(
  cd "$OUT_DIR"
  sha256sum -b setpoint-server.exe setpoint-agent.exe start.bat stop.bat start.ps1 stop.ps1 VERSION SOURCE_SHA agents/VERSION agents/SOURCE_SHA agents/SHA256SUMS agents/setpoint-agent-linux-amd64 agents/setpoint-agent-linux-arm64 > SHA256SUMS
)
