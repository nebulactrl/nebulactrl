import {
	type ApiOperation,
	type ApiSchema,
	type ApiTagPage,
	type DocMeta,
	docHref,
	type NavItem,
	schemaLabel,
} from "../types.ts";
import type { Corpus, CorpusEntry } from "./corpus.ts";
import { type LinkContext, resolveLink } from "./links.ts";
import { stripHtmlComments } from "./markdown.ts";

// llms.txt (https://llmstxt.org), llms-full.txt and a raw markdown file per
// page, all generated from the same corpus as the docs site. Every absolute URL
// is written against a placeholder origin; the control plane swaps in
// NEBULACTRL_PUBLIC_URL (or the request's host) when it serves them, so one
// build works behind any hostname.

/** Replaced at request time by the control plane (internal/webui). */
export const ORIGIN_PLACEHOLDER = "https://origin.nebulactrl.invalid";

const OPENAPI_PATH = "/api/v1/openapi.json";

/** The nebula.toml JSON Schema: the control plane serves it here, and so does the docs site. */
const SCHEMA_PATH = "/schema/nebula.toml.json";

const SUMMARY =
	"NebulaCtrl is a self-hosted, Docker-native platform-as-a-service. You connect your own Linux servers, deploy from Git or a container image, and manage variables, domains, databases, backups, logs and rollbacks from a canvas, a CLI or a REST API. A Go control plane with a Postgres database talks to a small agent on each Docker host; Traefik on each host routes traffic.";

export interface LlmsOutput {
	/** The llms.txt index. */
	index: string;
	/** Every user-facing page in one file. */
	full: string;
	/** `docs/<slug>.md` → clean markdown, for every page including each API area. */
	pages: Map<string, string>;
}

type Mode = "raw" | "full";

const LINK = /(\]\()([^)\s]+)((?:\s+"[^"]*")?\))/g;

/** What a link to a repository file becomes while there is no public repository: the text alone. */
const UNLINK = "\uE000";
const UNLINKED = /\[([^\]]*)\]\(\uE000(?:\s+"[^"]*")?\)/g;

function rewriteTarget(target: string, ctx: LinkContext, mode: Mode): string {
	const resolved = resolveLink(target, ctx);
	switch (resolved.kind) {
		case "page":
			return mode === "raw"
				? `${ORIGIN_PLACEHOLDER}${resolved.slug ? `/docs/${resolved.slug}.md` : "/docs"}${resolved.anchor ? `#${resolved.anchor}` : ""}`
				: `${ORIGIN_PLACEHOLDER}${docHref(resolved.slug, resolved.anchor)}`;
		case "site":
			return `${ORIGIN_PLACEHOLDER}${resolved.href}`;
		case "repo":
			return resolved.href ?? UNLINK;
		default:
			return target;
	}
}

/**
 * Makes the links of a page absolute: pages become `<origin>/docs/<slug>.md`
 * (raw) or `<origin>/docs/<slug>#anchor` (full), site paths gain the origin,
 * GitHub and external links stay. Code fences and inline code are left alone.
 */
export function rewriteLinks(
	body: string,
	ctx: LinkContext,
	mode: Mode,
): string {
	const out: string[] = [];
	let fence: string | null = null;
	for (const line of body.split("\n")) {
		const marker = /^\s{0,3}(`{3,}|~{3,})/.exec(line);
		if (marker) {
			const m = marker[1] as string;
			if (fence === null) fence = m;
			else if (m[0] === fence[0] && m.length >= fence.length) fence = null;
			out.push(line);
			continue;
		}
		if (fence !== null) {
			out.push(line);
			continue;
		}
		out.push(
			line
				.split(/(`+[^`]*`+)/)
				.map((segment, i) =>
					i % 2 === 1
						? segment
						: segment
								.replace(
									LINK,
									(_m, open: string, target: string, close: string) =>
										`${open}${rewriteTarget(target, ctx, mode)}${close}`,
								)
								.replace(UNLINKED, "$1"),
				)
				.join(""),
		);
	}
	return out.join("\n");
}

function flatten(items: NavItem[]): string[] {
	return items.flatMap((item) => [item.slug, ...flatten(item.children)]);
}

