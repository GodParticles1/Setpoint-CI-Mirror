#!/usr/bin/env bash
# Build artifacts only. This command never creates tags or GitHub Releases.
set -euo pipefail
APP_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
if [[ "${SETPOINT_VERSION:-}" != v* ]]; then
  echo 'formal release packaging requires a v-prefixed semantic version' >&2; exit 2
fi
exec bash "$APP_ROOT/scripts/package-windows-server.sh" "${1:-.cache/release}"
