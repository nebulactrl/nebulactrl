#!/usr/bin/env bash
# Recovers a release whose tag was pushed but whose draft GitHub Release was not
# created (release.yml, job "version"). It looks at the remote for a v* tag that
# points at the commit this run is releasing ($GITHUB_SHA):
#
#   no such tag                         exit 3, nothing to recover
#   tag, a published release exists     "already released": released=false
#   tag, a draft release exists         reuse the draft: released=true
#   tag, no release                     create the draft (generated notes): released=true
#
# Why: semantic-release pushes the tag, then the git note of that tag, and only then
# creates the draft release. When the note push (or anything between the tag and the
# draft) fails, the tag stays at HEAD, a re-run of semantic-release says "no release"
# for it, and the protected tag cannot be deleted. The tag at HEAD is the release this
# run was meant to make, so it is adopted instead.
#
# The remote is the source of truth, not the local tags: semantic-release creates the
# tag locally before pushing it. A tag at the released commit was necessarily made for
# this very commit, so adopting it never releases anything the gates did not test.
#
# Environment: GITHUB_SHA, GITHUB_REPOSITORY, GH_TOKEN, GITHUB_OUTPUT and
# GITHUB_STEP_SUMMARY (all set in GitHub Actions; the last two default to /dev/null
# and stdout elsewhere), DRY_RUN=true to create nothing (released=false).
# Writes version, tag, released and recovered to $GITHUB_OUTPUT.
set -euo pipefail

sha="${GITHUB_SHA:?GITHUB_SHA is required}"
repo="${GITHUB_REPOSITORY:?GITHUB_REPOSITORY is required}"
output="${GITHUB_OUTPUT:-/dev/null}"
summary="${GITHUB_STEP_SUMMARY:-/dev/stdout}"

# `git ls-remote --tags` lists an annotated tag twice: the tag object, and the
# commit it points at as <tag>^{}. A lightweight tag (semantic-release's) once.
tag="$(git ls-remote --tags origin 'refs/tags/v[0-9]*' |
	awk -v sha="$sha" '{ ref = $2; sub(/\^\{\}$/, "", ref); sub(/^refs\/tags\//, "", ref); if ($1 == sha) print ref }' |
	sort -uV | tail -n 1)"
if [ -z "$tag" ]; then
	echo "No v* tag at ${sha::7} on the remote: nothing to recover."
	exit 3
fi
version="${tag#v}"

# The release list shows drafts too (the token can write contents); `gh release
# view <tag>` does not reliably find a draft.
states="$(gh api --paginate "repos/$repo/releases?per_page=100" |
	jq -r --arg tag "$tag" '.[] | select(.tag_name == $tag) | if .draft then "draft" else "published" end')"

{
	echo "version=$version"
	echo "tag=$tag"
	echo "recovered=true"
} >>"$output"

case "$states" in
*published*)
	echo "released=false" >>"$output"
	echo "Already released: $tag points at ${sha::7} and its release is published. Nothing to do." | tee -a "$summary"
	exit 0
	;;
*draft*) state=draft ;;
*) state=none ;;
esac

if [ "${DRY_RUN:-false}" = true ]; then
	echo "released=false" >>"$output"
	case "$state" in
	draft) echo "Dry run: $tag already points at ${sha::7} and has a draft release; a real run would continue with that draft. Nothing was created." | tee -a "$summary" ;;
	none) echo "Dry run: $tag already points at ${sha::7} and has no release; a real run would create its draft release (generated notes). Nothing was created." | tee -a "$summary" ;;
	esac
	exit 0
fi

if [ "$state" = draft ]; then
	echo "Reusing the draft release of $tag (tag at ${sha::7}, left by an interrupted run)" | tee -a "$summary"
else
	# Draft: assets are attached by later jobs, `publish` releases it. The notes come
	# from GitHub (the commits since the previous tag), not from semantic-release.
	args=(--draft --verify-tag --title "$tag" --generate-notes)
	# The first release carries the same note as the one made by the bootstrap step.
	if [ "$tag" = v0.1.0 ]; then
		args+=(--notes "First tagged release. NebulaCtrl is on 0.x: a minor version may contain breaking changes.")
	fi
	gh release create "$tag" "${args[@]}"
	echo "Created draft $tag for the tag already at ${sha::7} (recovered an interrupted release)" | tee -a "$summary"
fi
echo "released=true" >>"$output"
