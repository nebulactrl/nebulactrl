import hljs from "highlight.js/lib/core";
import bash from "highlight.js/lib/languages/bash";
import diff from "highlight.js/lib/languages/diff";
import dockerfile from "highlight.js/lib/languages/dockerfile";
import go from "highlight.js/lib/languages/go";
import http from "highlight.js/lib/languages/http";
import ini from "highlight.js/lib/languages/ini";
import javascript from "highlight.js/lib/languages/javascript";
import json from "highlight.js/lib/languages/json";
import nginx from "highlight.js/lib/languages/nginx";
import python from "highlight.js/lib/languages/python";
import sql from "highlight.js/lib/languages/sql";
import typescript from "highlight.js/lib/languages/typescript";
import xml from "highlight.js/lib/languages/xml";
import yaml from "highlight.js/lib/languages/yaml";
import MarkdownIt, { type Env as MarkdownEnv, type Token } from "markdown-it";
import type { TocEntry } from "../types.ts";
import { type LinkContext, resolveLink } from "./links.ts";
import { Slugger } from "./slug.ts";

// Highlighting happens here, at build time: the browser receives spans with
// classes and styles them from the design tokens. No highlighter ships.
const LANGUAGES: Record<string, Parameters<typeof hljs.registerLanguage>[1]> = {
	bash,
	diff,
	dockerfile,
	go,
	http,
	ini,
	javascript,
	json,
	nginx,
	python,
	sql,
	typescript,
	xml,
	yaml,
};
for (const [name, definition] of Object.entries(LANGUAGES)) {
	hljs.registerLanguage(name, definition);
}

const LANGUAGE_ALIASES: Record<string, string> = {
	sh: "bash",
	shell: "bash",
	zsh: "bash",
	console: "bash",
	yml: "yaml",
	js: "javascript",
	ts: "typescript",
	jsonc: "json",
	toml: "ini",
	html: "xml",
	caddyfile: "nginx",
	py: "python",
};

/** What the language chip shows for a fence: the label the author wrote. */
const LANGUAGE_LABELS: Record<string, string> = {
	bash: "sh",
	yaml: "yaml",
	json: "json",
};

export interface CalloutKind {
	tone: "info" | "success" | "warning" | "danger";
	label: string;
}

const CALLOUTS: Record<string, CalloutKind> = {
	NOTE: { tone: "info", label: "Note" },
	TIP: { tone: "success", label: "Tip" },
	IMPORTANT: { tone: "warning", label: "Important" },
	WARNING: { tone: "warning", label: "Warning" },
	CAUTION: { tone: "danger", label: "Danger" },
	DANGER: { tone: "danger", label: "Danger" },
};

// 16px Lucide icon bodies (stroke, currentColor): info, triangle-alert,
// circle-alert, circle-check, copy, check. Inlined so the page markup is
// self-contained HTML.
const ICON_BODIES = {
	info: '<circle cx="12" cy="12" r="10"/><path d="M12 16v-4"/><path d="M12 8h.01"/>',
	warning:
		'<path d="m21.73 18-8-14a2 2 0 0 0-3.48 0l-8 14A2 2 0 0 0 4 21h16a2 2 0 0 0 1.73-3"/><path d="M12 9v4"/><path d="M12 17h.01"/>',
	danger:
		'<circle cx="12" cy="12" r="10"/><line x1="12" x2="12" y1="8" y2="12"/><line x1="12" x2="12.01" y1="16" y2="16"/>',
	success: '<circle cx="12" cy="12" r="10"/><path d="m9 12 2 2 4-4"/>',
	copy: '<rect width="14" height="14" x="8" y="8" rx="2" ry="2"/><path d="M4 16c-1.1 0-2-.9-2-2V4c0-1.1.9-2 2-2h10c1.1 0 2 .9 2 2"/>',
	check: '<path d="M20 6 9 17l-5-5"/>',
} as const;

function icon(name: keyof typeof ICON_BODIES, cls: string): string {
	return `<svg class="${cls}" xmlns="http://www.w3.org/2000/svg" width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">${ICON_BODIES[name]}</svg>`;
}

/** One heading's worth of plain text, for the search index. */
export interface TextSection {
	/** "" for the lead before the first h2. */
	anchor: string;
	heading: string;
	text: string;
}

export interface PageLink {
	href: string;
	slug: string;
	anchor: string;
}

