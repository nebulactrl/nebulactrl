#!/usr/bin/env bash
# Checks the auto-merge rule of scripts/ci/dependabot-automerge-check.sh against a
# table of (branch, updates, pull request title and body) -> eligible. Needs only bash and awk.
#
#   scripts/ci/test-dependabot-automerge.sh    # also run by scripts/release/check-workflows.sh
#
# An updates cell is a comma-separated list of "<type>" or "<type>:<group>", with
# type one of patch, minor, major; it becomes a Dependabot metadata block.
set -euo pipefail

# shellcheck source=scripts/ci/dependabot-automerge-check.sh
source "$(dirname "${BASH_SOURCE[0]}")/dependabot-automerge-check.sh"

# Keep in step with AUTOMERGE_GROUPS of .github/workflows/dependabot-automerge.yml.
AUTOMERGE_GROUPS="$(sed -nE 's/^ *AUTOMERGE_GROUPS: //p' "$(dirname "${BASH_SOURCE[0]}")/../../.github/workflows/dependabot-automerge.yml")"
[ -n "$AUTOMERGE_GROUPS" ] || {
	echo "FAIL: AUTOMERGE_GROUPS not found in dependabot-automerge.yml" >&2
	exit 1
}

message() {
	local spec type group i=0
	printf 'Bump things\n\n---\nupdated-dependencies:\n'
	local -a specs=()
	IFS=, read -ra specs <<<"$1" || true
	for spec in ${specs[@]+"${specs[@]}"}; do
		i=$((i + 1))
		type="${spec%%:*}"
		group=""
		[ "$spec" = "$type" ] || group="${spec#*:}"
		printf -- '- dependency-name: dep%d\n  dependency-version: 1.0.%d\n  dependency-type: direct:production\n  update-type: version-update:semver-%s\n' "$i" "$i" "$type"
		[ -z "$group" ] || printf '  dependency-group: %s\n' "$group"
	done
	printf '...\n\nSigned-off-by: dependabot[bot] <support@github.com>\n'
}

fail=0
n=0
# expect <true|false> <branch> <updates> [body]
expect() {
	local want="$1" branch="$2" spec="$3" text="${4:-Bump dep from 1.0.0 to 1.0.1}" got why=""
	n=$((n + 1))
	if why="$(decide "$branch" "$text" "$(parse_updates "$(message "$spec")")")"; then got=true; else got=false; fi
	if [ "$got" != "$want" ]; then
		echo "FAIL: $branch [$spec] want eligible=$want, got $got ($why)" >&2
		fail=1
	fi
}

# Grouped patch updates, as before.
expect true dependabot/github_actions/github-actions-patch-0123abc patch:github-actions-patch
expect true dependabot/docker_compose/deploy/install/compose-install-patch-0123abc patch:compose-install-patch,patch:compose-install-patch
expect true dependabot/bun/bun-dev-patch-0123abc patch:bun-dev-patch
# Groups that are not auto-merged, and minor or major updates.
expect false dependabot/github_actions/github-actions-0123abc minor:github-actions
expect false dependabot/github_actions/github-actions-patch-0123abc minor:github-actions-patch
expect false dependabot/go_modules/go-modules-0123abc patch:go-modules
expect false dependabot/bun/bun-runtime-0123abc patch:bun-runtime
expect false dependabot/bun/bun-dev-patch-0123abc patch:bun-dev-patch,major:bun-dev-patch
# Ungrouped patch update (the digest-only bump of PR #57) in the ecosystems with an auto-merged patch group.
expect true dependabot/docker_compose/deploy/install/registry-3.1.2-0123abc patch
expect true dependabot/docker_compose/deploy/dev/postgres-17.5-0123abc patch
expect true dependabot/github_actions/actions/checkout-7.0.2 patch
expect true dependabot/docker/golang-1.26.1 patch
expect true dependabot/docker/deploy/agent/alpine-3.22.1 patch
expect true dependabot/docker/tests/fixtures/git-server/alpine-3.22.1 patch
expect true dependabot/github_actions/actions/checkout-7.0.2 patch,patch:github-actions-patch
# Ungrouped but not patch, or in a manual ecosystem, or no metadata at all.
expect false dependabot/docker_compose/deploy/install/registry-3.2.0-0123abc minor
expect false dependabot/docker_compose/deploy/install/registry-4.0.0-0123abc major
expect false dependabot/docker_compose/deploy/install/registry-3.1.2-0123abc patch,minor
expect false dependabot/go_modules/github.com/jackc/pgx/v5-5.7.6 patch
expect false dependabot/bun/vite-7.1.5 patch
expect false dependabot/docker_compose/redis-8.0.1 patch
expect false dependabot/npm_and_yarn/foo-1.0.1 patch
expect false not-dependabot/github_actions/foo-1.0.1 patch
expect false dependabot/github_actions/actions/checkout-7.0.2 ""
# A security update has no group; Dependabot marks it in the title and the body
# (dependabot-core: message_builder.rb, pr_name_prefixer.rb).
expect false dependabot/github_actions/actions/checkout-7.0.2 patch $'chore(deps): [Security] bump actions/checkout from 7.0.1 to 7.0.2\nBumps checkout.'
expect false dependabot/github_actions/actions/checkout-7.0.2 patch $'🔒 Bump checkout\nBumps checkout.'
expect false dependabot/github_actions/actions/checkout-7.0.2 patch $'chore(deps): bump checkout\nBumps checkout from 7.0.1 to 7.0.2. **This update includes a security fix.**'
expect false dependabot/github_actions/actions/checkout-7.0.2 patch $'chore(deps): bump checkout\nBumps checkout. **This update includes security fixes.**'
expect false dependabot/github_actions/actions/checkout-7.0.2 patch $'chore(deps): bump checkout\n<details>\n<summary>Vulnerabilities fixed</summary>\n<p>GHSA</p>'
# ... but the boilerplate every pull request carries is not a mark.
expect true dependabot/github_actions/actions/checkout-7.0.2 patch $'chore(deps): bump checkout\nBumps checkout.\nYou can disable automated security fix PRs for this repo from the [Security Alerts page](https://example.invalid/network/alerts).'

[ "$fail" -eq 0 ] && echo "dependabot auto-merge rule: $n cases pass"
exit "$fail"
