import { describe, expect, test } from "bun:test";
import { buildSnippets, imageInfo, maskedUrl } from "./snippets";

describe("imageInfo", () => {
	test("tells Docker Hub, GHCR and other registries apart", () => {
		expect(imageInfo("nginx")).toEqual({
			host: "dockerhub",
			repo: "library/nginx",
			tag: "latest",
		});
		expect(imageInfo("acme/api:v2")).toEqual({
			host: "dockerhub",
			repo: "acme/api",
			tag: "v2",
		});
		expect(imageInfo("docker.io/Acme/API:v2")?.repo).toBe("acme/api");
		expect(imageInfo("ghcr.io/acme/api:stable")).toEqual({
			host: "ghcr",
			repo: "acme/api",
			tag: "stable",
		});
		expect(imageInfo("registry.example.com:5000/team/app")).toEqual({
			host: "other",
			repo: "registry.example.com:5000/team/app",
			tag: "latest",
		});
	});

	test("a digest-pinned image has no moving tag", () => {
		expect(imageInfo(`acme/api@sha256:${"a".repeat(64)}`)?.tag).toBe("");
	});

	test("an empty image is not an image", () => {
		expect(imageInfo("  ")).toBeNull();
	});
});

describe("buildSnippets", () => {
	const url = "https://nebula.example.com/hooks/deploy/abc/ndh_secret";
	const byId = (image: string) =>
		Object.fromEntries(buildSnippets({ url, image }).map((s) => [s.id, s]));

	test("offers the five setups, each carrying the URL", () => {
		const all = buildSnippets({ url, image: "ghcr.io/acme/api:v1" });
		expect(all.map((s) => s.id)).toEqual([
			"curl",
			"github",
			"gitlab",
			"dockerhub",
			"ghcr",
		]);
		expect(byId("ghcr.io/acme/api:v1").curl?.body).toContain(url);
		expect(byId("ghcr.io/acme/api:v1").dockerhub?.body).toContain(url);
		expect(byId("ghcr.io/acme/api:v1").ghcr?.body).toContain(url);
	});

	test("CI snippets keep the URL in a secret, not in the file", () => {
		const s = byId("acme/api:v1");
		expect(s.github?.body).toContain("secrets.NEBULACTRL_DEPLOY_HOOK");
		expect(s.github?.body).not.toContain(url);
		expect(s.gitlab?.body).toContain("$NEBULACTRL_DEPLOY_HOOK");
		expect(s.gitlab?.body).not.toContain(url);
	});

	test("warns when the registry does not host the service's image", () => {
		const s = byId("ghcr.io/acme/api:v1");
		expect(s.ghcr?.caution).toBeUndefined();
		expect(s.dockerhub?.caution).toContain("GHCR");
		expect(byId("acme/api:v1").ghcr?.caution).toContain("Docker Hub");
	});

	test("a git service gets no tag examples and a registry caution", () => {
		const s = byId("");
		expect(s.curl?.body).not.toContain("?tag=");
		expect(s.ghcr?.caution).toContain("builds from git");
	});

	test("names the repository and tag a registry push must match", () => {
		expect(byId("acme/api:v1").dockerhub?.body).toContain("acme/api:v1");
	});
});

describe("maskedUrl", () => {
	test("hides the secret", () => {
		expect(maskedUrl("https://h/hooks/deploy/x/<secret>")).not.toContain(
			"<secret>",
		);
	});
});