export function buildLlms(corpus: Corpus): LlmsOutput {
	const { entries, nav, sources } = corpus;
	const O = ORIGIN_PLACEHOLDER;

	const apiTags = (indexSlug: string): DocMeta[] =>
		[...entries.values()]
			.map((entry) => entry.meta)
			.filter((meta) => meta.parent === indexSlug)
			.sort((a, b) => a.order - b.order);

	/** The page's markdown with absolute links; for an API index, the areas and the spec pointer appended. */
	const body = (slug: string, mode: Mode): string => {
		const entry = entries.get(slug) as CorpusEntry;
		const source = sources.get(slug);
		if (!source) return "";
		let text = stripHtmlComments(source.body).trim();
		if (!/^#\s+\S/.test(text)) text = `# ${entry.meta.title}\n\n${text}`;
		text = rewriteLinks(text, corpus.linkContext(source.fromFile), mode);
		if (entry.meta.kind === "api") {
			const areas = apiTags(slug)
				.map(
					(tag) =>
						`- [${tag.title}](${O}/docs/${tag.slug}.md): ${tag.description}`,
				)
				.join("\n");
			text += `\n\n${areas}\n\nThe complete machine-readable API is the OpenAPI 3.1 document at ${O}${OPENAPI_PATH}.`;
		}
		return `${text}\n`;
	};

	const pages = new Map<string, string>();
	for (const [slug, entry] of entries) {
		if (entry.page.kind === "api-tag")
			pages.set(slug, tagMarkdown(entry.page, entry.meta));
		else pages.set(slug, body(slug, "raw"));
	}

	// ---- llms-full.txt: every user-facing page, in sidebar order, API areas left out.
	const order = nav.flatMap((section) => flatten(section.items));
	const guides = order.filter(
		(slug) => entries.get(slug)?.meta.kind !== "api-tag",
	);
	const fullParts = guides.map((slug) => {
		const meta = (entries.get(slug) as CorpusEntry).meta;
		const text = body(slug, "full").replace(/^#\s+.*\n+/, "");
		return `# ${meta.title}\nSource: ${O}/docs/${slug}\n\n${text.trim()}\n`;
	});
	const full = `# NebulaCtrl documentation\n\n> ${SUMMARY}\n\nEvery page of the NebulaCtrl docs in one file, in reading order. Each page starts with its title and its source URL. The API endpoint reference is not repeated here: it is generated from ${O}${OPENAPI_PATH}.\n\n---\n\n${fullParts.join("\n---\n\n")}`;

	// ---- llms.txt: the index.
	const link = (meta: DocMeta, label = meta.title) =>
		`- [${label}](${O}/docs/${meta.slug}.md): ${meta.description}`;
	const sections: string[] = [];
	const section = (title: string, lines: string[]) => {
		if (lines.length > 0) sections.push(`## ${title}\n\n${lines.join("\n")}`);
	};
	const metaOf = (slug: string) => (entries.get(slug) as CorpusEntry).meta;
	const slugsOf = (title: string) => {
		const found = nav.find((s) => s.title === title);
		return found
			? flatten(found.items).filter((s) => metaOf(s).kind !== "api-tag")
			: [];
	};
	const optionalNav = ["Internals"];
	const first = ["Getting started", "Reference"];

	section(
		"Getting started",
		slugsOf("Getting started").map((s) => link(metaOf(s))),
	);

	const reference = slugsOf("Reference").map(metaOf);
	const referenceFirst = reference.filter((m) => m.slug !== "operations");
	section("CLI, API and MCP", [
		...referenceFirst.map((m) => link(m)),
		`- [OpenAPI document](${O}${OPENAPI_PATH}): The complete REST API as OpenAPI 3.1 JSON, served by the control plane without authentication.`,
		`- [nebula.toml JSON Schema](${O}${SCHEMA_PATH}): Machine-readable schema with descriptions and examples for every key of nebula.toml, served by the control plane without authentication.`,
	]);

	for (const s of nav) {
		if (first.includes(s.title) || optionalNav.includes(s.title)) continue;
		section(
			s.title,
			slugsOf(s.title).map((slug) => link(metaOf(slug))),
		);
	}

	const optional: string[] = [
		`- [Everything in one file](${O}/llms-full.txt): Every page above concatenated, for loading the whole documentation at once.`,
	];
	for (const meta of reference.filter((m) => m.slug === "operations"))
		optional.push(link(meta));
	for (const slug of slugsOf("Internals")) optional.push(link(metaOf(slug)));
	const apiIndex = [...entries.values()].find((e) => e.meta.kind === "api");
	if (apiIndex) {
		for (const tag of apiTags(apiIndex.meta.slug)) {
			optional.push(
				`- [${tag.title} API](${O}/docs/${tag.slug}.md): ${tag.description}`,
			);
		}
	}
	section("Optional", optional);

	const index = `# NebulaCtrl\n\n> ${SUMMARY}\n\nThe documentation is markdown: append .md to any /docs/<page> URL to read the page as plain markdown. Pages are written for people running their own infrastructure; the CLI and API references are generated from the code.\n\n${sections.join("\n\n")}\n`;

	return { index, full, pages };
}

// ---------------------------------------------------------------- API areas as markdown

function fieldLines(
	schema: ApiSchema,
	defs: Record<string, ApiSchema>,
): string[] {
	let target: ApiSchema | undefined = schema;
	while (target?.type === "array" && target.items) target = target.items;
	const resolved = target?.ref ? defs[target.ref] : target;
	return (resolved?.props ?? []).map(
		(p) =>
			`  - \`${p.name}\` (${schemaLabel(p.schema)}${p.required ? ", required" : ""})${p.schema.description ? `: ${p.schema.description}` : ""}`,
	);
}

function operationMarkdown(
	op: ApiOperation,
	defs: Record<string, ApiSchema>,
): string {
	const lines = [`## ${op.method} ${op.path}`, ""];
	if (op.summary) lines.push(op.summary, "");
	if (op.description) lines.push(op.description, "");
	lines.push(
		op.public
			? "Authentication: none."
			: "Authentication: session cookie or `Authorization: Bearer nct_…` token.",
		"",
	);
	if (op.params.length > 0) {
		lines.push("Parameters:");
		for (const p of op.params) {
			lines.push(
				`- \`${p.name}\` (${p.in}, ${p.type}${p.required ? ", required" : ""})${p.description ? `: ${p.description}` : ""}`,
			);
		}
		lines.push("");
	}
	if (op.body) {
		lines.push(
			`Request body (${op.body.contentType}${op.body.required ? ", required" : ""}): ${schemaLabel(op.body.schema)}`,
		);
		lines.push(...fieldLines(op.body.schema, defs), "");
	}
	const ok = op.responses.filter((r) => /^[23]/.test(r.status));
	for (const r of ok) {
		lines.push(
			`Response ${r.status}: ${r.description}${r.schema ? ` (${schemaLabel(r.schema)})` : ""}`,
		);
		if (r.schema) lines.push(...fieldLines(r.schema, defs));
	}
	const errors = op.responses.filter((r) => /^[45]/.test(r.status));
	if (errors.length > 0) {
		lines.push(
			`Errors: ${errors
				.map((r) => {
					const codes = op.errorCodes
						.filter((c) => c.status === r.status)
						.map((c) => `\`${c.code}\``);
					return `${r.status} ${r.description}${codes.length > 0 ? ` (${codes.join(", ")})` : ""}`;
				})
				.join("; ")}`,
		);
	}
	return lines.join("\n").trimEnd();
}

/** A compact markdown rendering of one API area: every operation with its parameters, body fields and errors. */
function tagMarkdown(page: ApiTagPage, meta: DocMeta): string {
	const head = `# ${meta.title} API\n\n${page.description ? `${page.description}. ` : ""}${page.operations.length} ${page.operations.length === 1 ? "operation" : "operations"} under /api/v1. Errors are RFC 9457 problem+json with a stable \`code\` field. The complete schemas are in ${ORIGIN_PLACEHOLDER}${OPENAPI_PATH}.`;
	return `${head}\n\n${page.operations.map((op) => operationMarkdown(op, page.defs)).join("\n\n")}\n`;
}

/** Fills the placeholder origin in a generated file: `https://example.test`, no trailing slash. */
export function withOrigin(text: string, origin: string): string {
	// Trimmed by index: the origin comes from a request header in dev, and a
	// `/\/+$/` regex backtracks quadratically on a long run of slashes.
	let end = origin.length;
	while (end > 0 && origin.charCodeAt(end - 1) === 47) end--;
	return text.replaceAll(ORIGIN_PLACEHOLDER, origin.slice(0, end));
}
