// semantic-release configuration. It runs only from .github/workflows/release.yml
// (Actions -> Release -> Run workflow) through scripts/release/semantic-release.sh,
// which pins the exact tool versions and runs it with npx. Nothing here is in the
// bun lockfile and nothing is published to npm.
//
// The releases are the GitHub Release plus its tag. There is no changelog file
// and no version commit: semantic-release never pushes a commit, and the version
// lives in the tag only (binaries and images are stamped from it by the workflow).
//
// WE STAY ON 0.x. semantic-release's first release would be 1.0.0 whatever the
// commits say, so the workflow creates v0.1.0 itself when no v* tag exists
// (see release.yml, job "version"). After that, the rules below decide:
//
//   breaking change (feat!: / BREAKING CHANGE: footer)   -> minor   (0.1.0 -> 0.2.0)
//   feat                                                 -> minor
//   fix, perf, revert                                    -> patch
//   everything else (docs, chore, ci, test, build ...)   -> no release
//
// Semantic Versioning treats 0.y.z as unstable: a minor may break. The default
// "breaking -> major" mapping would cut 1.0.0 by accident, so it is overridden
// on purpose. To go to 1.0: that is a deliberate, manual decision, see "Going to
// 1.0" in CONTRIBUTING.md (change the first rule below to `release: "major"`,
// then release).
const analyzerPreset = "conventionalcommits";

export default {
	branches: ["main"],
	tagFormat: "v${version}",
	plugins: [
		[
			"@semantic-release/commit-analyzer",
			{
				preset: analyzerPreset,
				releaseRules: [
					// 0.x: a breaking change is a minor bump. Set "major" here to go to 1.0
					// (do NOT just delete the rule: a `fix!:` would then be a patch).
					{ breaking: true, release: "minor" },
					{ type: "feat", release: "minor" },
					{ type: "fix", release: "patch" },
					{ type: "perf", release: "patch" },
					{ revert: true, release: "patch" },
				],
			},
		],
		["@semantic-release/release-notes-generator", { preset: analyzerPreset }],
		[
			"@semantic-release/github",
			{
				// Only the release itself: no comments on issues and pull requests,
				// no "released" labels, no failure issues (fewer API calls, no noise).
				successComment: false,
				failComment: false,
				failTitle: false,
				releasedLabels: false,
			},
		],
	],
};
