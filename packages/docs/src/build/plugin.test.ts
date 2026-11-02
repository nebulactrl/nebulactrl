import { afterEach, describe, expect, test } from "bun:test";
import { EventEmitter } from "node:events";
import fs from "node:fs";
import os from "node:os";
import path from "node:path";
import type { Plugin } from "vite";
import { ORIGIN_PLACEHOLDER } from "./llms.ts";
import { docsPlugin } from "./plugin.ts";

// ---------------------------------------------------------------- fixtures

const roots: string[] = [];
afterEach(() => {
	for (const root of roots.splice(0))
		fs.rmSync(root, { recursive: true, force: true });
});

const page = (title: string, body: string, order = 1) =>
	`---\ntitle: ${title}\ndescription: "About ${title}."\nsection: Applications\norder: ${order}\n---\n\n# ${title}\n\n${body}\n`;

function repo(files: Record<string, string>): string {
	const root = fs.mkdtempSync(path.join(os.tmpdir(), "docs-plugin-"));
	roots.push(root);
	for (const [name, body] of Object.entries(files)) {
		const full = path.join(root, name);
		fs.mkdirSync(path.dirname(full), { recursive: true });
		fs.writeFileSync(full, body);
	}
	return root;
}

const TWO_PAGES = {
	"docs/alpha.md": page("Alpha", "First page, see [beta](beta.md).", 1),
	"docs/beta.md": page("Beta", "## Details\n\nSecond page.", 2),
};

interface Context {
	watched: string[];
	emitted: Array<{ fileName: string; source: string }>;
	errors: string[];
	addWatchFile(file: string): void;
	emitFile(file: { type: string; fileName: string; source: string }): void;
	error(message: string): never;
}

function context(): Context {
	const ctx: Context = {
		watched: [],
		emitted: [],
		errors: [],
		addWatchFile: (file) => {
			ctx.watched.push(file);
		},
		emitFile: (file) => {
			ctx.emitted.push({ fileName: file.fileName, source: file.source });
		},
		error: (message) => {
			ctx.errors.push(message);
			throw new Error(message);
		},
	};
	return ctx;
}

type Hook = (this: Context, ...args: never[]) => unknown;
const hook = (plugin: Plugin, name: keyof Plugin): Hook =>
	plugin[name] as unknown as Hook;

function resolve(plugin: Plugin, id: string): unknown {
	return (hook(plugin, "resolveId") as unknown as (id: string) => unknown)(id);
}

function load(plugin: Plugin, id: string, ctx = context()): string | null {
	return (
		hook(plugin, "load") as unknown as (
			this: Context,
			id: string,
		) => string | null
	).call(ctx, id);
}

/** Evaluates a virtual module's source and returns what it exports. */
async function evaluate(source: string): Promise<Record<string, unknown>> {
	const url = `data:text/javascript;base64,${Buffer.from(source).toString("base64")}`;
	return (await import(url)) as Record<string, unknown>;
}

// ---------------------------------------------------------------- tests

describe("resolving the virtual modules", () => {
	const plugin = docsPlugin({ repoRoot: repo(TWO_PAGES) });

	test("claims the manifest, loaders, search and every page module", () => {
		expect(plugin.name).toBe("nebulactrl-docs");
		expect(plugin.enforce).toBe("pre");
		expect(resolve(plugin, "virtual:docs-manifest")).toBe(
			"\0virtual:docs-manifest",
		);
		expect(resolve(plugin, "virtual:docs-loaders")).toBe(
			"\0virtual:docs-loaders",
		);
		expect(resolve(plugin, "virtual:docs-search")).toBe(
			"\0virtual:docs-search",
		);
		expect(resolve(plugin, "virtual:docs-page/operations/variables")).toBe(
			"\0virtual:docs-page/operations/variables",
		);
	});

	test("leaves every other module to the bundler", () => {
		expect(resolve(plugin, "react")).toBeNull();
		expect(resolve(plugin, "./virtual:docs-manifest")).toBeNull();
		expect(resolve(plugin, "virtual:docs")).toBeNull();
	});

	test("ignores ids it did not resolve, and unknown virtual names", () => {
		expect(load(plugin, "virtual:docs-manifest")).toBeNull();
		expect(load(plugin, "\0virtual:something-else")).toBeNull();
	});
});

