import fs from "node:fs";
import path from "node:path/posix";
import {
	type ApiTagPage,
	type DocMeta,
	type DocPage,
	type NavItem,
	type NavSection,
	operationAnchor,
	SECTIONS,
	type SearchRecord,
} from "../types.ts";
import {
	type DocFrontmatter,
	parseFrontmatter,
	readDocFrontmatter,
} from "./frontmatter.ts";
import type { LinkContext } from "./links.ts";
import { type CompiledPage, compileMarkdown } from "./markdown.ts";
import { type ApiTagData, groupByTag } from "./openapi.ts";
import { fileSlug } from "./slug.ts";

/** Docs files that are not user-facing: the repo index and research notes. */
const EXCLUDED: RegExp[] = [/^README\.md$/i, /-research\.md$/];

export function isExcluded(rel: string): boolean {
	return EXCLUDED.some((pattern) => pattern.test(rel));
}

export interface CorpusOptions {
	/** Absolute path of the repository's `docs/` folder. */
	docsDir: string;
	/** Absolute path of the repository root. */
	repoRoot: string;
	/** The parsed `openapi/openapi.json`, or null to build without an API reference. */
	openapi: unknown | null;
	/**
	 * Check that links leaving `docs/` (to `SECURITY.md`, `deploy/…`) exist. Off in the
	 * Docker web stage, which only has `docs/` and the spec; the repo test checks them.
	 */
	verifyRepoLinks?: boolean;
	/**
	 * Where links that leave `docs/` point. Omitted: `REPO_URL`. `null`: the
	 * repository is not public, so those links render as plain text.
	 */
	repoUrl?: string | null;
}

export interface CorpusEntry {
	meta: DocMeta;
	page: DocPage;
}

export interface Corpus {
	entries: Map<string, CorpusEntry>;
	nav: NavSection[];
	search: SearchRecord[];
	/** Every heading id of each markdown page: what an anchor may point at. */
	headingIds: Map<string, Set<string>>;
	/** Broken links, bad frontmatter, unknown sections: the build fails on any. */
	errors: string[];
	/** Pages without frontmatter (compiled with fallbacks; the docs test fails on them). */
	missingFrontmatter: string[];
	/** The markdown of each page (frontmatter stripped), for the raw and llms.txt outputs. */
	sources: Map<string, PageSource>;
	/** Resolves links as a page in `fromFile` would, for rewriting them in the raw outputs. */
	linkContext(fromFile: string): LinkContext;
}

interface PageSource {
	/** Relative to `docs/`, posix. */
	fromFile: string;
	body: string;
}

const SEARCH_LEAD_CAP = 450;

function listMarkdown(dir: string, base = dir): string[] {
	const out: string[] = [];
	for (const name of fs.readdirSync(dir, { withFileTypes: true })) {
		if (name.name.startsWith(".")) continue;
		const full = path.join(dir, name.name);
		if (name.isDirectory()) out.push(...listMarkdown(full, base));
		else if (name.name.endsWith(".md")) out.push(path.relative(base, full));
	}
	return out.sort();
}

