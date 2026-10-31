#!/usr/bin/env bash
# Checks the version bumps release.config.mjs produces while we are on 0.x, with
# the real semantic-release (pinned by scripts/release/semantic-release.sh) in
# --dry-run mode against a scratch repository under /tmp. It never touches this
# repository, never publishes and needs no GitHub token: the scratch config
# reuses only the commit-analyzer entry of the real
# config (the notes generator and the github plugin need a GitHub repository).
#
#   scripts/release/test-release-rules.sh
#
# Needs git and Node >= 22.14 (npx downloads semantic-release on first use).
# Run it after every change to release.config.mjs or to the pinned versions.
set -euo pipefail

root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
wrapper="$root/scripts/release/semantic-release.sh"
tmp="$(mktemp -d "${TMPDIR:-/tmp}/nebulactrl-release-rules.XXXXXX")"
trap 'rm -rf "$tmp"' EXIT

git init -q --bare -b main "$tmp/origin.git"
git init -q -b main "$tmp/work"
cd "$tmp/work"
git config user.name test
git config user.email test@example.invalid
git config commit.gpgsign false
git config tag.gpgsign false
git remote add origin "$tmp/origin.git"

cat >release.config.mjs <<JS
import real from "$root/release.config.mjs";
// The real commit-analyzer entry only: the notes generator needs a GitHub-style repository URL.
export default { branches: real.branches, tagFormat: real.tagFormat, plugins: real.plugins.slice(0, 1) };
JS
git add release.config.mjs
git commit -q -m "chore: scratch repository"
git tag -a v0.1.0 -m v0.1.0
git push -q origin main v0.1.0
base="$(git rev-parse HEAD)"

fail=0
# expect <name> <expected next version, or "none"> <commit message>
expect() {
	local name="$1" want="$2" msg="$3" out got
	git reset -q --hard "$base"
	git commit -q --allow-empty -m "$msg"
	git push -q -f origin main
	out="$("$wrapper" --dry-run --no-ci 2>&1)" || {
		echo "FAIL  $name: semantic-release exited non-zero" >&2
		echo "$out" >&2
		fail=1
		return
	}
	got="$(sed -n 's/.*next release version is \([0-9][0-9.]*\).*/\1/p' <<<"$out" | head -n 1)"
	[ -n "$got" ] || got=none
	if [ "$got" = "$want" ]; then
		printf 'ok    %-34s -> %s\n' "$name" "$got"
	else
		printf 'FAIL  %-34s -> %s (wanted %s)\n' "$name" "$got" "$want" >&2
		echo "$out" >&2
		fail=1
	fi
}

expect "feat!: breaking (header)" 0.2.0 "feat!: drop the v1 API"
expect "fix!: breaking (header)" 0.2.0 "fix(api)!: rename a field"
expect "BREAKING CHANGE footer" 0.2.0 $'fix: tighten validation\n\nBREAKING CHANGE: invalid hostnames are now rejected'
expect "feat" 0.2.0 "feat(web): add a servers page"
expect "fix" 0.1.1 "fix(agent): retry the enrollment"
expect "perf" 0.1.1 "perf(db): index deployments by service"
expect "revert" 0.1.1 $'revert: "feat: add a servers page"\n\nThis reverts commit 0123456789abcdef.'
expect "docs: no release" none "docs: fix a typo"
expect "chore: no release" none "chore(deps): bump a library"
expect "ci: no release" none "ci: pin the runner"

# Several commits: the highest bump wins, and no breaking change ever gives 1.0.0.
git reset -q --hard "$base"
git commit -q --allow-empty -m "fix: a"
git commit -q --allow-empty -m "feat!: b"
git commit -q --allow-empty -m "feat: c"
git push -q -f origin main
out="$("$wrapper" --dry-run --no-ci 2>&1)" || { echo "$out" >&2; fail=1; }
got="$(sed -n 's/.*next release version is \([0-9][0-9.]*\).*/\1/p' <<<"$out" | head -n 1)"
if [ "$got" = 0.2.0 ]; then
	printf 'ok    %-34s -> %s\n' "mixed: fix + feat! + feat" "$got"
else
	printf 'FAIL  %-34s -> %s (wanted 0.2.0)\n' "mixed: fix + feat! + feat" "${got:-none}" >&2
	echo "$out" >&2
	fail=1
fi

[ "$fail" -eq 0 ] || exit 1
echo "all release rules hold"
