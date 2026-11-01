/** A rectangle in viewport px, as `getBoundingClientRect` and `Range.getClientRects` return them. */
export interface LineRect {
	left: number;
	top: number;
	right: number;
	bottom: number;
}

/**
 * One rectangle per line of text. A range over a paragraph returns a box for
 * every run of text and for every inline element, so a line is often there
 * twice; boxes that share most of their height are one line, joined sideways.
 */
export function mergeLines(boxes: readonly LineRect[]): LineRect[] {
	const sorted = boxes
		.filter((b) => b.right > b.left && b.bottom > b.top)
		// DOMRect keeps its edges on the prototype, so a spread would copy none of them.
		.map((b) => ({
			left: b.left,
			top: b.top,
			right: b.right,
			bottom: b.bottom,
		}))
		.sort((a, b) => a.top - b.top || a.left - b.left);
	const lines: LineRect[] = [];
	for (const box of sorted) {
		const line = lines.find((l) => {
			const overlap = Math.min(l.bottom, box.bottom) - Math.max(l.top, box.top);
			return overlap > 0.5 * Math.min(l.bottom - l.top, box.bottom - box.top);
		});
		if (!line) {
			lines.push(box);
			continue;
		}
		line.left = Math.min(line.left, box.left);
		line.right = Math.max(line.right, box.right);
		line.top = Math.min(line.top, box.top);
		line.bottom = Math.max(line.bottom, box.bottom);
	}
	return lines;
}

/** The lines of text inside `node`, each as its own rectangle. */
export function textLines(node: Element): LineRect[] {
	const range = document.createRange();
	range.selectNodeContents(node);
	return mergeLines(Array.from(range.getClientRects()));
}
