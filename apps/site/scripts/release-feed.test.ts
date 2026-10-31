import { afterEach, describe, expect, test } from "bun:test";
import fs from "node:fs";
import os from "node:os";
import path from "node:path";
import {
	buildFeed,
	NOTES_LIMIT,
	notesUrl,
	trimNotes,
	writeFeed,
} from "./lib/release-feed.ts";

const script = path.join(import.meta.dir, "release-feed.ts");
const release = {
	body: "## 0.1.2\n\n### Bug fixes\n\n* fix the thing\n",
	publishedAt: "2026-10-29T12:34:56Z",
};

const tmp: string[] = [];
function scratch(): string {
	const dir = fs.mkdtempSync(path.join(os.tmpdir(), "nebulactrl-feed-"));
	tmp.push(dir);
	return dir;
}
afterEach(() => {
	for (const dir of tmp.splice(0))
		fs.rmSync(dir, { recursive: true, force: true });
});

describe("buildFeed", () => {
	test("has the fields a control plane reads, and no minAgent", () => {
		const feed = buildFeed("0.1.2", release);
		expect(feed).toEqual({
			version: "0.1.2",
			publishedAt: "2026-10-29T12:34:56Z",
			notesUrl: "",
			notes: "## 0.1.2\n\n### Bug fixes\n\n* fix the thing",
		});
		expect("minAgent" in feed).toBe(false);
	});

	test("links the release page under the repository URL, and is empty without one", () => {
		expect(buildFeed("0.1.2", release).notesUrl).toBe("");
		expect(
			buildFeed("0.1.2", release, "https://github.com/acme/app/").notesUrl,
		).toBe("https://github.com/acme/app/releases/tag/v0.1.2");
	});

	test("publishedAt is ISO 8601 in UTC whatever offset GitHub sends", () => {
		const feed = buildFeed("0.1.2", {
			...release,
			publishedAt: "2026-10-29T14:34:56.789+02:00",
		});
		expect(feed.publishedAt).toBe("2026-10-29T12:34:56Z");
	});

	test("rejects a version that is not X.Y.Z and a date that is not a date", () => {
		for (const bad of ["", "v0.1.2", "0.1", "latest", "0.1.2; rm -rf /"])
			expect(() => buildFeed(bad, release)).toThrow("is not X.Y.Z");
		expect(() =>
			buildFeed("0.1.2", { ...release, publishedAt: "yesterday" }),
		).toThrow("not a date");
	});

	test("a release without a body has empty notes", () => {
		expect(buildFeed("0.1.2", { ...release, body: null }).notes).toBe("");
	});
});

describe("trimNotes", () => {
	const url = notesUrl("0.1.2", "https://github.com/acme/app");

	test("trims and normalises line endings", () => {
		expect(trimNotes("\r\n  a\r\nb  \r\n\r\n", url)).toBe("a\nb");
	});

	test("keeps notes at the limit as they are", () => {
		const exact = "x".repeat(NOTES_LIMIT);
		expect(trimNotes(exact, url)).toBe(exact);
	});

	test("cuts longer notes on a line and links the release page when there is one", () => {
		const line = `* ${"y".repeat(98)}`;
		const body = Array.from({ length: 400 }, () => line).join("\n");
		const notes = trimNotes(body, url);
		expect(notes.length).toBeLessThan(NOTES_LIMIT + 200);
		expect(notes.endsWith(`The full release notes: ${url}`)).toBe(true);
		const kept = notes.split("\n\n")[0] ?? "";
		expect(kept.length).toBeLessThanOrEqual(NOTES_LIMIT);
		for (const row of kept.split("\n")) expect(row).toBe(line);
		expect(trimNotes(body, "").endsWith("The notes are cut here.")).toBe(true);
	});

	test("never ends the kept text inside a surrogate pair", () => {
		const body = `${"a".repeat(NOTES_LIMIT - 1)}😀😀`;
		const kept = trimNotes(body, url).split("\n\n")[0] ?? "";
		expect(kept).toBe("a".repeat(NOTES_LIMIT - 1));
	});
});

describe("writeFeed", () => {
	test("writes latest.json and <version>.json with the same object", () => {
		const dist = scratch();
		const feed = buildFeed("0.1.2", release);
		expect(writeFeed(dist, feed)).toEqual([
			"releases/latest.json",
			"releases/0.1.2.json",
		]);
		const latest = fs.readFileSync(
			path.join(dist, "releases/latest.json"),
			"utf8",
		);
		expect(JSON.parse(latest)).toEqual(feed);
		expect(latest.endsWith("\n")).toBe(true);
		const versioned = fs.readFileSync(
			path.join(dist, "releases/0.1.2.json"),
			"utf8",
		);
		expect(versioned).toBe(latest);
	});
});

describe("scripts/release-feed.ts", () => {
	function run(env: Record<string, string>, ...args: string[]) {
		return Bun.spawnSync(["bun", "run", script, ...args], {
			env: { ...process.env, ...env },
			stdout: "pipe",
			stderr: "pipe",
		});
	}

	test("turns `gh release view --json body,publishedAt` into the feed files", () => {
		const dir = scratch();
		const input = path.join(dir, "release.json");
		fs.writeFileSync(input, JSON.stringify(release));
		const dist = path.join(dir, "dist");
		const result = run({ RELEASE_VERSION: "0.1.2" }, input, dist);
		expect(result.exitCode).toBe(0);
		const feed = JSON.parse(
			fs.readFileSync(path.join(dist, "releases/latest.json"), "utf8"),
		);
		expect(feed.version).toBe("0.1.2");
		expect(fs.existsSync(path.join(dist, "releases/0.1.2.json"))).toBe(true);
	});

	test("fails, and writes nothing, without a version or with a bad one", () => {
		const dir = scratch();
		const input = path.join(dir, "release.json");
		fs.writeFileSync(input, JSON.stringify(release));
		const dist = path.join(dir, "dist");
		expect(run({ RELEASE_VERSION: "" }, input, dist).exitCode).toBe(1);
		expect(run({ RELEASE_VERSION: "0.1" }, input, dist).exitCode).toBe(1);
		expect(fs.existsSync(dist)).toBe(false);
	});
});