function firstHeading(body: string): string | null {
	const match = /^#\s+(.+?)\s*#*\s*$/m.exec(body);
	return match ? (match[1] as string).replace(/[`*_]/g, "") : null;
}

export function buildCorpus(options: CorpusOptions): Corpus {
	const docsDir = options.docsDir.split("\\").join("/");
	const repoRoot = options.repoRoot.split("\\").join("/");
	const errors: string[] = [];
	const missingFrontmatter: string[] = [];

	const files = listMarkdown(docsDir).filter((rel) => !isExcluded(rel));
	const slugOf = new Map<string, string>();
	const claimed = new Map<string, string>();
	for (const rel of files) {
		const slug = fileSlug(rel);
		const other = claimed.get(slug);
		if (other)
			errors.push(`docs/${rel} and docs/${other} both map to /docs/${slug}`);
		claimed.set(slug, rel);
		slugOf.set(rel, slug);
	}

	const verifyRepo = options.verifyRepoLinks ?? true;
	const repoEntry = (repoPath: string): "file" | "dir" | null => {
		if (!verifyRepo) return "file";
		try {
			const stat = fs.statSync(path.join(repoRoot, repoPath));
			return stat.isDirectory() ? "dir" : "file";
		} catch {
			return null;
		}
	};

	const entries = new Map<string, CorpusEntry>();
	const compiled = new Map<string, CompiledPage>();
	const sources = new Map<string, PageSource>();
	let apiIndex: string | null = null;

	for (const rel of files) {
		const slug = slugOf.get(rel) as string;
		const where = `docs/${rel}`;
		let raw: string;
		try {
			raw = fs.readFileSync(path.join(docsDir, rel), "utf8");
		} catch (error) {
			errors.push(`${where}: ${(error as Error).message}`);
			continue;
		}
		let fm: Partial<DocFrontmatter>;
		let body: string;
		let hasFrontmatter = false;
		try {
			const parsed = parseFrontmatter(raw);
			hasFrontmatter = parsed.lines > 0;
			body = parsed.body;
			fm = readDocFrontmatter(parsed.data);
		} catch (error) {
			errors.push(`${where}: ${(error as Error).message}`);
			continue;
		}
		if (!(hasFrontmatter && fm.title && fm.description && fm.section)) {
			missingFrontmatter.push(where);
		}
		const title = fm.title ?? firstHeading(body) ?? slug;
		const section = fm.section ?? "Troubleshooting";
		if (!(SECTIONS as readonly string[]).includes(section)) {
			errors.push(
				`${where}: unknown section "${section}" (use one of: ${SECTIONS.join(", ")})`,
			);
		}
		const result = compileMarkdown(body, {
			title,
			ctx: {
				fromFile: rel,
				pages: slugOf,
				repoEntry,
				...(options.repoUrl !== undefined ? { repoUrl: options.repoUrl } : {}),
			},
		});
		for (const message of result.errors) errors.push(`${where}: ${message}`);
		compiled.set(slug, result);
		sources.set(slug, { fromFile: rel, body });
		if (fm.api) apiIndex = slug;
		const meta: DocMeta = {
			slug,
			title,
			description: fm.description ?? "",
			section,
			order: fm.order ?? 1000,
			kind: fm.api ? "api" : "md",
			source: where,
			...(fm.generated ? { generated: true } : {}),
		};
		entries.set(slug, {
			meta,
			page: {
				kind: meta.kind === "api" ? "api" : "md",
				slug,
				html: result.html,
				toc: result.toc,
			},
		});
	}

	// API tag pages, one per OpenAPI tag, nested under the index page.
	const apiTags: ApiTagData[] =
		options.openapi && apiIndex ? groupByTag(options.openapi) : [];
	const apiSlugs = new Set<string>();
	if (apiIndex) {
		const index = entries.get(apiIndex) as CorpusEntry;
		apiTags.forEach((tag, i) => {
			const slug = `${apiIndex}/${tag.name}`;
			apiSlugs.add(slug);
			const page: ApiTagPage = {
				kind: "api-tag",
				slug,
				tag: tag.name,
				description: tag.description,
				operations: tag.operations,
				defs: tag.defs,
			};
			entries.set(slug, {
				meta: {
					slug,
					title: tagTitle(tag.name),
					description:
						`${tag.operations.length} ${tag.operations.length === 1 ? "operation" : "operations"}. ${tag.description}`.trim(),
					section: index.meta.section,
					order: i,
					kind: "api-tag",
					parent: apiIndex as string,
					generated: true,
				},
				page,
			});
		});
	}

	// Link check: every internal link must reach a page and, when it names one, a heading.
	for (const [slug, result] of compiled) {
		const where = entries.get(slug)?.meta.source ?? slug;
		for (const anchor of result.anchorLinks) {
			if (!result.headingIds.has(anchor)) {
				errors.push(
					`${where}: link "#${anchor}" matches no heading on this page`,
				);
			}
		}
		for (const link of result.pageLinks) {
			if (link.slug === "") continue; // /docs, the landing page
			const target = entries.get(link.slug);
			if (!target) {
				errors.push(`${where}: link "${link.href}" points at no docs page`);
				continue;
			}
			if (link.anchor === "") continue;
			const targetCompiled = compiled.get(link.slug);
			if (targetCompiled && !targetCompiled.headingIds.has(link.anchor)) {
				errors.push(
					`${where}: link "${link.href}": /docs/${link.slug} has no heading "#${link.anchor}"`,
				);
			}
		}
	}

	return {
		entries,
		nav: buildNav(entries),
		search: buildSearch(entries, compiled),
		headingIds: new Map(
			[...compiled].map(([slug, result]) => [slug, result.headingIds]),
		),
		errors,
		missingFrontmatter,
		sources,
		linkContext: (fromFile) => ({
			fromFile,
			pages: slugOf,
			repoEntry,
			...(options.repoUrl !== undefined ? { repoUrl: options.repoUrl } : {}),
		}),
	};
}

/** "shared-variables" → "Shared variables". */
function tagTitle(name: string): string {
	const words = name.replace(/[-_]/g, " ");
	return `${words.charAt(0).toUpperCase()}${words.slice(1)}`;
}

function buildNav(entries: Map<string, CorpusEntry>): NavSection[] {
	const sections: NavSection[] = SECTIONS.map((title) => ({
		id: title.toLowerCase().replace(/[^a-z0-9]+/g, "-"),
		title,
		items: [],
	}));
	const byParent = new Map<string, DocMeta[]>();
	const top: DocMeta[] = [];
	for (const { meta } of entries.values()) {
		if (meta.parent) {
			const list = byParent.get(meta.parent) ?? [];
			list.push(meta);
			byParent.set(meta.parent, list);
		} else top.push(meta);
	}
	const compare = (a: DocMeta, b: DocMeta) =>
		a.order - b.order || a.title.localeCompare(b.title);
	const toItem = (meta: DocMeta): NavItem => ({
		slug: meta.slug,
		title: meta.title,
		children: (byParent.get(meta.slug) ?? []).sort(compare).map(toItem),
	});
	for (const meta of top.sort(compare)) {
		const section = sections.find((s) => s.title === meta.section);
		section?.items.push(toItem(meta));
	}
	return sections.filter((s) => s.items.length > 0);
}

function buildSearch(
	entries: Map<string, CorpusEntry>,
	compiled: Map<string, CompiledPage>,
): SearchRecord[] {
	const records: SearchRecord[] = [];
	for (const { meta, page } of entries.values()) {
		const result = compiled.get(meta.slug);
		if (result) {
			for (const section of result.sections) {
				const lead = section.anchor === "";
				const text = lead
					? `${meta.description} ${section.text}`.trim()
					: section.text;
				records.push([
					meta.slug,
					section.anchor,
					meta.title,
					section.heading,
					meta.section,
					text.slice(0, SEARCH_LEAD_CAP),
				]);
			}
			if (result.sections.length === 0) {
				records.push([
					meta.slug,
					"",
					meta.title,
					"",
					meta.section,
					meta.description,
				]);
			}
		}
		if (page.kind === "api-tag") {
			records.push([
				meta.slug,
				"",
				`${meta.title} API`,
				"",
				meta.section,
				`${page.description} ${page.operations.map((op) => `${op.method} ${op.path}`).join(" ")}`.slice(
					0,
					SEARCH_LEAD_CAP,
				),
			]);
			for (const op of page.operations) {
				records.push([
					meta.slug,
					operationAnchor(op),
					`${meta.title} API`,
					`${op.method} ${op.path}`,
					meta.section,
					`${op.summary} ${op.description}`.slice(0, 180),
				]);
			}
		}
	}
	return records;
}
