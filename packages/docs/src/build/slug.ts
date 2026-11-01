import path from "node:path/posix";

/** The URL slug of a docs file: `operations/variables.md` → `operations/variables`, `installing/README.md` → `installing`. */
export function fileSlug(rel: string): string {
	const noExt = rel.replace(/\.md$/i, "");
	const base = path.basename(noExt);
	if (base.toLowerCase() === "readme") {
		const dir = path.dirname(noExt);
		return dir === "." ? "" : dir;
	}
	return noExt;
}

/**
 * GitHub's heading slug, so the anchors the docs already use (written for
 * GitHub's renderer) keep working: lowercase, drop everything but letters,
 * digits, spaces, hyphens and underscores, then spaces become hyphens.
 */
export function slugifyHeading(text: string): string {
	return text
		.trim()
		.toLowerCase()
		.replace(/[^\p{L}\p{N}\p{M}_\- ]/gu, "")
		.replace(/ /g, "-");
}

/** Hands out unique heading ids for one page: `intro`, `intro-1`, `intro-2`. */
export class Slugger {
	private readonly seen = new Map<string, number>();

	next(text: string): string {
		const base = slugifyHeading(text) || "section";
		const count = this.seen.get(base);
		if (count === undefined) {
			this.seen.set(base, 0);
			return base;
		}
		let n = count + 1;
		let candidate = `${base}-${n}`;
		while (this.seen.has(candidate)) {
			n++;
			candidate = `${base}-${n}`;
		}
		this.seen.set(base, n);
		this.seen.set(candidate, 0);
		return candidate;
	}
}
