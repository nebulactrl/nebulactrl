/**
 * Writes the release feed into dist/releases/ after the site build:
 *
 *   gh release view "v$VERSION" --json body,publishedAt > release.json
 *   RELEASE_VERSION=$VERSION bun run scripts/release-feed.ts release.json
 *
 * Only the `site` job of .github/workflows/release.yml runs it. A normal build
 * has no release and so no feed. The second argument is the output directory
 * (default: dist, next to this scripts folder).
 */
import fs from "node:fs";
import path from "node:path";
import { fileURLToPath } from "node:url";
import {
	buildFeed,
	type GitHubRelease,
	writeFeed,
} from "./lib/release-feed.ts";

function main() {
	const version = process.env.RELEASE_VERSION;
	const input = process.argv[2];
	if (!version || !input) {
		throw new Error(
			"usage: RELEASE_VERSION=X.Y.Z bun run scripts/release-feed.ts <gh release json> [dist]",
		);
	}
	const dist =
		process.argv[3] ??
		path.resolve(fileURLToPath(new URL(".", import.meta.url)), "..", "dist");
	const release = JSON.parse(fs.readFileSync(input, "utf8")) as GitHubRelease;
	// GITHUB_URL (the repository, as the site build reads it) links the release
	// page; without it notesUrl stays empty.
	const files = writeFeed(
		dist,
		buildFeed(version, release, process.env.GITHUB_URL ?? ""),
	);
	for (const file of files) console.log(`release feed: ${file}`);
}

try {
	main();
} catch (error) {
	console.error(`release feed: ${(error as Error).message}`);
	process.exit(1);
}
