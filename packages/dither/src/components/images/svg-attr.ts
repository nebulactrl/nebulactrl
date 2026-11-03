const ATTR_ESCAPES: Record<string, string> = {
	"&": "&amp;",
	"<": "&lt;",
	">": "&gt;",
	'"': "&quot;",
};

/** A value safe inside a double-quoted SVG attribute: it cannot close the quote or open a tag. */
export function svgAttr(value: string | number): string {
	return String(value).replace(/[&<>"]/g, (c) => ATTR_ESCAPES[c] ?? c);
}