describe("loading the virtual modules", () => {
	test("the manifest holds each page's metadata and the sidebar tree", async () => {
		const plugin = docsPlugin({ repoRoot: repo(TWO_PAGES) });
		const source = load(plugin, "\0virtual:docs-manifest") as string;
		const { metas, nav } = (await evaluate(source)) as {
			metas: Array<{ slug: string; title: string; section: string }>;
			nav: Array<{ title: string; items: unknown[] }>;
		};
		expect(metas.map((m) => [m.slug, m.title, m.section]).sort()).toEqual([
			["alpha", "Alpha", "Applications"],
			["beta", "Beta", "Applications"],
		]);
		expect(nav.map((section) => section.title)).toContain("Applications");
	});

	test("the loaders import one module per page", async () => {
		const plugin = docsPlugin({ repoRoot: repo(TWO_PAGES) });
		const source = load(plugin, "\0virtual:docs-loaders") as string;
		expect(source).toContain(
			'"alpha": () => import("virtual:docs-page/alpha")',
		);
		expect(source).toContain('"beta": () => import("virtual:docs-page/beta")');
		expect(source.startsWith("export const loaders = {")).toBe(true);
	});

	test("a page module exports the compiled page, with its link rewritten to /docs", async () => {
		const plugin = docsPlugin({ repoRoot: repo(TWO_PAGES) });
		const source = load(plugin, "\0virtual:docs-page/alpha") as string;
		const { default: compiled } = (await evaluate(source)) as {
			default: { kind: string; html: string };
		};
		expect(compiled.kind).toBe("md");
		expect(compiled.html).toContain('href="/docs/beta"');
		expect(compiled.html).toContain("First page");
	});

	test("asking for a page the docs do not have is an error naming it", () => {
		const plugin = docsPlugin({ repoRoot: repo(TWO_PAGES) });
		const ctx = context();
		expect(() => load(plugin, "\0virtual:docs-page/nope", ctx)).toThrow(
			'docs: no page "nope"',
		);
	});

	test("the search index covers every page", async () => {
		const plugin = docsPlugin({ repoRoot: repo(TWO_PAGES) });
		const source = load(plugin, "\0virtual:docs-search") as string;
		const { default: data } = (await evaluate(source)) as {
			default: { pages: Array<[string, string, string]>; rows: unknown[][] };
		};
		expect(data.pages.map((p) => p[0]).sort()).toEqual(["alpha", "beta"]);
		expect(data.rows.length).toBeGreaterThanOrEqual(2);
	});

	test("watches the OpenAPI document and every markdown file, so an edit rebuilds", () => {
		const root = repo({
			...TWO_PAGES,
			"docs/deep/gamma.md": page("Gamma", "x", 3),
			"docs/notes.txt": "x",
		});
		const plugin = docsPlugin({ repoRoot: root });
		const ctx = context();
		load(plugin, "\0virtual:docs-manifest", ctx);
		expect(ctx.watched).toContain(path.join(root, "openapi", "openapi.json"));
		expect(ctx.watched).toContain(path.join(root, "docs", "alpha.md"));
		expect(ctx.watched).toContain(path.join(root, "docs", "deep", "gamma.md"));
		expect(ctx.watched.some((file) => file.endsWith("notes.txt"))).toBe(false);
	});

	test("a docs tree with problems fails the build and lists each one", () => {
		// A go.mod marks a full checkout, where links are verified against it.
		const root = repo({
			"go.mod": "module x\n",
			"docs/alpha.md": page(
				"Alpha",
				"A [broken](gone.md) link and [another](also-gone.md).",
			),
		});
		const plugin = docsPlugin({ repoRoot: root });
		expect(() => load(plugin, "\0virtual:docs-manifest")).toThrow(
			/^docs: 2 problem\(s\)\n {2}- docs\/alpha\.md: link "gone\.md"[^\n]*\n {2}- docs\/alpha\.md: link "also-gone\.md"/,
		);
	});

	test("a page with broken frontmatter fails the build", () => {
		const plugin = docsPlugin({
			repoRoot: repo({ "docs/beta.md": "---\ntitle: [x]\n---\n# B" }),
		});
		expect(() => load(plugin, "\0virtual:docs-manifest")).toThrow(
			"not supported",
		);
	});

	test("without a go.mod (the Docker web stage) links that leave docs/ are not verified", () => {
		const plugin = docsPlugin({
			repoRoot: repo({
				"docs/alpha.md": page("Alpha", "See [the source](../internal/x.go)."),
			}),
		});
		expect(load(plugin, "\0virtual:docs-manifest")).toContain('"alpha"');
	});

	test("the corpus is built once and shared by every module", () => {
		const root = repo(TWO_PAGES);
		const plugin = docsPlugin({ repoRoot: root });
		const first = load(plugin, "\0virtual:docs-manifest");
		fs.writeFileSync(path.join(root, "docs", "alpha.md"), page("Renamed", "x"));
		expect(load(plugin, "\0virtual:docs-manifest")).toBe(first);
	});

	test("with an OpenAPI document the API reference pages are part of the docs", async () => {
		const openapi = {
			openapi: "3.1.0",
			tags: [{ name: "services", description: "Services" }],
			paths: {
				"/services": {
					get: {
						operationId: "listServices",
						tags: ["services"],
						summary: "List",
						responses: { "200": { description: "OK" } },
					},
				},
			},
		};
		const plugin = docsPlugin({
			repoRoot: repo({
				...TWO_PAGES,
				"docs/reference/api.md": `---\ntitle: API\ndescription: "The API."\nsection: Reference\norder: 1\napi: true\n---\n\n# API\n\nConventions.\n`,
				"openapi/openapi.json": JSON.stringify(openapi),
			}),
		});
		const { metas } = (await evaluate(
			load(plugin, "\0virtual:docs-manifest") as string,
		)) as { metas: Array<{ slug: string; kind: string }> };
		expect(
			metas.filter((m) => m.kind === "api-tag").map((m) => m.slug),
		).toEqual(["reference/api/services"]);
	});
});

