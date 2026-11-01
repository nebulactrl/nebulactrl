/** Pure maths for `ImageCompare`: the divider's keyboard and pointer positions. */

export const clampPercent = (value: number) =>
	Math.min(100, Math.max(0, value));

/** A position in percent, kept to one decimal. */
export const roundPercent = (value: number) =>
	Math.round(clampPercent(value) * 10) / 10;

/**
 * Where a key moves the divider to, or `null` for keys that do nothing.
 * Arrows move 1, with Shift 10; Page Up and Page Down 10; Home and End go to
 * the ends.
 */
export function keyTarget(
	key: string,
	shift: boolean,
	position: number,
): number | null {
	const step = shift ? 10 : 1;
	switch (key) {
		case "ArrowLeft":
		case "ArrowDown":
			return clampPercent(position - step);
		case "ArrowRight":
		case "ArrowUp":
			return clampPercent(position + step);
		case "PageDown":
			return clampPercent(position - 10);
		case "PageUp":
			return clampPercent(position + 10);
		case "Home":
			return 0;
		case "End":
			return 100;
		default:
			return null;
	}
}

/** The divider's position for a pointer at `clientX` over a box at `left` that is `width` wide. */
export function pointerPosition(
	clientX: number,
	left: number,
	width: number,
): number | null {
	if (width <= 0) return null;
	return roundPercent(((clientX - left) / width) * 100);
}

/** Whether a corner label is hidden because the divider has covered it. */
export function labelHidden(side: "left" | "right", position: number): boolean {
	return side === "left" ? position < 18 : position > 82;
}
