#!/usr/bin/env bash
# Shared, local-only provenance for the existing package entry points.
set -euo pipefail

resolve_package_identity() {
  VERSION="${SETPOINT_VERSION:-}"
  local semver='^v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(-[0-9A-Za-z-]+(\.[0-9A-Za-z-]+)*)?(\+[0-9A-Za-z-]+(\.[0-9A-Za-z-]+)*)?$'
  if [[ ! "$VERSION" =~ $semver && ! "$VERSION" =~ ^(public-)?ci-[0-9a-f]{40}$ ]]; then
    echo 'SETPOINT_VERSION must be vSemVer or an existing ci-/public-ci- exact-SHA label' >&2; return 2
  fi
  # SemVer prerelease numeric identifiers cannot contain leading zeros.
  if [[ "$VERSION" =~ $semver && "$VERSION" == *-* ]]; then
    local prerelease="${VERSION#*-}" part
    prerelease="${prerelease%%+*}"
    local -a parts
    IFS=. read -r -a parts <<< "$prerelease"
    for part in "${parts[@]}"; do
      if [[ "$part" =~ ^[0-9]+$ && ${#part} -gt 1 && "$part" == 0* ]]; then
        echo 'invalid numeric SemVer prerelease identifier' >&2; return 2
      fi
    done
  fi
  local root identity tree expected
  root="$(git rev-parse --show-toplevel)"
  if [ -n "$(git -C "$root" status --porcelain)" ]; then
    echo 'package source checkout must be clean' >&2; return 2
  fi
  identity="$(git -C "$root" rev-parse HEAD)"
  if [ -f "$root/.public-mirror/provenance.json" ]; then
    identity="$(sed -n 's/.*"source_private_sha"[[:space:]]*:[[:space:]]*"\([0-9a-f]*\)".*/\1/p' "$root/.public-mirror/provenance.json")"
    expected="$(sed -n 's/.*"source_projection_git_tree_sha256"[[:space:]]*:[[:space:]]*"\([0-9a-f]*\)".*/\1/p' "$root/.public-mirror/provenance.json")"
    tree="$(git -C "$root" ls-tree -r HEAD -- app .gitattributes | sha256sum | awk '{print $1}')"
    if [ "$tree" != "$expected" ]; then echo 'public projection source tree mismatch' >&2; return 2; fi
  fi
  if [[ ! "$identity" =~ ^[0-9a-f]{40}$ ]]; then echo 'exact source SHA unavailable' >&2; return 2; fi
  if [ -n "${SETPOINT_SOURCE_SHA:-}" ] && [ "$SETPOINT_SOURCE_SHA" != "$identity" ]; then
    echo 'SETPOINT_SOURCE_SHA does not match the checked-out source' >&2; return 2
  fi
  SOURCE_SHA="$identity"
  BUILD_LDFLAGS="-s -w -X setpoint/internal/buildinfo.Version=$VERSION -X setpoint/internal/buildinfo.SourceSHA=$SOURCE_SHA"
}

require_empty_package_directory() {
  if [ -d "$1" ] && [ -n "$(find "$1" -mindepth 1 -maxdepth 1 -print -quit)" ]; then
    echo "output directory must be empty: $1" >&2; return 2
  fi
}
