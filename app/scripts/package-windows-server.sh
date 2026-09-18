#!/usr/bin/env bash
set -euo pipefail

VERSION="${SETPOINT_VERSION:-}"
OUT_DIR="${1:-dist/setpoint-windows-amd64}"
APP_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"

if [ -z "$VERSION" ]; then
  echo "SETPOINT_VERSION is required" >&2
  exit 2
fi

if [ -d "$OUT_DIR" ] && [ -n "$(find "$OUT_DIR" -mindepth 1 -maxdepth 1 -print -quit)" ]; then
  echo "output directory must be empty: $OUT_DIR" >&2
  exit 2
fi

mkdir -p "$OUT_DIR" "$OUT_DIR/agents" "$OUT_DIR/logs" "$OUT_DIR/data"
OUT_DIR="$(cd "$OUT_DIR" && pwd)"

(
  cd "$APP_ROOT"
  GOOS=windows GOARCH=amd64 CGO_ENABLED=0 \
    go build -trimpath \
      -ldflags "-s -w -X main.version=$VERSION" \
      -o "$OUT_DIR/setpoint-server.exe" \
      ./cmd/setpoint-server
  SETPOINT_VERSION="$VERSION" bash scripts/package-agent-distribution.sh "$OUT_DIR/agents"
)

cp "$APP_ROOT/packaging/windows/start.bat" "$OUT_DIR/start.bat"
cp "$APP_ROOT/packaging/windows/stop.bat" "$OUT_DIR/stop.bat"
cp "$APP_ROOT/packaging/windows/start.ps1" "$OUT_DIR/start.ps1"
cp "$APP_ROOT/packaging/windows/stop.ps1" "$OUT_DIR/stop.ps1"
printf '%s\n' "$VERSION" > "$OUT_DIR/VERSION"

(
  cd "$OUT_DIR"
  sha256sum setpoint-server.exe start.bat stop.bat start.ps1 stop.ps1 > SHA256SUMS
)
