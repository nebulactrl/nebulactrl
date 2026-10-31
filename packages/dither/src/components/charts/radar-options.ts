/**
 * Pure helpers for `DitherRadarChart`: which axis a pointer is nearest to,
 * circular grid rings, and the tone of each ring band of a gradient fill.
 * Covered by `radar-options.test.ts`.
 */

/**
 * The axis nearest to a pointer, from its offset (`dx`, `dy`, screen
 * pixels) from the chart's centre. Axis 0 points up and the rest follow
 * clockwise. -1 when there are no axes.
 */
export function axisAtOffset(dx: number, dy: number, count: number): number {
	if (count <= 0) return -1;
	const turn = Math.PI * 2;
	const angle = ((Math.atan2(dx, -dy) % turn) + turn) % turn;
	return Math.round(angle / (turn / count)) % count;
}

const r1 = (value: number) => Math.round(value * 10) / 10;

/** A closed circle as path data (two half arcs), for round grid rings. */
export function circlePath(cx: number, cy: number, r: number): string {
	return `M${r1(cx - r)} ${r1(cy)}A${r1(r)} ${r1(r)} 0 1 0 ${r1(cx + r)} ${r1(cy)}A${r1(r)} ${r1(r)} 0 1 0 ${r1(cx - r)} ${r1(cy)}Z`;
}

/**
 * Tone of each ring band of a gradient fill, innermost first: sparse at the
 * centre, denser ring by ring toward the outside.
 */
export function ringBandTones(rings: number, min = 0.14, max = 0.62): number[] {
	if (rings <= 1) return [max];
	return Array.from(
		{ length: rings },
		(_, k) => min + ((max - min) * k) / (rings - 1),
	);
}