export interface CompiledPage {
	html: string;
	toc: TocEntry[];
	/** Every heading id on the page (h1 included): what an anchor may point at. */
	headingIds: Set<string>;
	sections: TextSection[];
	/** Links to other docs pages, verified later against the corpus. */
	pageLinks: PageLink[];
	/** In-page `#anchor` links, verified against `headingIds`. */
	anchorLinks: string[];
	errors: string[];
}

interface Env extends MarkdownEnv {
	ctx: LinkContext;
	slugger: Slugger;
	toc: TocEntry[];
	headingIds: Set<string>;
	sections: TextSection[];
	pageLinks: PageLink[];
	anchorLinks: string[];
	errors: string[];
}

// `html: false`: raw HTML in a docs page is escaped, never emitted. The docs
// are trusted repo content, but nothing in the pipeline depends on that.
const md = new MarkdownIt({ html: false, linkify: true, typographer: false });

function inlineText(token: Token): string {
	if (!token.children) return token.content;
	let out = "";
	for (const child of token.children) {
		if (child.type === "text" || child.type === "code_inline")
			out += child.content;
		else if (child.type === "softbreak" || child.type === "hardbreak")
			out += " ";
	}
	return out;
}

const SECTION_TEXT_CAP = 450;

md.core.ruler.push("docs", (state) => {
	const env = state.env as Env;
	const tokens = state.tokens;
	let section: TextSection = { anchor: "", heading: "", text: "" };
	env.sections.push(section);
	const addText = (text: string) => {
		if (section.text.length >= SECTION_TEXT_CAP) return;
		section.text = `${section.text} ${text}`.trim().slice(0, SECTION_TEXT_CAP);
	};

	for (let i = 0; i < tokens.length; i++) {
		const token = tokens[i] as Token;

		if (token.type === "heading_open") {
			const inline = tokens[i + 1] as Token;
			const level = Number(token.tag.slice(1));
			const text = inlineText(inline);
			const id = env.slugger.next(text);
			env.headingIds.add(id);
			token.attrSet("id", id);
			token.meta = { id, text, level };
			if (level === 2 || level === 3) {
				env.toc.push({ id, text, level });
				section = { anchor: id, heading: text, text: "" };
				env.sections.push(section);
			}
			const close = tokens[i + 2];
			if (close) close.meta = { id, text, level };
		}

		if (token.type === "blockquote_open") markCallout(tokens, i);

		if (token.type === "table_open") describeTable(tokens, i);

		if (token.type === "fence" || token.type === "code_block") {
			addText(token.content.replace(/\s+/g, " ").slice(0, 300));
		}

		if (token.type === "inline" && tokens[i - 1]?.type !== "heading_open") {
			addText(inlineText(token));
		}

		if (token.type === "inline" && token.children) {
			token.children.forEach((child, at, siblings) => {
				if (child.type === "link_open")
					rewriteHref(child, env, "href", siblings, at);
				else if (child.type === "image") rewriteImage(child, env);
			});
		}
	}
	return true;
});

/** Columns whose widest cell is this short or shorter stay on one line. */
const FIT_COLUMN_CHARS = 32;
/** A column with a cell holding this much prose (code excluded) is descriptive: CSS gives it a readable minimum width. */
const TEXT_COLUMN_CHARS = 30;

function plainLength(inline: Token): number {
	let length = 0;
	for (const child of inline.children ?? [])
		if (child.type === "text") length += child.content.length;
	return length;
}

/**
 * Sorts a table's columns by what they hold, so the stylesheet can size them:
 * a column with real sentences in it is `docs-cell-text` (never squeezed to a
 * word per line), a column of short values is `docs-cell-fit` (stays on one
 * line), anything else (long code such as an endpoint) wraps where it must.
 * The scroll frame is named from the header cells, since a scrolling region
 * needs a label.
 */
function describeTable(tokens: Token[], open: number) {
	const columns: { text: number; total: number; cells: Token[] }[] = [];
	const headers: string[] = [];
	let column = 0;
	let inHead = false;
	for (let i = open + 1; i < tokens.length; i++) {
		const token = tokens[i] as Token;
		if (token.type === "table_close") break;
		if (token.type === "thead_open") inHead = true;
		else if (token.type === "thead_close") inHead = false;
		else if (token.type === "tr_open") column = 0;
		else if (token.type === "th_open" || token.type === "td_open") {
			const inline = tokens[i + 1] as Token;
			let slot = columns[column];
			if (!slot) {
				slot = { text: 0, total: 0, cells: [] };
				columns[column] = slot;
			}
			slot.text = Math.max(slot.text, plainLength(inline));
			slot.total = Math.max(slot.total, inlineText(inline).length);
			slot.cells.push(token);
			if (inHead) {
				const label = inlineText(inline).trim();
				if (label) headers.push(label);
			}
			column++;
		}
	}
	for (const { text, total, cells } of columns) {
		const kind =
			text >= TEXT_COLUMN_CHARS
				? "docs-cell-text"
				: total <= FIT_COLUMN_CHARS
					? "docs-cell-fit"
					: "";
		if (kind) for (const cell of cells) cell.attrJoin("class", kind);
	}
	(tokens[open] as Token).meta = {
		label: headers.length ? `Table: ${headers.join(", ")}` : "Table",
	};
}

