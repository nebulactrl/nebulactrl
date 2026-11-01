#!/usr/bin/env bash
# Dead-code check for the Bun workspace (apps/*, packages/*): unused files,
# exports, dependencies and unlisted dependencies. Configuration: knip.json at
# the repo root. Exits non-zero when knip reports anything.
#
#   scripts/ci/knip.sh
set -euo pipefail

cd "$(dirname "${BASH_SOURCE[0]}")/../.."

bun install --frozen-lockfile
exec bunx knip --no-progress
