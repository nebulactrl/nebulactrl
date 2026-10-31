import path from "node:path/posix";
import { docHref, REPO_BRANCH, REPO_URL } from "../types.ts";

/** What a link in a docs page points at, after rewriting. */
export type ResolvedLink =
	| { kind: "anchor"; href: string; anchor: string }
	| { kind: "external"; href: string }
	/** Another docs page: `slug` is checked against the corpus afterwards. */
	| { kind: "page"; href: string; slug: string; anchor: string }
	/** A page the control plane itself serves outside the docs: llms.txt, the OpenAPI document. */
	| { kind: "site"; href: string }
	/**
	 * A file or folder of the repository outside the docs (or an excluded docs file).
	 * `href` is null when the context has no public repository (`repoUrl: null`):
	 * the target is still checked, but there is nothing to link to.
	 */
	| { kind: "repo"; href: string | null }
	| { kind: "error"; message: string };

export interface LinkContext {
	/** The linking file, relative to `docs/`, posix: "operations/variables.md". */
	fromFile: string;
	/** Included docs files (relative to `docs/`) → slug. */
	pages: ReadonlyMap<string, string>;
	/** What sits at a repo-relative path: used for links that leave `docs/`. */
	repoEntry(repoPath: string): "file" | "dir" | null;
	/**
	 * Where the repository lives on the web, for links that leave `docs/`.
	 * Omitted: the default `REPO_URL`. `null`: the repository is not public, so
	 * such links (and explicit links to `REPO_URL`) render as plain text.
	 */
	repoUrl?: string | null;
}

/** Paths the control plane serves next to /docs that a page may link to. */
export const SITE_PATHS: readonly string[] = [
	"/llms.txt",
	"/llms-full.txt",
	"/api/v1/openapi.json",
];

const EXTERNAL = /^([a-z][a-z0-9+.-]*:|\/\/)/i;

/**
 * Rewrites one markdown link target.
 *
 * - `#anchor` stays on the page.
 * - `x.md#y` below `docs/` becomes `/docs/<slug>#y` (the page and anchor are
 *   verified later, once every page is compiled).
 * - Anything else relative must exist in the repo and becomes a GitHub link.
 * - Absolute paths other than `/docs/…` are rejected: they cannot be checked.
 */
export function resolveLink(href: string, ctx: LinkContext): ResolvedLink {
	const raw = href.trim();
	if (raw === "") return { kind: "error", message: "empty link target" };
	if (raw.startsWith("#")) {
		return { kind: "anchor", href: raw, anchor: decode(raw.slice(1)) };
	}
	if (
		ctx.repoUrl === null &&
		(raw === REPO_URL || raw.startsWith(`${REPO_URL}/`))
	) {
		return { kind: "repo", href: null };
	}
	if (EXTERNAL.test(raw)) return { kind: "external", href: raw };

	const hashAt = raw.indexOf("#");
	const target = hashAt < 0 ? raw : raw.slice(0, hashAt);
	const anchor = hashAt < 0 ? "" : decode(raw.slice(hashAt + 1));
	if (target.startsWith("/")) {
		if (target === "/docs" || target.startsWith("/docs/")) {
			const slug = target.replace(/^\/docs\/?/, "");
			return { kind: "page", href: docHref(slug, anchor), slug, anchor };
		}
		if (SITE_PATHS.includes(target)) {
			return { kind: "site", href: anchor ? `${target}#${anchor}` : target };
		}
		return {
			kind: "error",
			message: `absolute path ${target} cannot be checked; link a relative path`,
		};
	}

	const joined = path.normalize(path.join(path.dirname(ctx.fromFile), target));
	const isDir = target.endsWith("/");
	const clean = joined.replace(/\/$/, "");
	if (clean === ".." || clean.startsWith("../")) {
		// Outside docs/: resolve against the repo root (docs/ sits one level down).
		const repoPath = path
			.normalize(path.join("docs", clean))
			.replace(/\/$/, "");
		if (repoPath.startsWith("..")) {
			return { kind: "error", message: `${raw} leaves the repository` };
		}
		const entry = ctx.repoEntry(repoPath);
		if (!entry)
			return { kind: "error", message: `${raw} does not exist (${repoPath})` };
		return {
			kind: "repo",
			href: repoUrl(ctx, repoPath, entry === "dir" || isDir, anchor),
		};
	}

	const slug = ctx.pages.get(clean);
	if (slug !== undefined) {
		return { kind: "page", href: docHref(slug, anchor), slug, anchor };
	}
	// A directory link to a docs folder whose README is a page.
	const readme = ctx.pages.get(path.join(clean, "README.md"));
	if (readme !== undefined && (isDir || !/\.[a-z0-9]+$/i.test(clean))) {
		return {
			kind: "page",
			href: docHref(readme, anchor),
			slug: readme,
			anchor,
		};
	}
	const repoPath = path.join("docs", clean);
	const entry = ctx.repoEntry(repoPath);
	if (!entry)
		return { kind: "error", message: `${raw} does not exist (${repoPath})` };
	return {
		kind: "repo",
		href: repoUrl(ctx, repoPath, entry === "dir" || isDir, anchor),
	};
}

function repoUrl(
	ctx: LinkContext,
	repoPath: string,
	dir: boolean,
	anchor: string,
): string | null {
	if (ctx.repoUrl === null) return null;
	const url = `${ctx.repoUrl ?? REPO_URL}/${dir ? "tree" : "blob"}/${REPO_BRANCH}/${repoPath}`;
	return anchor ? `${url}#${anchor}` : url;
}

function decode(value: string): string {
	try {
		return decodeURIComponent(value);
	} catch {
		return value;
	}
}