/** Turns `> [!NOTE]` blockquotes into callouts: strips the marker, tags the open and close tokens. */
function markCallout(tokens: Token[], at: number) {
	const para = tokens[at + 1];
	const inline = tokens[at + 2];
	if (para?.type !== "paragraph_open" || inline?.type !== "inline") return;
	const first = inline.children?.[0];
	if (first?.type !== "text") return;
	const match = /^\[!([A-Za-z]+)\][ \t]*/.exec(first.content);
	const kind = match ? CALLOUTS[(match[1] as string).toUpperCase()] : undefined;
	if (!match || !kind) return;
	first.content = first.content.slice(match[0].length);
	inline.content = inline.content.slice(match[0].length);
	const children = inline.children as Token[];
	if (first.content === "") {
		children.shift();
		if (children[0]?.type === "softbreak") children.shift();
	}
	inline.content = inline.content.replace(/^\s*\n/, "");
	// Close of this blockquote: the matching blockquote_close at the same depth.
	let depth = 0;
	for (let j = at; j < tokens.length; j++) {
		const t = tokens[j] as Token;
		if (t.type === "blockquote_open") depth++;
		if (t.type === "blockquote_close" && --depth === 0) {
			t.meta = { callout: kind };
			break;
		}
	}
	(tokens[at] as Token).meta = { callout: kind };
	if (children.length === 0) {
		// The marker stood alone on its line: drop the empty paragraph.
		tokens.splice(at + 1, 3);
	}
}

function rewriteHref(
	token: Token,
	env: Env,
	attr: "href",
	siblings: Token[],
	at: number,
) {
	const href = String(token.attrGet(attr) ?? "");
	const resolved = resolveLink(href, env.ctx);
	switch (resolved.kind) {
		case "error":
			env.errors.push(`link "${href}": ${resolved.message}`);
			return;
		case "anchor":
			env.anchorLinks.push(resolved.anchor);
			return;
		case "page":
			env.pageLinks.push({
				href: resolved.href,
				slug: resolved.slug,
				anchor: resolved.anchor,
			});
			token.attrSet(attr, resolved.href);
			return;
		case "repo":
			if (resolved.href === null) {
				unlink(token, siblings, at);
				return;
			}
			token.attrSet(attr, resolved.href);
			token.attrSet("target", "_blank");
			token.attrSet("rel", "noopener noreferrer");
			token.attrSet("class", "docs-external");
			return;
		case "site":
		case "external":
			token.attrSet(attr, resolved.href);
			token.attrSet("target", "_blank");
			token.attrSet("rel", "noopener noreferrer");
			token.attrSet("class", "docs-external");
			return;
	}
}

/** Turns a link into a plain span: the text stays, there is nothing to click. */
function unlink(open: Token, siblings: Token[], at: number) {
	open.tag = "span";
	open.attrs = [["class", "docs-unlinked"]];
	let depth = 0;
	for (let i = at; i < siblings.length; i++) {
		const t = siblings[i] as Token;
		if (t.type === "link_open") depth++;
		if (t.type === "link_close" && --depth === 0) {
			t.tag = "span";
			return;
		}
	}
}

function rewriteImage(token: Token, env: Env) {
	const src = String(token.attrGet("src") ?? "");
	if (!/^https:\/\//i.test(src)) {
		env.errors.push(`image "${src}": only https images are supported in docs`);
	}
}

// ---------------------------------------------------------------- renderers

md.renderer.rules.heading_close = (tokens, idx, _options, _env, self) => {
	const token = tokens[idx] as Token;
	const meta = token.meta as { id: string; text: string; level: number } | null;
	if (!meta || meta.level === 1) return self.renderToken(tokens, idx, _options);
	const label = md.utils.escapeHtml(`Link to ${meta.text}`);
	return `<a class="docs-anchor" href="#${meta.id}" aria-label="${label}">#</a></${token.tag}>\n`;
};

