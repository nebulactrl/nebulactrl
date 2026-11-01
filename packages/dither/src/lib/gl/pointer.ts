/**
 * The eased pointer behind `u_pointer`. Pure, so the loop can stop easing the
 * moment the value has reached its target.
 */

export interface PointerPoint {
	x: number;
	y: number;
}

/** Eased and target values closer than this are the same point. */
export const POINTER_SETTLE = 1e-4;

/** Exponential ease rate, per second. */
const EASE_RATE = 8;

/** Clamp a point to the 0..1 box `u_pointer` lives in. */
export function clampPointer(x: number, y: number): [number, number] {
	return [Math.min(1, Math.max(0, x)), Math.min(1, Math.max(0, y))];
}

/**
 * Move `now` towards `target` over `delta` seconds, in place. Within
 * `POINTER_SETTLE` it lands exactly on the target and does no more work.
 * Returns whether it moved, so a caller can tell a settled pointer.
 */
export function easePointer(
	now: PointerPoint,
	target: PointerPoint,
	delta: number,
): boolean {
	const dx = target.x - now.x;
	const dy = target.y - now.y;
	if (Math.abs(dx) < POINTER_SETTLE && Math.abs(dy) < POINTER_SETTLE) {
		const moved = dx !== 0 || dy !== 0;
		now.x = target.x;
		now.y = target.y;
		return moved;
	}
	const k = 1 - Math.exp(-delta * EASE_RATE);
	now.x += dx * k;
	now.y += dy * k;
	return true;
}
