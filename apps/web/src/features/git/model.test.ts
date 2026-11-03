import { afterEach, describe, expect, spyOn, test } from "bun:test";
import {
	canReadCi,
	commitSubject,
	formatWatchPaths,
	parseWatchPaths,
	providerInfo,
	readFlowResult,
	shortSha,
	submitManifestForm,
	watchPathProblem,
} from "./model";

describe("canReadCi", () => {
	test("every provider with an API can, Bitbucket and webhook-only cannot", () => {
		expect(canReadCi("github", "github_app")).toBe(true);
		expect(canReadCi("gitlab", "oauth")).toBe(true);
		expect(canReadCi("gitea", "token")).toBe(true);
		expect(canReadCi("bitbucket", "webhook_only")).toBe(false);
		expect(canReadCi("gitea", "webhook_only")).toBe(false);
		expect(canReadCi(undefined, undefined)).toBe(true);
	});
});

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
	test("a name that merely contains dots is fine", () => {
		expect(watchPathProblem(["a/..b/c", "docs/v1..v2/**"])).toBeUndefined();
	});
	test("allows 50 paths and rejects 51", () => {
		const paths = Array.from({ length: 50 }, (_, i) => `p${i}`);
		expect(watchPathProblem(paths)).toBeUndefined();
		expect(watchPathProblem([...paths, "p50"])).toBe("At most 50 watch paths.");
	});
	test("allows 255 characters and rejects 256, naming the start of the pattern", () => {
		expect(watchPathProblem(["a".repeat(255)])).toBeUndefined();
		expect(watchPathProblem(["a".repeat(256)])).toBe(
			`"${"a".repeat(20)}…" is too long.`,
		);
	});
	test("a negation prefix counts toward the length", () => {
		expect(watchPathProblem([`!${"a".repeat(255)}`])).toContain("too long");
	});
	test("an empty list is valid and formats to nothing", () => {
		expect(watchPathProblem([])).toBeUndefined();
		expect(formatWatchPaths(undefined)).toBe("");
		expect(parseWatchPaths("  \n , ")).toEqual([]);
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
	test("a subject exactly at the limit is not cut, and one over is", () => {
		expect(commitSubject("x".repeat(72))).toBe("x".repeat(72));
		expect(commitSubject("x".repeat(73))).toBe(`${"x".repeat(71)}…`);
	});
	test("subject is trimmed and an empty message gives an empty subject", () => {
		expect(commitSubject("  hi  \nbody")).toBe("hi");
		expect(commitSubject("")).toBe("");
	});
	test("a short sha is returned whole", () => {
		expect(shortSha("abc")).toBe("abc");
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
	test("reads a notice and keeps other parameters in order", () => {
		const r = readFlowResult("?a=1&git_notice=Install+it&b=2");
		expect(r.notice).toBe("Install it");
		expect(r.connected).toBeNull();
		expect(r.rest).toBe("?a=1&b=2");
	});
	test("an empty flow parameter counts as absent but is still stripped", () => {
		const r = readFlowResult("?git_error=&x=1");
		expect(r.error).toBeNull();
		expect(r.rest).toBe("?x=1");
	});
	test("when only flow parameters are present nothing is left", () => {
		expect(readFlowResult("?git_connected=c1").rest).toBe("");
	});
});

describe("providers", () => {
	test("self-hosted providers need an instance URL", () => {
		expect(providerInfo("gitea").instanceUrlRequired).toBe(true);
		expect(providerInfo("forgejo").instanceUrlRequired).toBe(true);
		expect(providerInfo("gitlab").defaultBaseUrl).toBe("https://gitlab.com");
		expect(providerInfo("bitbucket").manualWebhook).toBe(true);
	});
	test("GitHub and GitLab work without an instance URL", () => {
		expect(providerInfo("github").instanceUrlRequired).toBe(false);
		expect(providerInfo("gitlab").instanceUrlRequired).toBe(false);
		expect(providerInfo("github").manualWebhook).toBe(false);
	});
	test("an unknown provider keeps its id as label and is treated as self-hosted", () => {
		const info = providerInfo("sourcehut");
		expect(info.label).toBe("sourcehut");
		expect(info.instanceUrlRequired).toBe(true);
		expect(info.manualWebhook).toBe(false);
	});
});

describe("submitManifestForm", () => {
	afterEach(() => {
		document.body.innerHTML = "";
	});

	test("POSTs the manifest as a hidden field to GitHub's URL", () => {
		const submit = spyOn(
			HTMLFormElement.prototype,
			"submit",
		).mockImplementation(() => {});
		try {
			submitManifestForm(
				"https://github.com/settings/apps/new?state=s",
				'{"a":1}',
			);
			expect(submit).toHaveBeenCalledTimes(1);
			const form = document.body.querySelector("form");
			expect(form?.method).toBe("post");
			expect(form?.action).toBe("https://github.com/settings/apps/new?state=s");
			const field = form?.querySelector("input");
			expect(field?.type).toBe("hidden");
			expect(field?.name).toBe("manifest");
			expect(field?.value).toBe('{"a":1}');
		} finally {
			submit.mockRestore();
		}
	});
});