describe("the emitted files", () => {
	test("llms.txt, llms-full.txt and one markdown file per page ship as assets", () => {
		const plugin = docsPlugin({ repoRoot: repo(TWO_PAGES) });
		const ctx = context();
		// The corpus is loaded lazily by the first virtual module.
		load(plugin, "\0virtual:docs-manifest", ctx);
		(hook(plugin, "generateBundle") as unknown as (this: Context) => void).call(
			ctx,
		);
		const names = ctx.emitted.map((file) => file.fileName);
		expect(names).toEqual(
			expect.arrayContaining([
				"llms.txt",
				"llms-full.txt",
				"docs/alpha.md",
				"docs/beta.md",
			]),
		);
		const index =
			ctx.emitted.find((f) => f.fileName === "llms.txt")?.source ?? "";
		expect(index.startsWith("# NebulaCtrl")).toBe(true);
		// The control plane fills the origin in when it serves them.
		expect(index).toContain(ORIGIN_PLACEHOLDER);
		expect(
			ctx.emitted.find((f) => f.fileName === "docs/alpha.md")?.source,
		).toContain("First page");
	});
});

interface Response {
	headers: Record<string, string>;
	body?: string;
	setHeader(name: string, value: string): void;
	end(body: string): void;
}

function devServer(plugin: Plugin) {
	let middleware:
		| ((req: unknown, res: Response, next: (error?: unknown) => void) => void)
		| undefined;
	const watcher = Object.assign(new EventEmitter(), {
		added: [] as string[],
		add(files: string[]) {
			watcher.added.push(...files);
		},
	});
	const modules = new Map<string, { id: string }>();
	const invalidated: string[] = [];
	const sent: unknown[] = [];
	const server = {
		middlewares: {
			use: (fn: typeof middleware) => {
				middleware = fn;
			},
		},
		watcher,
		moduleGraph: {
			idToModuleMap: modules,
			invalidateModule: (mod: { id: string }) => invalidated.push(mod.id),
		},
		ws: { send: (message: unknown) => sent.push(message) },
	};
	(hook(plugin, "configureServer") as unknown as (server: unknown) => void)(
		server,
	);
	return {
		watcher,
		modules,
		invalidated,
		sent,
		request(url: string, host?: string) {
			const res: Response = {
				headers: {},
				setHeader(name, value) {
					res.headers[name] = value;
				},
				end(body) {
					res.body = body;
				},
			};
			let nextCalled: { error?: unknown } | null = null;
			middleware?.({ url, headers: host ? { host } : {} }, res, (error) => {
				nextCalled = { error };
			});
			return { res, next: nextCalled as { error?: unknown } | null };
		},
	};
}

