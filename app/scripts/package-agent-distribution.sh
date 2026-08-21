#!/usr/bin/env bash
set -euo pipefail

VERSION="${SETPOINT_VERSION:-}"
OUT_DIR="${1:-agents}"

if [ -z "$VERSION" ]; then
  echo "SETPOINT_VERSION is required" >&2
  exit 2
fi

mkdir -p "$OUT_DIR"
rm -f -- \
  "$OUT_DIR/setpoint-agent-linux-amd64" \
  "$OUT_DIR/setpoint-agent-linux-arm64" \
  "$OUT_DIR/SHA256SUMS" \
  "$OUT_DIR/VERSION"

for arch in amd64 arm64; do
  GOOS=linux GOARCH="$arch" CGO_ENABLED=0 \
    go build -trimpath \
      -ldflags "-s -w -X main.version=$VERSION" \
      -o "$OUT_DIR/setpoint-agent-linux-$arch" \
      ./cmd/setpoint-agent
done

printf '%s\n' "$VERSION" > "$OUT_DIR/VERSION"
(
  cd "$OUT_DIR"
  sha256sum setpoint-agent-linux-amd64 setpoint-agent-linux-arm64 > SHA256SUMS
)
