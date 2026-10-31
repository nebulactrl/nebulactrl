#!/usr/bin/env bash
# Enforces the CI policy on .github/workflows (see the header of ci.yml) and lints
# the workflows with actionlint when it is installed:
#
#   scripts/release/check-workflows.sh        # `make lint-workflows`
#
# Fails when a workflow has an automatic trigger, a Docker registry cache option,
# a runner other than the standard GitHub-hosted ubuntu-24.04 / ubuntu-24.04-arm
# (no self-hosted or third-party labels), a job without a timeout, or a remote
# action that is not pinned to a full commit SHA. Comments count too for the
# trigger and cache rules: reword them instead of quoting the forbidden word.
set -euo pipefail

cd "$(dirname "${BASH_SOURCE[0]}")/../.."
fail=0
workflows=(.github/workflows)

# forbid <description> <extended regex>
forbid() {
	local what="$1" re="$2"
	if grep -rnE -e "$re" "${workflows[@]}"; then
		echo "FAIL: $what (above)" >&2
		fail=1
	fi
}

forbid "push trigger" '^[[:space:]]*push:'
forbid "pull_request trigger" 'pull_request'
forbid "schedule trigger" 'schedule:'
forbid "Docker registry cache option" 'cache-(from|to)'

# Only standard GitHub-hosted runners, as a single label (or the matrix value of a
# `runner:` key, which is checked the same way).
while IFS= read -r line; do
	value="$(sed -E 's/^[^:]*:[0-9]+:[[:space:]]*-?[[:space:]]*(runs-on|runner):[[:space:]]*//; s/[[:space:]]+#.*$//; s/[[:space:]]+$//' <<<"$line")"
	case "$value" in
	ubuntu-24.04 | ubuntu-24.04-arm | '${{ matrix.runner }}') ;;
	*)
		echo "$line" >&2
		echo "FAIL: runner must be ubuntu-24.04 or ubuntu-24.04-arm (above)" >&2
		fail=1
		;;
	esac
done < <(grep -rnE '^[[:space:]]*-?[[:space:]]*(runs-on|runner):' "${workflows[@]}" || true)

# Every remote action is pinned to a full 40-hex commit SHA (the tag goes in a
# trailing comment). Local reusable workflows (./...) are exempt.
while IFS= read -r line; do
	ref="$(sed -E 's/^[^:]*:[0-9]+:[[:space:]]*-?[[:space:]]*uses:[[:space:]]*//' <<<"$line")"
	case "$ref" in
	./*) ;;
	*)
		if ! grep -qE '^[^[:space:]@]+@[0-9a-f]{40}([[:space:]]|$)' <<<"$ref"; then
			echo "$line" >&2
			echo "FAIL: action not pinned to a full commit SHA (above)" >&2
			fail=1
		fi
		;;
	esac
done < <(grep -rnE '^[[:space:]]*-?[[:space:]]*uses:' "${workflows[@]}" || true)

# Every job of a workflow needs a timeout.
for f in .github/workflows/*.yml; do
	jobs="$(grep -cE '^  [a-z][a-z0-9-]*:$' <(sed -n '/^jobs:/,$p' "$f"))"
	timeouts="$(grep -cE '^    timeout-minutes:' "$f" || true)"
	calls="$(grep -cE '^    uses: ' "$f" || true)" # reusable-workflow calls carry the callee's timeouts
	if [ "$((timeouts + calls))" -ne "$jobs" ]; then
		echo "FAIL: $f has $jobs jobs but $timeouts timeout-minutes and $calls workflow calls" >&2
		fail=1
	fi
done

if command -v actionlint >/dev/null 2>&1; then
	actionlint
else
	echo "note: actionlint is not installed, skipping (go run github.com/rhysd/actionlint/cmd/actionlint@latest)" >&2
fi

[ "$fail" -eq 0 ] && echo "workflows follow the CI policy"
exit "$fail"
