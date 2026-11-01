/** A rectangle in the canvas's own coordinates. */
export interface Rect {
	left: number;
	top: number;
	right: number;
	bottom: number;
}

export interface DockPlacement {
	/** Screen x of the selected card's centre. */
	cardCentreX: number;
	/** Screen y of the selected card's bottom edge. */
	cardBottom: number;
	dockWidth: number;
	dockHeight: number;
	/** Free space between the card and the dock. */
	gap: number;
	/** The least distance kept from the canvas edges. */
	margin: number;
	/** The part of the canvas that is free: the inspector's width is already taken off. */
	viewportWidth: number;
	viewportHeight: number;
	/**
	 * Chrome the dock must not cover, such as the zoom stack in the lower-left
	 * corner. A dock that would land on it moves to its right edge or above it.
	 */
	keepOut?: Rect;
}

function clamp(value: number, min: number, max: number): number {
	return Math.min(Math.max(value, min), Math.max(min, max));
}

function overlaps(a: Rect, b: Rect): boolean {
	return (
		a.left < b.right && a.right > b.left && a.top < b.bottom && a.bottom > b.top
	);
}

/**
 * Where the dock goes: centred under the card, pulled back inside the free
 * part of the canvas when the card is near an edge or half off screen, and
 * clear of the chrome in `keepOut` (it slides past it, or hops over it,
 * whichever is the shorter move that still fits).
 */
export function dockPosition(input: DockPlacement): {
	left: number;
	top: number;
} {
	const left = clamp(
		input.cardCentreX - input.dockWidth / 2,
		input.margin,
		input.viewportWidth - input.dockWidth - input.margin,
	);
	const top = clamp(
		input.cardBottom + input.gap,
		input.margin,
		input.viewportHeight - input.dockHeight - input.margin,
	);
	const placed = { left: Math.round(left), top: Math.round(top) };
	const out = input.keepOut;
	if (
		!out ||
		!overlaps(
			{
				...placed,
				right: placed.left + input.dockWidth,
				bottom: placed.top + input.dockHeight,
			},
			out,
		)
	) {
		return placed;
	}
	const options: Array<{ left: number; top: number; moved: number }> = [];
	const pastRight = Math.round(out.right + input.margin);
	if (pastRight + input.dockWidth <= input.viewportWidth - input.margin) {
		options.push({
			left: pastRight,
			top: placed.top,
			moved: pastRight - placed.left,
		});
	}
	const above = Math.round(out.top - input.margin - input.dockHeight);
	if (above >= input.margin) {
		options.push({
			left: placed.left,
			top: above,
			moved: placed.top - above,
		});
	}
	const best = options.sort((a, b) => a.moved - b.moved)[0];
	return best ? { left: best.left, top: best.top } : placed;
}

export interface RevealInput {
	/** The selected card in screen space. */
	card: { left: number; top: number; right: number; bottom: number };
	/** Bottom edge of the dock under it. */
	dockBottom: number;
	viewWidth: number;
	viewHeight: number;
	/** Width of the inspector covering the canvas's right edge. */
	rightInset: number;
	margin: number;
	/** Room kept at the top for the canvas toolbars. */
	topMargin: number;
}

/**
 * How far to pan so the selected card and its dock sit inside the part of
 * the canvas the inspector leaves free. The card's left edge wins when the
 * card is wider than that part.
 */
export function revealShift(input: RevealInput): { dx: number; dy: number } {
	const { card } = input;
	const freeRight = input.viewWidth - input.rightInset - input.margin;
	let dx = 0;
	if (card.right > freeRight) dx = freeRight - card.right;
	if (card.left + dx < input.margin) dx = input.margin - card.left;
	const bottom = Math.max(card.bottom, input.dockBottom);
	let dy = 0;
	if (bottom > input.viewHeight - input.margin) {
		dy = input.viewHeight - input.margin - bottom;
	}
	if (card.top + dy < input.topMargin) dy = input.topMargin - card.top;
	return { dx: Math.round(dx), dy: Math.round(dy) };
}
