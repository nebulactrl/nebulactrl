// Time order for a merged log. The environment tail is one stream made of
// every service's own tail, so the first burst arrives service by service
// (all of `api`'s backlog, then all of `web`'s) and a slow service's lines can
// land behind newer ones from a fast one. `mergeByTime` puts a batch of new
// lines where their time says, and keeps ids ascending: the search buffer and
// the list's React keys rely on that, so a line placed in the middle takes a
// fractional id between its neighbours.

/** The two fields the merge reads. */
export interface Ordered {
	/** Ascending through the array; unique; above 0. */
	id: number;
	/** RFC 3339 time the source gave the line. */
	ts?: string | undefined;
}

/** The gap below which ids stop being told apart and the array is renumbered. */
const MIN_GAP = 1e-9;

/** The time a line sits at, set when the merge places it. */
const placed = new WeakMap<object, number>();

function instant(ts: string | undefined): number | undefined {
	if (!ts) return undefined;
	const ms = Date.parse(ts);
	return Number.isNaN(ms) ? undefined : ms;
}

function keyOf(line: Ordered): number {
	return placed.get(line) ?? instant(line.ts) ?? Number.NEGATIVE_INFINITY;
}

function withId<L extends Ordered>(line: L, id: number): L {
	if (line.id === id) return line;
	const copy = { ...line, id };
	const key = placed.get(line);
	if (key !== undefined) placed.set(copy, key);
	return copy;
}

/** The first index whose line is later than `key`: a line goes after its equals. */
function upperBound(lines: readonly Ordered[], key: number): number {
	let low = 0;
	let high = lines.length;
	while (low < high) {
		const mid = (low + high) >>> 1;
		if (keyOf(lines[mid] as Ordered) <= key) low = mid + 1;
		else high = mid;
	}
	return low;
}

export interface Merged<L> {
	lines: L[];
	/**
	 * A line landed before ones already shown. Anything that cached the old
	 * array's lines by position (the search buffer) has to start over.
	 */
	reordered: boolean;
}

interface Keyed<L> {
	line: L;
	key: number;
}

/**
 * Merges `batch` (in arrival order) into `current` (in time order). A line
 * with no time of its own stays behind the line that arrived before it.
 * Lines of equal time keep arrival order. A batch that is already later than
 * everything shown just appends.
 */
export function mergeByTime<L extends Ordered>(
	current: readonly L[],
	batch: readonly L[],
): Merged<L> {
	if (batch.length === 0) return { lines: current.slice(), reordered: false };

	const newest = current.at(-1);
	const lastKey = newest ? keyOf(newest) : Number.NEGATIVE_INFINITY;
	let carry = lastKey;
	const keyed: Keyed<L>[] = batch.map((line) => {
		const key = instant(line.ts) ?? carry;
		carry = key;
		placed.set(line, key);
		return { line, key };
	});
	// `sort` is stable, so equal times keep their arrival order.
	keyed.sort((a, b) => (a.key < b.key ? -1 : a.key > b.key ? 1 : 0));

	const split = keyed.findIndex((entry) => entry.key >= lastKey);
	const middle = split === -1 ? keyed : keyed.slice(0, split);
	const tail = split === -1 ? [] : keyed.slice(split);

	// The tail takes the batch's own arrival ids in ascending order, so the
	// array stays ascending whatever order the sort gave.
	const pool = tail.map((entry) => entry.line.id).sort((a, b) => a - b);
	const appended = tail.map((entry, i) => withId(entry.line, pool[i] ?? 0));
	if (middle.length === 0) {
		return { lines: current.concat(appended), reordered: false };
	}

	// Group the middle lines by where they land, then walk `current` once.
	const groups = new Map<number, L[]>();
	for (const entry of middle) {
		const at = upperBound(current, entry.key);
		const group = groups.get(at);
		if (group) group.push(entry.line);
		else groups.set(at, [entry.line]);
	}
	const merged: L[] = [];
	let cursor = 0;
	let crowded = false;
	for (const [at, group] of [...groups].sort((a, b) => a[0] - b[0])) {
		for (; cursor < at; cursor++) merged.push(current[cursor] as L);
		const low = current[at - 1]?.id ?? 0;
		const high = current[at]?.id ?? low + 1;
		const step = (high - low) / (group.length + 1);
		if (step < MIN_GAP) crowded = true;
		group.forEach((line, j) => {
			merged.push(withId(line, low + step * (j + 1)));
		});
	}
	for (; cursor < current.length; cursor++) merged.push(current[cursor] as L);
	merged.push(...appended);

	return {
		lines: crowded ? merged.map((line, i) => withId(line, i + 1)) : merged,
		reordered: true,
	};
}
