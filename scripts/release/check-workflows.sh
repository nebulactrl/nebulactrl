#!/usr/bin/env bash
# Checks the rules every workflow in .github/workflows must follow, then lints the
# workflows with actionlint and zizmor, both at the versions pinned below:
#
#   scripts/release/check-workflows.sh        # `make lint-workflows`, and the `workflows` job of ci
#
# Fails when a workflow has a trigger other than workflow_dispatch, workflow_call,
# push, pull_request or schedule (pull_request_target and workflow_run are never
# allowed: they run with the base repository's secrets and token on code from
# forks; branch_protection_rule only in scorecard.yml, which OpenSSF Scorecard
# needs), a runner other than the standard GitHub-hosted ubuntu-24.04 /
# ubuntu-24.04-arm, a job without a timeout, or a remote action that is not pinned
# to a full commit SHA. zizmor findings of medium severity or higher fail it too.
# actionlint needs go and zizmor needs uvx or pipx; without them the check is
# skipped on a workstation and fails when CI is set (GitHub Actions sets it).
set -euo pipefail

actionlint_version=v1.7.12
zizmor_version=1.31.1

cd "$(dirname "${BASH_SOURCE[0]}")/../.."
fail=0
workflows=(.github/workflows)

# The keys of each workflow's top-level `on:` mapping.
allowed_triggers=" workflow_dispatch workflow_call push pull_request schedule "
for f in .github/workflows/*.yml; do
	allowed="$allowed_triggers"
	[ "$f" = .github/workflows/scorecard.yml ] && allowed="$allowed branch_protection_rule "
	if grep -qE '^on:[[:space:]]*[^[:space:]#]' "$f"; then
		echo "$f: write the triggers as a mapping under 'on:'" >&2
		fail=1
		continue
	fi
	triggers="$(awk '/^on:/ { in_on = 1; next } in_on && /^[^[:space:]#]/ { exit } in_on && /^  [a-z_]+:/ { sub(/:.*/, "", $1); print $1 }' "$f")"
	if [ -z "$triggers" ]; then
		echo "FAIL: $f has no triggers" >&2
		fail=1
	fi
	for t in $triggers; do
		case "$allowed" in
		*" $t "*) ;;
		*)
			echo "FAIL: $f: trigger '$t' is not allowed (workflow_dispatch, workflow_call, push, pull_request, schedule)" >&2
			fail=1
			;;
		esac
	done
done

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

# A missing tool is only acceptable on a workstation.
missing() {
	if [ -n "${CI:-}" ]; then
		echo "FAIL: $1" >&2
		fail=1
	else
		echo "note: $1, skipping" >&2
	fi
}

if command -v go >/dev/null 2>&1; then
	go run "github.com/rhysd/actionlint/cmd/actionlint@$actionlint_version" || fail=1
else
	missing "go is not installed (needed for actionlint $actionlint_version)"
fi

if command -v uvx >/dev/null 2>&1; then
	zizmor=(uvx "zizmor@$zizmor_version")
elif command -v pipx >/dev/null 2>&1; then
	zizmor=(pipx run "zizmor==$zizmor_version")
else
	zizmor=()
	missing "neither uvx nor pipx is installed (needed for zizmor $zizmor_version)"
fi
if [ "${#zizmor[@]}" -gt 0 ]; then
	# The online audits (impostor commits, known-vulnerable actions) need a token.
	if [ -z "${GH_TOKEN:-}" ] && command -v gh >/dev/null 2>&1; then
		GH_TOKEN="$(gh auth token 2>/dev/null || true)"
		export GH_TOKEN
	fi
	args=(--min-severity medium)
	[ -n "${GH_TOKEN:-}" ] || args+=(--no-online-audits)
	# .github holds the workflows and dependabot.yml.
	"${zizmor[@]}" "${args[@]}" .github || fail=1
fi

[ "$fail" -eq 0 ] && echo "workflows follow the rules"
exit "$fail"
