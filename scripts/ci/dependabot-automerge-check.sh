#!/usr/bin/env bash
# The decision of .github/workflows/dependabot-automerge.yml: may Dependabot's pull
# request be merged without a human? The workflow runs it from the base commit, so
# the pull request cannot change the rule it is judged by. Tested by
# scripts/ci/test-dependabot-automerge.sh (also run by check-workflows.sh).
#
#   BRANCH, BODY, REPO, PR, GH_TOKEN, AUTOMERGE_GROUPS, GITHUB_OUTPUT set   # the workflow
#   source scripts/ci/dependabot-automerge-check.sh                         # the functions only
#
# The pull request is eligible when it holds one verified commit by Dependabot and
# every update in its metadata block is a patch update that is either
#   - in a group of AUTOMERGE_GROUPS, or
#   - in no group, but the pull request's ecosystem and directory (read from the
#     branch dependabot/<ecosystem>/<directory>/<name>) have their *-patch group in
#     AUTOMERGE_GROUPS. Dependabot leaves an update outside the group when the
#     group's update-types do not match, as for a digest-only update of a pinned
#     image (a "3.1.2 -> 3.1.2" bump is semver-patch with no group), and the same
#     goes for a security update, which the pull request body gives away and which
#     is always reviewed by hand.
# Go modules and Bun have no entry in group_of_branch on purpose: Go modules have no
# patch group and an ungrouped Bun update does not say whether it is a runtime
# dependency.

# The *-patch group of .github/dependabot.yml for the ecosystem and directory in a
# Dependabot branch name; nothing for the ecosystems that stay manual.
group_of_branch() {
	case "$1" in
	dependabot/github_actions/*) echo github-actions-patch ;;
	dependabot/docker_compose/deploy/install/*) echo compose-install-patch ;;
	dependabot/docker_compose/deploy/dev/*) echo compose-dev-patch ;;
	dependabot/docker/deploy/agent/* | dependabot/docker/tests/fixtures/git-server/*) echo docker-dev-patch ;;
	dependabot/docker/*) echo docker-image-patch ;;
	esac
}

# Dependabot's metadata block at the end of a commit message, one line
# "<group> <update-type>" per dependency, "-" for a missing field.
parse_updates() {
	awk '
		/^updated-dependencies:$/ { block = 1; next }
		block && /^\.\.\.$/ { exit }
		block && /^- dependency-name:/ { n++ }
		block && /^  update-type:/ { type[n] = $2 }
		block && /^  dependency-group:/ { group[n] = $2 }
		END { for (i = 1; i <= n; i++) print (i in group ? group[i] : "-") " " (i in type ? type[i] : "-") }
	' <<<"$1"
}

# decide <branch> <pull request body> <updates>: prints why not and returns 1, or
# returns 0. Reads AUTOMERGE_GROUPS.
decide() {
	local branch="$1" body="$2" updates="$3" ungrouped group type
	[ -n "$updates" ] || {
		echo "no updated-dependencies metadata in the commit message"
		return 1
	}
	ungrouped="$(group_of_branch "$branch")"
	while read -r group type; do
		if [ "$group" = - ]; then
			[ -n "$ungrouped" ] || {
				echo "ungrouped update on branch '$branch', whose ecosystem and directory stay manual"
				return 1
			}
			if grep -qiE 'includes security fixes|vulnerabilities fixed' <<<"$body"; then
				echo "a security update is always reviewed by hand"
				return 1
			fi
			group="$ungrouped"
		fi
		case " $AUTOMERGE_GROUPS " in
		*" $group "*) ;;
		*)
			echo "group '$group' is not in AUTOMERGE_GROUPS"
			return 1
			;;
		esac
		[ "$type" = version-update:semver-patch ] || {
			echo "'$type' in group '$group' is not a patch update"
			return 1
		}
	done <<<"$updates"
}

main() {
	local commits updates why
	eligible() {
		echo "eligible=$1" >>"$GITHUB_OUTPUT"
		[ "$1" = true ] || echo "::notice::not auto-merged: $2"
		exit 0
	}

	commits="$(gh api "repos/$REPO/pulls/$PR/commits")"
	jq -e 'length == 1 and .[0].author.login == "dependabot[bot]" and .[0].commit.verification.verified' \
		<<<"$commits" >/dev/null || eligible false "the branch must hold exactly one verified commit by Dependabot"

	updates="$(parse_updates "$(jq -r '.[0].commit.message' <<<"$commits")")"
	why="$(decide "$BRANCH" "$BODY" "$updates")" || eligible false "$why"
	eligible true
}

if [ "${BASH_SOURCE[0]}" = "$0" ]; then
	set -euo pipefail
	main
fi
