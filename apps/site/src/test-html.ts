/**
 * Helpers for tests that read the markup of a rendered page as text. They
 * work on the site's own output, so they are small: tags go, the three
 * entities React emits are decoded.
 */

const TAG = /<[^>]+>/g;

/** `markup` without its tags, each replaced by `gap`. Repeated until nothing changes, so a tag cannot be rebuilt from the pieces around a removed one. */
export function stripTags(markup: string, gap = ""): string {
	let text = markup;
	for (let previous = ""; previous !== text; ) {
		previous = text;
		text = text.replace(TAG, gap);
	}
	return text;
}

const ENTITIES: Record<string, string> = {
	"&amp;": "&",
	"&quot;": '"',
	"&#x27;": "'",
};

/** The entities React escapes text and attributes with, decoded in one pass so `&amp;quot;` stays `&quot;`. */
export function decodeEntities(text: string): string {
	return text.replace(
		/&(?:amp|quot|#x27);/g,
		(entity) => ENTITIES[entity] ?? entity,
	);
}

/** Visible prose of a page: no scripts, styles, terminals, code, icons or attributes. */
export function visibleText(page: string): string {
	return decodeEntities(
		stripTags(
			page.replace(/<(script|style|pre|code|svg|noscript)[\s\S]*?<\/\1>/g, " "),
			" ",
		),
	).replace(/\s+/g, " ");
}
