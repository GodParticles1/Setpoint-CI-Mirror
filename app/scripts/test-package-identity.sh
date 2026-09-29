#!/usr/bin/env bash
set -euo pipefail
source "$(dirname "${BASH_SOURCE[0]}")/package-identity.sh"
fixture="$(mktemp -d)"
trap 'rm -rf -- "$fixture"' EXIT
cd "$fixture"
git init -q
git config user.name 'Identity contract test'
git config user.email 'identity-test@example.invalid'
printf 'fixture\n' > source.txt
git add source.txt
git commit -qm fixture
export SETPOINT_VERSION=v1.2.3-rc.1
resolve_package_identity
[ "$SOURCE_SHA" = "$(git rev-parse HEAD)" ]
if (SETPOINT_SOURCE_SHA=0000000000000000000000000000000000000000 resolve_package_identity); then exit 1; fi
if (SETPOINT_VERSION=v1.2.3-01 resolve_package_identity); then exit 1; fi
if (SETPOINT_VERSION='v1.2.3 -X invalid' resolve_package_identity); then exit 1; fi
printf 'dirty\n' >> source.txt
if (resolve_package_identity); then exit 1; fi
echo 'package identity negative gates: PASS'
