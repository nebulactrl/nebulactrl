import { describe, expect, test } from "bun:test";
import {
	commitSubject,
	formatWatchPaths,
	parseWatchPaths,
	providerInfo,
	readFlowResult,
	shortSha,
	watchPathProblem,
} from "./model";

describe("watch paths", () => {
	test("parses lines and commas, trims and drops duplicates", () => {
		expect(
			parseWatchPaths(" apps/api/** \n\n!**/*.md, apps/api/**\nDockerfile"),
		).toEqual(["apps/api/**", "!**/*.md", "Dockerfile"]);
	});
	test("round-trips through the textarea", () => {
		const paths = ["apps/web/**", "!docs/**"];
		expect(parseWatchPaths(formatWatchPaths(paths))).toEqual(paths);
		expect(formatWatchPaths(null)).toBe("");
	});
	test("flags parent directory segments", () => {
		expect(watchPathProblem(["apps/api"])).toBeUndefined();
		expect(watchPathProblem(["../x"])).toContain("..");
		expect(watchPathProblem(["!a/../b"])).toContain("..");
	});
});

describe("commits", () => {
	test("short sha and subject", () => {
		expect(shortSha("bffeb74224043ba2feb48e12f2ac4a0a9de3e04c")).toBe(
			"bffeb74",
		);
		expect(commitSubject("Fix it\n\nlong body")).toBe("Fix it");
		expect(commitSubject("x".repeat(100), 10)).toBe("xxxxxxxxx…");
	});
});

describe("flow result", () => {
	test("reads and strips the redirect parameters", () => {
		const r = readFlowResult("?git_connected=abc&x=1");
		expect(r.connected).toBe("abc");
		expect(r.error).toBeNull();
		expect(r.rest).toBe("?x=1");
		expect(readFlowResult("?git_error=boom").error).toBe("boom");
		expect(readFlowResult("").rest).toBe("");
	});
});

describe("providers", () => {
	test("self-hosted providers need an instance URL", () => {
		expect(providerInfo("gitea").instanceUrlRequired).toBe(true);
		expect(providerInfo("forgejo").instanceUrlRequired).toBe(true);
		expect(providerInfo("gitlab").defaultBaseUrl).toBe("https://gitlab.com");
		expect(providerInfo("bitbucket").manualWebhook).toBe(true);
	});
});