describe("the dev server", () => {
	test("serves llms.txt with the origin of whoever asks", () => {
		const plugin = docsPlugin({ repoRoot: repo(TWO_PAGES) });
		const dev = devServer(plugin);
		const { res, next } = dev.request("/llms.txt?x=1", "docs.local:5173");
		expect(next).toBeNull();
		expect(res.headers["Content-Type"]).toBe("text/plain; charset=utf-8");
		expect(res.body).toContain("http://docs.local:5173/docs/alpha.md");
		expect(res.body).not.toContain(ORIGIN_PLACEHOLDER);
	});

	test("without a Host header the origin is localhost", () => {
		const dev = devServer(docsPlugin({ repoRoot: repo(TWO_PAGES) }));
		expect(dev.request("/llms-full.txt").res.body).toContain(
			"http://localhost/docs/",
		);
	});

	test("serves one page's markdown with its own content type", () => {
		const dev = devServer(docsPlugin({ repoRoot: repo(TWO_PAGES) }));
		const { res } = dev.request("/docs/beta.md", "h");
		expect(res.headers["Content-Type"]).toBe("text/markdown; charset=utf-8");
		expect(res.body).toContain("Second page.");
	});

	test("anything else, including a page that does not exist, is left to the next handler", () => {
		const dev = devServer(docsPlugin({ repoRoot: repo(TWO_PAGES) }));
		expect(dev.request("/").next).not.toBeNull();
		expect(dev.request("/docs/nope.md").next).not.toBeNull();
		expect(dev.request("/docs/beta").next).not.toBeNull();
		expect(dev.request("/docs/nope.md").res.body).toBeUndefined();
	});

	test("a docs tree that does not build is passed to the next handler as an error", () => {
		const dev = devServer(
			docsPlugin({
				repoRoot: repo({
					"go.mod": "module x\n",
					"docs/a.md": page("A", "[x](gone.md)"),
				}),
			}),
		);
		const { next, res } = dev.request("/llms.txt");
		if (!(next?.error instanceof Error)) {
			throw new Error("expected the next handler to be given an error");
		}
		expect(next.error.message).toContain("docs:");
		expect(res.body).toBeUndefined();
	});

	test("watches the docs directory and the OpenAPI document", () => {
		const root = repo(TWO_PAGES);
		const dev = devServer(docsPlugin({ repoRoot: root }));
		expect(dev.watcher.added).toEqual([
			path.join(root, "docs"),
			path.join(root, "openapi", "openapi.json"),
		]);
	});

	test.each(["change", "add", "unlink"])(
		"a docs file that %ss rebuilds the corpus, drops the virtual modules and reloads the page",
		(event) => {
			const root = repo(TWO_PAGES);
			const plugin = docsPlugin({ repoRoot: root });
			const dev = devServer(plugin);
			dev.modules.set("a", { id: "\0virtual:docs-manifest" });
			dev.modules.set("b", { id: "\0virtual:docs-page/alpha" });
			dev.modules.set("c", { id: "/src/main.ts" });
			const before = dev.request("/docs/alpha.md").res.body;

			fs.writeFileSync(
				path.join(root, "docs", "alpha.md"),
				page("Alpha", "Rewritten body."),
			);
			dev.watcher.emit(event, path.join(root, "docs", "alpha.md"));

			expect(dev.invalidated).toEqual([
				"\0virtual:docs-manifest",
				"\0virtual:docs-page/alpha",
			]);
			expect(dev.sent).toEqual([{ type: "full-reload" }]);
			const after = dev.request("/docs/alpha.md").res.body;
			expect(before).toContain("First page");
			expect(after).toContain("Rewritten body.");
		},
	);

	test("a file outside the docs is ignored, but the OpenAPI document is not", () => {
		const root = repo(TWO_PAGES);
		const dev = devServer(docsPlugin({ repoRoot: root }));
		dev.watcher.emit("change", path.join(root, "src", "main.ts"));
		expect(dev.sent).toEqual([]);
		dev.watcher.emit("change", path.join(root, "openapi", "openapi.json"));
		expect(dev.sent).toEqual([{ type: "full-reload" }]);
	});
});
