import { describe, expect, test } from "bun:test";
import fs from "node:fs";
import path from "node:path";
import { buildCorpus } from "./corpus.ts";
import type { LinkContext } from "./links.ts";
import { buildLlms, ORIGIN_PLACEHOLDER, rewriteLinks } from "./llms.ts";

const repoRoot = path.resolve(import.meta.dir, "../../../..");
const spec = JSON.parse(
	fs.readFileSync(path.join(repoRoot, "openapi/openapi.json"), "utf8"),
) as unknown;
const corpus = buildCorpus({
	docsDir: path.join(repoRoot, "docs"),
	repoRoot,
	openapi: spec,
});
const out = buildLlms(corpus);
const O = ORIGIN_PLACEHOLDER;

describe("llms.txt", () => {
	const lines = out.index.split("\n");

	test("follows the llmstxt.org shape: H1, blockquote summary, then H2 link lists", () => {
		expect(lines[0]).toBe("# NebulaCtrl");
		expect(lines[1]).toBe("");
		expect(lines[2]?.startsWith("> ")).toBe(true);
		const firstH2 = lines.findIndex((l) => l.startsWith("## "));
		expect(firstH2).toBeGreaterThan(3);
		expect(lines.filter((l) => l.startsWith("# "))).toHaveLength(1);
		// Everything after the first H2 is a heading, a blank line or a list item.
		for (const line of lines.slice(firstH2)) {
			expect(
				line === "" ||
					line.startsWith("## ") ||
					/^- \[[^\]]+\]\([^)]+\)(: .+)?$/.test(line),
			).toBe(true);
		}
		expect(out.index.endsWith("\n")).toBe(true);
	});

	test("starts with Getting started, then CLI, API and MCP; Optional is last and holds the ADRs", () => {
		const headings = lines.filter((l) => l.startsWith("## "));
		expect(headings[0]).toBe("## Getting started");
		expect(headings[1]).toBe("## CLI, API and MCP");
		expect(headings[headings.length - 1]).toBe("## Optional");
		const optional = out.index.slice(out.index.indexOf("## Optional"));
		expect(optional).toContain("0001 Go modular monolith");
		expect(out.index.slice(0, out.index.indexOf("## Optional"))).not.toContain(
			"0001 Go modular monolith",
		);
	});

	test("every link resolves: a page's raw markdown, the OpenAPI document or llms-full.txt", () => {
		const urls = [...out.index.matchAll(/^- \[[^\]]+\]\(([^)]+)\)/gm)].map(
			(m) => m[1] as string,
		);
		expect(urls.length).toBeGreaterThan(40);
		for (const url of urls) {
			expect(url.startsWith(O)).toBe(true);
			const rest = url.slice(O.length);
			if (
				rest === "/api/v1/openapi.json" ||
				rest === "/schema/nebula.toml.json" ||
				rest === "/llms-full.txt"
			)
				continue;
			const match = /^\/docs\/(.+)\.md$/.exec(rest);
			expect(match).not.toBeNull();
			expect(out.pages.has(match?.[1] as string)).toBe(true);
		}
	});

	test("lists every sidebar page once (API areas under Optional)", () => {
		const slugs = [
			...out.index.matchAll(/\]\([^)]*\/docs\/([^)]+)\.md\)/g),
		].map((m) => m[1] as string);
		expect(new Set(slugs).size).toBe(slugs.length);
		expect(slugs.sort()).toEqual([...corpus.entries.keys()].sort());
	});

	test("points agents at the API and MCP", () => {
		expect(out.index).toContain(`(${O}/api/v1/openapi.json)`);
		expect(out.index).toContain(`(${O}/docs/reference/mcp.md)`);
		expect(out.index).toContain(`(${O}/docs/reference/api.md)`);
		expect(out.index).toContain(`(${O}/docs/reference/cli.md)`);
	});

	test("lists the nebula.toml reference, guide and JSON Schema", () => {
		expect(out.index).toContain(
			`- [nebula.toml reference](${O}/docs/reference/nebula-toml.md): `,
		);
		expect(out.index).toContain(
			`- [nebula.toml guide](${O}/docs/operations/project-config.md): `,
		);
		expect(out.index).toContain(
			`- [nebula.toml JSON Schema](${O}/schema/nebula.toml.json): `,
		);
		// llms-full.txt carries both pages and the authoring rules of the guide.
		expect(out.full).toContain(`Source: ${O}/docs/reference/nebula-toml`);
		expect(out.full).toContain(`Source: ${O}/docs/operations/project-config`);
		expect(out.full).toContain("Never write secret values.");
	});
});

