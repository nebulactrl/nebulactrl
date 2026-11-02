import { mock } from "bun:test";
import type {
	ApiOperation,
	DocMeta,
	DocPage,
	NavSection,
} from "@nebulactrl/docs/types";
import type * as DocsDataModule from "./docs-data";

// Test-only. The docs pages read their content from Vite virtual modules
// (`virtual:docs-manifest`, `virtual:docs-loaders`) that exist only in a Vite
// build. `installDocsFixtures()` stands in a small site for them.
//
// Bun's `mock.module` and module cache are shared by every test file in the
// run, so nothing here runs on import and every test file that installs the
// fixtures restores the empty site (what src/modules.test.ts uses) afterwards.

const DOCS_DATA = `${import.meta.dir}/docs-data.ts`;

function meta(
	slug: string,
	title: string,
	section: string,
	order: number,
	extra: Partial<DocMeta> = {},
): DocMeta {
	return {
		slug,
		title,
		description: `About ${title}.`,
		section,
		order,
		kind: "md",
		source: `docs/${slug}.md`,
		...extra,
	};
}

const manyApps = Array.from({ length: 8 }, (_, index) =>
	meta(
		`applications/page-${index + 1}`,
		`App page ${index + 1}`,
		"Applications",
		index,
	),
);

export const metas: DocMeta[] = [
	meta("getting-started/overview", "Overview", "Getting started", 0),
	meta("getting-started/concepts", "Concepts", "Getting started", 1),
	meta("getting-started/first-deploy", "First deploy", "Getting started", 2),
	meta("getting-started/next-steps", "Next steps", "Getting started", 3),
	...manyApps,
	meta("reference/api", "API reference", "Reference", 0, {
		kind: "api",
		source: "docs/reference/api.md",
	}),
	meta("reference/api/servers", "Servers", "Reference", 1, {
		kind: "api-tag",
		parent: "reference/api",
		generated: true,
	}),
	meta("reference/api/projects", "Projects", "Reference", 0, {
		kind: "api-tag",
		parent: "reference/api",
		generated: true,
		description: "Create and list projects",
	}),
	meta("reference/cli", "CLI", "Reference", 1, {
		generated: true,
		source: "cli/cmd/root.go",
	}),
	meta("internals/virtual", "Virtual page", "Internals", 0),
];
// A page without a source (virtual page): no edit or generated line.
const virtualPage = metas[metas.length - 1];
if (virtualPage) delete virtualPage.source;

const nav: NavSection[] = [
	{
		id: "getting-started",
		title: "Getting started",
		items: [
			"getting-started/overview",
			"getting-started/concepts",
			"getting-started/first-deploy",
			"getting-started/next-steps",
		].map((slug) => ({
			slug,
			title: metas.find((m) => m.slug === slug)?.title ?? slug,
			children: [],
		})),
	},
	{
		id: "applications",
		title: "Applications",
		items: manyApps.map((app) => ({
			slug: app.slug,
			title: app.title,
			children: [],
		})),
	},
	{
		id: "reference",
		title: "Reference",
		items: [
			{
				slug: "reference/api",
				title: "API reference",
				children: [
					{ slug: "reference/api/projects", title: "Projects", children: [] },
					{ slug: "reference/api/servers", title: "Servers", children: [] },
				],
			},
			{ slug: "reference/cli", title: "CLI", children: [] },
		],
	},
	{
		id: "internals",
		title: "Internals",
		items: [{ slug: "internals/virtual", title: "Virtual page", children: [] }],
	},
];

const operation: ApiOperation = {
	id: "createProject",
	method: "POST",
	path: "/projects",
	summary: "Create a project",
	description: "Creates a project.",
	public: false,
	params: [],
	responses: [{ status: "201", description: "Created" }],
	errorCodes: [],
};

const listOperation: ApiOperation = {
	...operation,
	id: "listProjects",
	method: "GET",
	summary: "List projects",
	description: "",
	public: true,
};

export const CODE_HTML = `<div class="docs-code"><button type="button" data-docs-copy><span class="docs-code-copy-text">Copy</span></button><pre><code>bun install
</code></pre></div>`;

export const pages: Record<string, DocPage> = {
	"getting-started/overview": {
		kind: "md",
		slug: "getting-started/overview",
		html: `<h1>Overview</h1><p>Intro text. <a href="/docs/getting-started/concepts">Concepts</a> and <a href="#setup">jump to setup</a> and <a href="/docs/nowhere">a gone page</a> and <a href="https://example.com/out">elsewhere</a>.</p><h2 id="setup">Setup</h2><p>Setup body.</p>${CODE_HTML}<h3 id="deep">Deep</h3><p>Deep body.</p>`,
		toc: [
			{ id: "setup", text: "Setup", level: 2 },
			{ id: "deep", text: "Deep", level: 3 },
		],
	},
	"getting-started/concepts": {
		kind: "md",
		slug: "getting-started/concepts",
		html: "<h1>Concepts</h1><p>Concepts body.</p>",
		toc: [],
	},
	"getting-started/first-deploy": {
		kind: "md",
		slug: "getting-started/first-deploy",
		html: "<h1>First deploy</h1><p>Deploy body.</p>",
		toc: [],
	},
	"reference/api": {
		kind: "api",
		slug: "reference/api",
		html: "<h1>API reference</h1><p>Conventions body.</p>",
		toc: [],
	},
	"reference/api/projects": {
		kind: "api-tag",
		slug: "reference/api/projects",
		tag: "Projects",
		description: "Create and list projects",
		operations: [operation, listOperation],
		defs: {},
	},
	"reference/cli": {
		kind: "md",
		slug: "reference/cli",
		html: "<h1>CLI</h1><p>CLI body.</p>",
		toc: [],
	},
	"internals/virtual": {
		kind: "md",
		slug: "internals/virtual",
		html: "<h1>Virtual page</h1><p>Virtual body.</p>",
		toc: [],
	},
};

/** The loaders the fixture site serves; a test may replace entries (the object is read on every call). */
export const loaders: Record<string, () => Promise<{ default: DocPage }>> = {};

export function resetLoaders() {
	for (const key of Object.keys(loaders)) delete loaders[key];
	for (const [slug, page] of Object.entries(pages)) {
		loaders[slug] = () => Promise.resolve({ default: page });
	}
}

type DocsData = typeof DocsDataModule;

// The `?query` import is a dynamic import on purpose: it makes Bun build a new
// instance of docs-data.ts, which a static import cannot do.

/**
 * Serves the fixture site: a fresh copy of docs-data.ts built on the fixture
 * manifest, swapped in for the shared module so components that import
 * `./docs-data` read it whatever any other test file loaded first.
 */
export async function installDocsFixtures(): Promise<DocsData> {
	resetLoaders();
	mock.module("virtual:docs-manifest", () => ({ metas, nav }));
	mock.module("virtual:docs-loaders", () => ({ loaders }));
	const fresh = (await import(
		`./docs-data.ts?fixture=${Date.now()}`
	)) as DocsData;
	mock.module(DOCS_DATA, () => fresh);
	return fresh;
}

/** Puts the empty site back (the one src/modules.test.ts installs). */
export async function restoreEmptyDocs(): Promise<void> {
	mock.module("virtual:docs-manifest", () => ({ metas: [], nav: [] }));
	mock.module("virtual:docs-loaders", () => ({ loaders: {} }));
	const empty = (await import(
		`./docs-data.ts?empty=${Date.now()}`
	)) as DocsData;
	mock.module(DOCS_DATA, () => empty);
}
