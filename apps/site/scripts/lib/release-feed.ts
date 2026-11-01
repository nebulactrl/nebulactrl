import fs from "node:fs";
import path from "node:path";

/**
 * The release feed a control plane reads to learn that a newer version exists:
 * https://nebulactrl.com/releases/latest.json and releases/<version>.json (the
 * same object). A control plane parses it (internal/sysupdate), so the field
 * names are a contract: do not rename them. `minAgent` is part of the contract
 * too, but no source for it exists yet, so the generator never writes it.
 */
export interface ReleaseFeed {
	/** The release without the `v`: "0.1.2". */
	version: string;
	/** ISO 8601, UTC, whole seconds. */
	publishedAt: string;
	/**
	 * The public release page of this version on GitHub; empty when the build
	 * has no GITHUB_URL.
	 */
	notesUrl: string;
	/** Markdown, trimmed, at most NOTES_LIMIT characters plus the cut notice. */
	notes: string;
}

/** What `gh release view v<version> --json body,publishedAt` prints. */
export interface GitHubRelease {
	body: string | null;
	publishedAt: string;
}

/** Longest notes body kept. */
export const NOTES_LIMIT = 20000;

const VERSION = /^\d+\.\d+\.\d+(-[0-9A-Za-z.-]+)?$/;

/**
 * The release page of `version` under `repoUrl` (e.g.
 * https://github.com/nebulactrl/nebulactrl), or "" without one.
 */
export function notesUrl(version: string, repoUrl = ""): string {
	const base = repoUrl.trim().replace(/\/+$/, "");
	return base ? `${base}/releases/tag/v${version}` : "";
}

/** Normalises line endings, trims, and cuts at NOTES_LIMIT on a line boundary. */
export function trimNotes(body: string | null, url: string): string {
	const text = (body ?? "").replace(/\r\n?/g, "\n").trim();
	if (text.length <= NOTES_LIMIT) return text;
	let cut = text.slice(0, NOTES_LIMIT);
	const lastLine = cut.lastIndexOf("\n");
	// Keep whole lines unless that would throw away most of the allowance.
	if (lastLine >= NOTES_LIMIT / 2) cut = cut.slice(0, lastLine);
	// Never leave half of a surrogate pair at the end.
	const last = cut.charCodeAt(cut.length - 1);
	if (last >= 0xd800 && last <= 0xdbff) cut = cut.slice(0, -1);
	return `${cut.trimEnd()}\n\nThe notes are cut here.${url ? ` The full release notes: ${url}` : ""}`;
}

export function buildFeed(
	version: string,
	release: GitHubRelease,
	repoUrl = "",
): ReleaseFeed {
	if (!VERSION.test(version))
		throw new Error(`release version "${version}" is not X.Y.Z`);
	const published = new Date(release.publishedAt);
	if (Number.isNaN(published.getTime()))
		throw new Error(
			`publishedAt "${release.publishedAt}" of the release is not a date`,
		);
	const url = notesUrl(version, repoUrl);
	return {
		version,
		publishedAt: published.toISOString().replace(/\.\d{3}Z$/, "Z"),
		notesUrl: url,
		notes: trimNotes(release.body, url),
	};
}

/**
 * Writes `<dist>/releases/latest.json` and `<dist>/releases/<version>.json`
 * and returns the files, relative to `dist`. Call it after the site build: the
 * build empties `dist`, and a normal build has no feed at all.
 */
export function writeFeed(dist: string, feed: ReleaseFeed): string[] {
	const body = `${JSON.stringify(feed, null, 2)}\n`;
	const files = ["releases/latest.json", `releases/${feed.version}.json`];
	fs.mkdirSync(path.join(dist, "releases"), { recursive: true });
	for (const file of files) fs.writeFileSync(path.join(dist, file), body);
	return files;
}
