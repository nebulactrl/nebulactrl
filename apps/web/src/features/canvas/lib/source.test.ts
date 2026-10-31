import { describe, expect, test } from "bun:test";
import { makeService } from "./fixtures";
import { repoName, sourceSubtitle } from "./source";

describe("repoName", () => {
	test("keeps owner/repo from the common clone URL shapes", () => {
		expect(repoName("https://github.com/acme/shop-api.git")).toBe(
			"acme/shop-api",
		);
		expect(repoName("https://github.com/acme/shop-api/")).toBe("acme/shop-api");
		expect(repoName("git@github.com:acme/shop-api.git")).toBe("acme/shop-api");
		expect(repoName("git://nebulactrl-test-git/app.git")).toBe("app");
		expect(repoName("https://gitlab.com/group/sub/repo")).toBe("sub/repo");
	});
});

describe("sourceSubtitle", () => {
	test("an image service shows its reference", () => {
		expect(sourceSubtitle(makeService())).toBe("traefik/whoami:latest");
	});

	test("a git service shows repo@branch and the serving commit", () => {
		const base = makeService();
		const git = makeService({
			sourceType: "git",
			config: {
				...base.config,
				image: "",
				gitUrl: "https://github.com/acme/shop-api.git",
				gitBranch: "main",
			},
			currentRelease: {
				id: "rel-1",
				imageRef: "nc-build/shop-api:abc",
				source: { type: "git", commitSha: "a91c3f2d9e8b" },
			},
		});
		expect(sourceSubtitle(git)).toBe("acme/shop-api@main · a91c3f2");
		expect(sourceSubtitle({ ...git, currentRelease: null })).toBe(
			"acme/shop-api@main",
		);
	});
});
