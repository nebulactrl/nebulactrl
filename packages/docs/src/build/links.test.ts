import { describe, expect, test } from "bun:test";
import { type LinkContext, resolveLink } from "./links";
import { fileSlug, Slugger, slugifyHeading } from "./slug";

const pages = new Map([
	["operations/variables.md", "operations/variables"],
	["operations/networking.md", "operations/networking"],
	["installing/README.md", "installing"],
	["reference/api.md", "reference/api"],
]);

const ctx = (fromFile: string): LinkContext => ({
	fromFile,
	pages,
	repoEntry: (p) => {
		if (p === "SECURITY.md" || p === "deploy/install/install.sh") return "file";
		if (p === "deploy/install") return "dir";
		return null;
	},
});

describe("resolveLink", () => {
	test("rewrites a relative .md link to its /docs path and keeps the anchor", () => {
		expect(
			resolveLink(
				"../operations/variables.md#syntax",
				ctx("installing/servers.md"),
			),
		).toEqual({
			kind: "page",
			href: "/docs/operations/variables#syntax",
			slug: "operations/variables",
			anchor: "syntax",
		});
		expect(
			resolveLink("networking.md", ctx("operations/variables.md")),
		).toMatchObject({
			href: "/docs/operations/networking",
		});
	});

	test("a folder README is the folder's page", () => {
		expect(
			resolveLink("../installing/README.md", ctx("operations/variables.md")),
		).toMatchObject({
			href: "/docs/installing",
		});
		expect(
			resolveLink("../installing/", ctx("operations/variables.md")),
		).toMatchObject({
			href: "/docs/installing",
		});
	});

	test("keeps in-page anchors and external URLs", () => {
		expect(resolveLink("#syntax", ctx("operations/variables.md"))).toEqual({
			kind: "anchor",
			href: "#syntax",
			anchor: "syntax",
		});
		expect(resolveLink("https://example.com/x", ctx("a.md"))).toEqual({
			kind: "external",
			href: "https://example.com/x",
		});
		expect(resolveLink("mailto:a@b.c", ctx("a.md")).kind).toBe("external");
	});

	test("links that leave docs/ become GitHub links when the path exists", () => {
		expect(
			resolveLink("../../SECURITY.md", ctx("installing/servers.md")),
		).toEqual({
			kind: "repo",
			href: "https://github.com/nebulactrl/nebulactrl/blob/main/SECURITY.md",
		});
		expect(
			resolveLink("../../deploy/install", ctx("installing/servers.md")),
		).toEqual({
			kind: "repo",
			href: "https://github.com/nebulactrl/nebulactrl/tree/main/deploy/install",
		});
	});

	test("a docs file that is not a page (excluded) links to its GitHub path", () => {
		const withFile: LinkContext = {
			...ctx("a.md"),
			repoEntry: (p) =>
				p === "docs/architecture/platform-services-research.md" ? "file" : null,
		};
		expect(
			resolveLink("architecture/platform-services-research.md", withFile),
		).toMatchObject({
			kind: "repo",
			href: "https://github.com/nebulactrl/nebulactrl/blob/main/docs/architecture/platform-services-research.md",
		});
	});

	test("reports targets that do not exist", () => {
		expect(resolveLink("missing.md", ctx("operations/variables.md")).kind).toBe(
			"error",
		);
		expect(
			resolveLink("../../nope.md", ctx("operations/variables.md")).kind,
		).toBe("error");
		expect(resolveLink("/api/v1/me", ctx("a.md")).kind).toBe("error");
		expect(
			resolveLink("../../../etc/passwd", ctx("operations/variables.md")).kind,
		).toBe("error");
	});
});

describe("slugs", () => {
	test("file slugs", () => {
		expect(fileSlug("operations/variables.md")).toBe("operations/variables");
		expect(fileSlug("installing/README.md")).toBe("installing");
		expect(fileSlug("README.md")).toBe("");
	});

	test("heading slugs follow GitHub's, so existing anchors keep working", () => {
		expect(slugifyHeading("IP rules (allow and deny)")).toBe(
			"ip-rules-allow-and-deny",
		);
		expect(slugifyHeading("3. TLS")).toBe("3-tls");
		expect(slugifyHeading("What `docker run` does")).toBe(
			"what-docker-run-does",
		);
	});

	test("duplicates get a numeric suffix", () => {
		const slugger = new Slugger();
		expect([
			slugger.next("Options"),
			slugger.next("Options"),
			slugger.next("Options"),
		]).toEqual(["options", "options-1", "options-2"]);
	});
});

describe("a context without a public repository (repoUrl: null)", () => {
	const hidden = (fromFile: string): LinkContext => ({
		...ctx(fromFile),
		repoUrl: null,
	});

	test("repo links are still verified, but carry no href", () => {
		expect(
			resolveLink("../../SECURITY.md", hidden("installing/servers.md")),
		).toEqual({ kind: "repo", href: null });
		expect(
			resolveLink("../../nope.md", hidden("installing/servers.md")).kind,
		).toBe("error");
	});

	test("an explicit link to the default repository is hidden too; other external links stay", () => {
		expect(
			resolveLink(
				"https://github.com/nebulactrl/nebulactrl/blob/main/NOTICE",
				hidden("a.md"),
			),
		).toEqual({ kind: "repo", href: null });
		expect(
			resolveLink("https://github.com/grafana/loki", hidden("a.md")).kind,
		).toBe("external");
	});

	test("a custom repoUrl replaces the default base", () => {
		expect(
			resolveLink("../../SECURITY.md", {
				...ctx("installing/servers.md"),
				repoUrl: "https://git.example/o/r",
			}),
		).toEqual({
			kind: "repo",
			href: "https://git.example/o/r/blob/main/SECURITY.md",
		});
	});
});