describe("llms-full.txt", () => {
	const guides = [...corpus.entries.values()].filter(
		(e) => e.meta.kind !== "api-tag",
	);

	test("contains every user-facing page exactly once, with a title and a Source line", () => {
		for (const { meta } of guides) {
			const header = `\nSource: ${O}/docs/${meta.slug}\n`;
			expect(out.full.split(header).length - 1).toBe(1);
		}
		expect([...out.full.matchAll(/^Source: /gm)]).toHaveLength(guides.length);
	});

	test("leaves out the API endpoint areas but keeps the API intro", () => {
		expect(out.full).not.toContain(`Source: ${O}/docs/reference/api/`);
		expect(out.full).toContain(`Source: ${O}/docs/reference/api\n`);
		expect(out.full).toContain(`${O}/api/v1/openapi.json`);
	});

	test("reads in sidebar order and rewrites internal links to absolute /docs URLs", () => {
		expect(
			out.full.indexOf(
				"Source: " + O + "/docs/getting-started/what-is-nebulactrl",
			),
		).toBeLessThan(
			out.full.indexOf("Source: " + O + "/docs/operations/deployments\n"),
		);
		expect(out.full).toContain(`(${O}/docs/operations/variables#`);
		expect(out.full).not.toMatch(/\]\((?:\.\.?\/)[^)]*\.md/);
	});
});

describe("raw markdown per page", () => {
	test("has no frontmatter, starts with its title and links absolutely", () => {
		for (const [slug, text] of out.pages) {
			expect(text.startsWith("---")).toBe(false);
			expect(text.startsWith("# ")).toBe(true);
			expect(text, slug).not.toMatch(/\]\((?:\.\.?\/)[^)]*\.md/);
		}
		const quick = out.pages.get("getting-started/quickstart") as string;
		expect(quick).toContain(`(${O}/docs/installing.md)`);
		expect(quick).toContain(`(${O}/docs/operations/troubleshooting.md)`);
	});

	test("has a page for every API area, listing its operations", () => {
		const services = out.pages.get("reference/api/deployments") as string;
		expect(services).toContain("## POST /services/{serviceId}/deployments");
		expect(services).toContain("`deployment_in_progress`");
	});
});

describe("rewriteLinks", () => {
	const ctx: LinkContext = {
		fromFile: "a/x.md",
		pages: new Map([["b/y.md", "b/y"]]),
		repoEntry: () => "file",
	};
	test("rewrites page and site links but not code or external links", () => {
		const text = [
			"See [y](../b/y.md#h) and [spec](/api/v1/openapi.json) and [ext](https://example.com/a.md).",
			"Inline `[no](../b/y.md)` stays.",
			"```md",
			"[fenced](../b/y.md)",
			"```",
		].join("\n");
		const raw = rewriteLinks(text, ctx, "raw");
		expect(raw).toContain(`[y](${O}/docs/b/y.md#h)`);
		expect(raw).toContain(`[spec](${O}/api/v1/openapi.json)`);
		expect(raw).toContain("[ext](https://example.com/a.md)");
		expect(raw).toContain("`[no](../b/y.md)`");
		expect(raw).toContain("[fenced](../b/y.md)");
		expect(rewriteLinks(text, ctx, "full")).toContain(`[y](${O}/docs/b/y#h)`);
	});
});

describe("rewriteLinks without a public repository", () => {
	test("a link to a repository file becomes its text", () => {
		const hiddenCtx: LinkContext = {
			fromFile: "operations/variables.md",
			pages: new Map(),
			repoEntry: () => "file",
			repoUrl: null,
		};
		expect(
			rewriteLinks(
				"See [the notice](../../NOTICE) and [Loki](https://github.com/grafana/loki).",
				hiddenCtx,
				"raw",
			),
		).toBe("See the notice and [Loki](https://github.com/grafana/loki).");
	});
});
