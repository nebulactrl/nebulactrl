import { afterAll, describe, expect, test } from "bun:test";
import fs from "node:fs";
import os from "node:os";
import path from "node:path";
import { buildCorpus, isExcluded } from "./corpus.ts";

const repoRoot = path.resolve(import.meta.dir, "../../../..");

// ---------------------------------------------------------------- fixtures

const dirs: string[] = [];
function fixture(files: Record<string, string>): string {
	const root = fs.mkdtempSync(path.join(os.tmpdir(), "docs-test-"));
	dirs.push(root);
	for (const [name, body] of Object.entries(files)) {
		const full = path.join(root, "docs", name);
		fs.mkdirSync(path.dirname(full), { recursive: true });
		fs.writeFileSync(full, body);
	}
	fs.writeFileSync(path.join(root, "SECURITY.md"), "# s");
	return root;
}
afterAll(() => {
	for (const dir of dirs) fs.rmSync(dir, { recursive: true, force: true });
});

const page = (title: string, section: string, body: string) =>
	`---\ntitle: ${title}\ndescription: "About ${title}."\nsection: ${section}\norder: 1\n---\n\n${body}\n`;

const build = (root: string) =>
	buildCorpus({
		docsDir: path.join(root, "docs"),
		repoRoot: root,
		openapi: null,
	});

describe("the link checker", () => {
	test("accepts valid page, anchor and repo links", () => {
		const root = fixture({
			"a.md": page(
				"A",
				"Applications",
				"# A\n\n## Setup\n\nSee [b](b.md#details), [up](#setup), [sec](../SECURITY.md).",
			),
			"b.md": page("B", "Applications", "# B\n\n## Details\n\ntext"),
		});
		const corpus = build(root);
		expect(corpus.errors).toEqual([]);
		expect(corpus.entries.get("a")?.page).toMatchObject({ kind: "md" });
	});

	test("fails on a missing page, a missing anchor, an in-page anchor and a missing repo file", () => {
		const root = fixture({
			"a.md": page(
				"A",
				"Applications",
				"# A\n\n[x](gone.md) [y](b.md#nope) [z](#nowhere) [w](../nothing.md)",
			),
			"b.md": page("B", "Applications", "# B\n\n## Details"),
		});
		const errors = build(root).errors;
		expect(errors).toHaveLength(4);
		expect(errors.join("\n")).toContain("gone.md");
		expect(errors.join("\n")).toContain('has no heading "#nope"');
		expect(errors.join("\n")).toContain('"#nowhere" matches no heading');
		expect(errors.join("\n")).toContain("nothing.md");
	});

	test("reports unknown sections, bad frontmatter and colliding slugs", () => {
		const root = fixture({
			"a.md": page("A", "Nonsense", "# A"),
			"b.md": "---\ntitle: [x]\n---\n# B",
			"c/README.md": page("C", "Applications", "# C"),
			"c.md": page("C2", "Applications", "# C2"),
		});
		const errors = build(root).errors.join("\n");
		expect(errors).toContain('unknown section "Nonsense"');
		expect(errors).toContain("not supported");
		expect(errors).toContain("both map to /docs/c");
	});

	test("excludes research notes and the repo index", () => {
		expect(isExcluded("architecture/platform-services-research.md")).toBe(true);
		expect(isExcluded("README.md")).toBe(true);
		expect(isExcluded("operations/README.md")).toBe(false);
		const root = fixture({
			"README.md": "# index",
			"x-research.md": "# notes",
			"a.md": page("A", "Applications", "# A"),
		});
		expect([...build(root).entries.keys()]).toEqual(["a"]);
	});
});

// ---------------------------------------------------------------- the real docs

describe("docs/ in this repository", () => {
	const spec = JSON.parse(
		fs.readFileSync(path.join(repoRoot, "openapi/openapi.json"), "utf8"),
	) as unknown;
	const corpus = buildCorpus({
		docsDir: path.join(repoRoot, "docs"),
		repoRoot,
		openapi: spec,
	});

	test("has no broken internal link or anchor", () => {
		expect(corpus.errors).toEqual([]);
	});

	test("every user-facing page has title, description and section frontmatter", () => {
		expect(corpus.missingFrontmatter).toEqual([]);
	});

	test("compiles the guides, the CLI reference and one API page per tag", () => {
		expect(corpus.entries.has("getting-started/quickstart")).toBe(true);
		expect(corpus.entries.get("reference/cli")?.meta.generated).toBe(true);
		expect(corpus.entries.get("reference/api")?.page.kind).toBe("api");
		const tags = [...corpus.entries.values()].filter(
			(e) => e.meta.kind === "api-tag",
		);
		expect(tags.length).toBeGreaterThan(10);
		expect(corpus.search.length).toBeGreaterThan(corpus.entries.size);
	});

	test("the sidebar lists every page once, in section order", () => {
		const listed: string[] = [];
		const walk = (
			items: {
				slug: string;
				children: { slug: string; children: never[] }[];
			}[],
		) => {
			for (const item of items) {
				listed.push(item.slug);
				walk(item.children as never);
			}
		};
		for (const section of corpus.nav) walk(section.items as never);
		expect(new Set(listed).size).toBe(listed.length);
		expect(listed.sort()).toEqual([...corpus.entries.keys()].sort());
		expect(corpus.nav[0]?.title).toBe("Getting started");
	});

	test("every 'Learn more' target in the app source points at a real page and heading", () => {
		const srcDir = path.join(repoRoot, "apps/web/src");
		const targets: { file: string; target: string }[] = [];
		const visit = (dir: string) => {
			for (const entry of fs.readdirSync(dir, { withFileTypes: true })) {
				const full = path.join(dir, entry.name);
				if (entry.isDirectory()) {
					if (entry.name !== "gen") visit(full);
				} else if (
					/\.(tsx?)$/.test(entry.name) &&
					!entry.name.endsWith(".test.ts")
				) {
					const text = fs.readFileSync(full, "utf8");
					for (const m of text.matchAll(/<DocsLink[^>]*?\sto="([^"]+)"/g)) {
						targets.push({ file: full, target: m[1] as string });
					}
					for (const m of text.matchAll(/<DocsButton\s+path="([^"]+)"/g)) {
						targets.push({
							file: full,
							target: (m[1] as string)
								.replace(/\/README\.md$/i, "")
								.replace(/\.md$/i, ""),
						});
					}
					if (text.includes("SECTION_DOCS")) {
						const block =
							/SECTION_DOCS[^{]*\{([\s\S]*?)\n\};/.exec(text)?.[1] ?? "";
						for (const m of block.matchAll(/:\s*"([^"]+)"/g)) {
							targets.push({ file: full, target: m[1] as string });
						}
					}
				}
			}
		};
		visit(srcDir);
		expect(targets.length).toBeGreaterThan(10);
		const problems: string[] = [];
		for (const { file, target } of targets) {
			const [slug = "", anchor] = target.split("#");
			if (!corpus.entries.has(slug))
				problems.push(`${path.relative(srcDir, file)}: no docs page "${slug}"`);
			else if (anchor && !corpus.headingIds.get(slug)?.has(anchor)) {
				problems.push(
					`${path.relative(srcDir, file)}: "${slug}" has no heading "#${anchor}"`,
				);
			}
		}
		expect(problems).toEqual([]);
	});
});
