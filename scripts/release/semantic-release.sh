#!/usr/bin/env bash
# Runs semantic-release with exact, pinned tool versions through npx, so none of
# it enters the bun lockfile and nothing needs `npm install`. Arguments go to
# semantic-release unchanged:
#
#   scripts/release/semantic-release.sh --dry-run --no-ci     # local, needs GITHUB_TOKEN
#
# This file is the single place the versions live: release.yml and
# scripts/release/test-release-rules.sh both call it. Bump them by hand
# (npm view <package> version), then run test-release-rules.sh.
#
# Requires Node >= 22.14 (semantic-release 25).
set -euo pipefail

SEMANTIC_RELEASE=25.0.9
COMMIT_ANALYZER=13.0.1
RELEASE_NOTES_GENERATOR=14.1.1
GITHUB_PLUGIN=12.0.10
# The preset's major must match the conventional-changelog-writer the release
# notes generator depends on: generator 14.x uses writer 8, which preset 9.x
# renders with; preset 10.x needs writer 9 ("Missing helper").
CONVENTIONAL_COMMITS_PRESET=9.3.1

# The configuration is release.config.mjs in the current directory (the repo root).
exec npx --yes \
	-p "semantic-release@${SEMANTIC_RELEASE}" \
	-p "@semantic-release/commit-analyzer@${COMMIT_ANALYZER}" \
	-p "@semantic-release/release-notes-generator@${RELEASE_NOTES_GENERATOR}" \
	-p "@semantic-release/github@${GITHUB_PLUGIN}" \
	-p "conventional-changelog-conventionalcommits@${CONVENTIONAL_COMMITS_PRESET}" \
	semantic-release "$@"
