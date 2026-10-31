// Shapes shared by the build-time docs compiler (node) and the docs UI
// (browser). Plain data only: this file is imported on both sides.

/** The kinds of page the docs site has. */
export type DocKind = "md" | "api" | "api-tag";

export interface TocEntry {
	id: string;
	text: string;
	/** 2 or 3: the page title is the h1 and is not listed. */
	level: 2 | 3;
}

/** What the sidebar, the landing page and the search need to know about a page. */
export interface DocMeta {
	/** URL path below `/docs`, no leading slash: "operations/variables". */
	slug: string;
	title: string;
	description: string;
	/** The sidebar group, one of `SECTIONS`. */
	section: string;
	/** Position inside the section; ties break by title. */
	order: number;
	kind: DocKind;
	/** For API tag pages: the slug of the API reference index. Sidebar nests under it. */
	parent?: string;
	/** Path of the source below the repo root ("docs/operations/variables.md"), absent for virtual pages. */
	source?: string;
	/** Written by a generator: the page shows "Generated" instead of "Edit this page". */
	generated?: boolean;
}

export interface NavItem {
	slug: string;
	title: string;
	children: NavItem[];
}

export interface NavSection {
	id: string;
	title: string;
	items: NavItem[];
}

/** The compiled body of a markdown page. */
export interface MarkdownPage {
	kind: "md" | "api";
	slug: string;
	html: string;
	toc: TocEntry[];
}

// ---------------------------------------------------------------- API reference

export interface ApiSchema {
	type?: string;
	format?: string;
	description?: string;
	enum?: string[];
	nullable?: boolean;
	/** A named component schema, resolved through `ApiTagPage.defs`. */
	ref?: string;
	props?: ApiProperty[];
	items?: ApiSchema;
	/** oneOf / anyOf members. */
	variants?: ApiSchema[];
	/** The value schema of a free-form map. */
	additional?: ApiSchema;
	default?: string;
}

export interface ApiProperty {
	name: string;
	required: boolean;
	schema: ApiSchema;
}

export interface ApiParam {
	name: string;
	in: "path" | "query" | "header" | "cookie";
	required: boolean;
	type: string;
	description: string;
}

export interface ApiResponse {
	status: string;
	description: string;
	contentType?: string;
	schema?: ApiSchema;
}

export interface ApiOperation {
	id: string;
	method: "GET" | "POST" | "PUT" | "PATCH" | "DELETE";
	path: string;
	summary: string;
	description: string;
	/** True when the operation takes no credentials (`security: [{}]`). */
	public: boolean;
	params: ApiParam[];
	body?: { contentType: string; required: boolean; schema: ApiSchema };
	responses: ApiResponse[];
	/** `409 deployment_in_progress` pairs found in the description. */
	errorCodes: { status: string; code: string }[];
}

export interface ApiTagPage {
	kind: "api-tag";
	slug: string;
	tag: string;
	description: string;
	operations: ApiOperation[];
	/** Every named schema the operations reach, converted. */
	defs: Record<string, ApiSchema>;
}

export type DocPage = MarkdownPage | ApiTagPage;

// ---------------------------------------------------------------- search

/**
 * One searchable chunk: a page's lead, or one heading's section.
 * Tuple order is fixed so the index stays compact on the wire.
 */
export type SearchRecord = [
	slug: string,
	anchor: string,
	pageTitle: string,
	heading: string,
	section: string,
	text: string,
];

/**
 * The search index as shipped: pages once, rows pointing at them, so a page's
 * title and section are not repeated for every heading.
 */
export interface SearchData {
	/** [slug, title, section] */
	pages: [string, string, string][];
	/** [page index, anchor, heading, text] */
	rows: [number, string, string, string][];
}

export interface SearchHit {
	slug: string;
	anchor: string;
	pageTitle: string;
	heading: string;
	section: string;
	/** A short text excerpt around the first match. */
	snippet: string;
	score: number;
}

/** The sidebar groups in reading order. A page's `section` must be one of these. */
export const SECTIONS = [
	"Getting started",
	"Installing",
	"Applications",
	"Databases and backups",
	"Templates",
	"Servers",
	"Observability",
	"Teams and security",
	"Platform services",
	"Reference",
	"Troubleshooting",
	"Internals",
] as const;

/** `/docs/<slug>` for a slug, `/docs` for the landing page. */
export function docHref(slug: string, anchor?: string): string {
	const base = slug ? `/docs/${slug}` : "/docs";
	return anchor ? `${base}#${anchor}` : base;
}

/** Where the docs live on GitHub, for "Edit this page" and links out of the docs tree. */
export const REPO_URL = "https://github.com/nebulactrl/nebulactrl";
export const REPO_BRANCH = "main";

/** The anchor an operation carries on its tag page (also the search index's anchor for it). */
export function operationAnchor(op: ApiOperation): string {
	return `op-${op.id}`;
}

/** A one-line type for a schema: `string (date-time) | null`, `Service[]`, `enum: a | b`. */
export function schemaLabel(schema: ApiSchema): string {
	if (schema.ref) return schema.ref;
	if (schema.enum) return `enum: ${schema.enum.join(" | ")}`;
	if (schema.variants) return "one of";
	let label = schema.type ?? "any";
	if (schema.type === "array" && schema.items)
		label = `${schemaLabel(schema.items)}[]`;
	else if (schema.format) label += ` (${schema.format})`;
	if (schema.type === "object" && schema.additional)
		label = `map of ${schemaLabel(schema.additional)}`;
	if (schema.nullable) label += " | null";
	return label;
}