md.renderer.rules.table_open = (tokens, idx) => {
	const label = (tokens[idx] as Token).meta?.label as string | undefined;
	// A table wider than its column scrolls inside this frame, so the frame is a labelled, focusable region (keyboard users can scroll it).
	return `<div class="docs-table-wrap" role="region" aria-label="${md.utils.escapeHtml(label ?? "Table")}" tabindex="0"><table>\n`;
};
md.renderer.rules.table_close = () => "</table></div>\n";

md.renderer.rules.blockquote_open = (tokens, idx, options, _env, self) => {
	const kind = (tokens[idx] as Token).meta?.callout as CalloutKind | undefined;
	if (!kind) return self.renderToken(tokens, idx, options);
	return `<div class="docs-callout" data-tone="${kind.tone}" role="note">${icon(kind.tone, "docs-callout-icon")}<div class="docs-callout-body"><p class="docs-callout-title">${kind.label}</p>\n`;
};
md.renderer.rules.blockquote_close = (tokens, idx, options, _env, self) => {
	if (!(tokens[idx] as Token).meta?.callout)
		return self.renderToken(tokens, idx, options);
	return "</div></div>\n";
};

function renderCode(code: string, info: string): string {
	const requested = info.trim().split(/\s+/)[0]?.toLowerCase() ?? "";
	const lang = LANGUAGE_ALIASES[requested] ?? requested;
	const body =
		lang && hljs.getLanguage(lang)
			? hljs.highlight(code, { language: lang, ignoreIllegals: true }).value
			: md.utils.escapeHtml(code);
	const label = requested ? (LANGUAGE_LABELS[lang] ?? requested) : "";
	const chip = label
		? `<span class="docs-code-lang">${md.utils.escapeHtml(label)}</span>`
		: "<span></span>";
	const copy = `<button type="button" class="docs-code-copy" data-docs-copy aria-label="Copy code"><span class="docs-code-copy-icon">${icon("copy", "docs-icon-copy")}${icon("check", "docs-icon-done")}</span><span class="docs-code-copy-text" data-label="Copy">Copy</span></button>`;
	return `<div class="docs-code"><div class="docs-code-bar">${chip}${copy}</div><pre><code class="hljs${lang ? ` language-${lang}` : ""}">${body}</code></pre></div>\n`;
}

md.renderer.rules.fence = (tokens, idx) => {
	const token = tokens[idx] as Token;
	return renderCode(token.content.replace(/\n$/, ""), token.info);
};
md.renderer.rules.code_block = (tokens, idx) =>
	renderCode((tokens[idx] as Token).content.replace(/\n$/, ""), "");

// ---------------------------------------------------------------- entry

/** Removes `<!-- … -->` comments outside code fences, so notes to authors do not render as text. */
export function stripHtmlComments(source: string): string {
	const out: string[] = [];
	let fence: string | null = null;
	let inComment = false;
	for (const line of source.split("\n")) {
		const marker = /^\s{0,3}(`{3,}|~{3,})/.exec(line);
		if (!inComment && marker) {
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
		let rest = line;
		let result = "";
		for (;;) {
			if (inComment) {
				const end = rest.indexOf("-->");
				if (end < 0) {
					rest = "";
					break;
				}
				rest = rest.slice(end + 3);
				inComment = false;
			}
			const start = rest.indexOf("<!--");
			if (start < 0) {
				result += rest;
				break;
			}
			result += rest.slice(0, start);
			rest = rest.slice(start + 4);
			inComment = true;
		}
		out.push(result);
	}
	return out.join("\n");
}

export function compileMarkdown(
	body: string,
	opts: { title: string; ctx: LinkContext },
): CompiledPage {
	let source = stripHtmlComments(body).replace(/^\s*\n/, "");
	if (!/^#\s+\S/.test(source)) source = `# ${opts.title}\n\n${source}`;
	const env: Env = {
		ctx: opts.ctx,
		slugger: new Slugger(),
		toc: [],
		headingIds: new Set(),
		sections: [],
		pageLinks: [],
		anchorLinks: [],
		errors: [],
	};
	const html = md.render(source, env);
	return {
		html,
		toc: env.toc,
		headingIds: env.headingIds,
		sections: env.sections.filter((s) => s.text !== "" || s.anchor !== ""),
		pageLinks: env.pageLinks,
		anchorLinks: env.anchorLinks,
		errors: env.errors,
	};
}
