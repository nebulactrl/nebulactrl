export function syncByTimestamp(
	ticks: ReadonlyArray<{ value?: unknown }>,
	data: { activeLabel?: string | number | undefined },
): number {
	const target = Number(data.activeLabel);
	if (!Number.isFinite(target)) return -1;
	let best = -1;
	let bestDistance = Number.POSITIVE_INFINITY;
	ticks.forEach((tick, index) => {
		const distance = Math.abs(Number(tick.value) - target);
		if (distance < bestDistance) {
			best = index;
			bestDistance = distance;
		}
	});
	return best;
}

const MINUTE = 60_000;
const HOUR = 60 * MINUTE;
const DAY = 24 * HOUR;

const TIME_STEPS_MS = [
	MINUTE,
	5 * MINUTE,
	10 * MINUTE,
	15 * MINUTE,
	30 * MINUTE,
	HOUR,
	2 * HOUR,
	3 * HOUR,
	6 * HOUR,
	12 * HOUR,
	DAY,
	2 * DAY,
] as const;

/**
 * Axis ticks for the time range [from, to] (epoch ms) on round wall-clock
 * marks in the visitor's own time zone: at most `maxTicks` of them where a
 * step that large exists, spaced by the smallest of a minute, 5, 10, 15 or
 * 30 minutes, an hour or a few, or a day or two.
 *
 * `offsetMinutes` is how far the local clock is ahead of UTC; it defaults
 * to the visitor's at `from`, and is a parameter so tests are not
 * time-zone dependent.
 */
export function timeTicks(
	from: number,
	to: number,
	maxTicks = 6,
	offsetMinutes = -new Date(from).getTimezoneOffset(),
): number[] {
	const span = to - from;
	if (!(span > 0)) return [];
	const step =
		TIME_STEPS_MS.find((s) => span / s <= maxTicks) ?? TIME_STEPS_MS.at(-1);
	if (step === undefined) return [];
	const offset = offsetMinutes * MINUTE;
	const ticks: number[] = [];
	for (
		let t = Math.ceil((from + offset) / step) * step - offset;
		t <= to;
		t += step
	) {
		ticks.push(t);
	}
	return ticks;
}
