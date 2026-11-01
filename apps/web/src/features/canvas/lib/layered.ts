import type { Point } from "#/api/models";

/** The canvas lattice (`--canvas-lattice`): every anchor Arrange produces sits on it. */
export const GRID = 24;
/** Free space between two columns (layers): room for the cables to fan out. */
export const LAYER_GAP = 120;
/** Free space between two cards in a column. */
export const NODE_GAP = 24;
/** A long cable's reserved lane through a column it crosses. */
const LANE_GAP = GRID;
/** How hard a lane holds its line against the cards beside it: a long cable runs near straight. */
const LANE_WEIGHT = 4;

export const ceilGrid = (n: number) => Math.ceil(n / GRID) * GRID;
const floorGrid = (n: number) => Math.floor(n / GRID) * GRID;
const roundGrid = (n: number) => Math.round(n / GRID) * GRID;

/** One box to place. */
export interface LayerVertex {
	id: string;
	w: number;
	h: number;
	/**
	 * Where the grid-aligned anchor sits inside the box, below its top edge:
	 * zero for a card, the header and padding of a group frame (whose members,
	 * not its frame, are what the lattice aligns).
	 */
	insetY?: number;
	/** The Internet feeds it: first in its column, so its cable drops in clear. */
	entry?: boolean;
	/** A database or cache: pushed to the last column when nothing leaves it. */
	data?: boolean;
}

export interface LayeredResult {
	/** Each box's top-left corner, from a (0, 0) origin; anchors are on the grid. */
	boxes: Map<string, Point>;
	/** The column of each vertex, from 0. */
	columns: Map<string, number>;
	/** Extent of the boxes. */
	width: number;
	height: number;
	/** Crossings left between neighbouring columns (a long cable counts through its lane). */
	crossings: number;
}

// A column holds real vertices and the dummy lanes of cables that cross it.
interface Slot {
	real: number; // vertex index, or -1 for a lane
	h: number;
	inset: number;
	entry: boolean;
	up: number[];
	down: number[];
}

/**
 * A layered (Sugiyama) layout, left to right:
 *
 * 1. Columns: longest path from the sources, after breaking cycles. Data
 *    vertices that nothing leaves go to the last column; a source that is not
 *    an entry moves up against its nearest target, so a worker sits beside
 *    the app it feeds and not at the far left.
 * 2. Order inside columns: barycentre sweeps with adjacent-swap refinement
 *    over a graph where every cable that skips a column carries a lane through
 *    it, keeping the entries first.
 * 3. Rows: each column's anchors follow the mean height of their neighbours
 *    (isotonic projection, so order and gaps hold), on the lattice.
 *
 * Vertices are tried in the order given, so the same input gives the same
 * layout.
 */
export function layeredLayout(
	vertices: readonly LayerVertex[],
	links: ReadonlyArray<readonly [string, string]>,
): LayeredResult {
	const n = vertices.length;
	if (n === 0) {
		return {
			boxes: new Map(),
			columns: new Map(),
			width: 0,
			height: 0,
			crossings: 0,
		};
	}
	const index = new Map(vertices.map((v, i) => [v.id, i] as const));

	// Edges: known, distinct, no self loops.
	const seen = new Set<string>();
	const edges: Array<[number, number]> = [];
	for (const [from, to] of links) {
		const a = index.get(from);
		const b = index.get(to);
		if (a === undefined || b === undefined || a === b) continue;
		const key = `${a}>${b}`;
		if (seen.has(key)) continue;
		seen.add(key);
		edges.push([a, b]);
	}

	// The order the links came in must not matter.
	edges.sort((p, q) => p[0] - q[0] || p[1] - q[1]);
	const dag = breakCycles(n, edges);
	const column = assignColumns(vertices, dag);

	// Slots per column, with lanes for every cable that skips columns.
	const lastColumn = Math.max(...column);
	const slots: Slot[] = [];
	const columns: number[][] = Array.from({ length: lastColumn + 1 }, () => []);
	for (let i = 0; i < n; i++) {
		const vertex = vertices[i] as LayerVertex;
		slots.push({
			real: i,
			h: vertex.h,
			inset: vertex.insetY ?? 0,
			entry: vertex.entry === true,
			up: [],
			down: [],
		});
		columns[column[i] as number]?.push(i);
	}
	const link = (from: number, to: number) => {
		slots[from]?.down.push(to);
		slots[to]?.up.push(from);
	};
	for (const [a, b] of dag) {
		let previous = a;
		for (let c = (column[a] as number) + 1; c < (column[b] as number); c++) {
			const lane = slots.length;
			slots.push({
				real: -1,
				h: 0,
				inset: 0,
				entry: false,
				up: [],
				down: [],
			});
			columns[c]?.push(lane);
			link(previous, lane);
			previous = lane;
		}
		link(previous, b);
	}

	const crossings = orderColumns(slots, columns);
	const anchors = placeRows(slots, columns);

	// Columns: left-aligned, as wide as their widest vertex.
	const x: number[] = [];
	let cursor = 0;
	for (const members of columns) {
		x.push(cursor);
		let width = 0;
		for (const s of members) {
			const real = slots[s]?.real ?? -1;
			if (real >= 0) width = Math.max(width, (vertices[real] as LayerVertex).w);
		}
		cursor += ceilGrid(width) + LAYER_GAP;
	}

	let top = Number.POSITIVE_INFINITY;
	for (let i = 0; i < n; i++) {
		top = Math.min(top, (anchors[i] as number) - (slots[i] as Slot).inset);
	}
	// Whole lattice steps only, so the anchors stay on it.
	const shift = -floorGrid(top);

	const boxes = new Map<string, Point>();
	const columnOf = new Map<string, number>();
	let width = 0;
	let height = 0;
	for (let i = 0; i < n; i++) {
		const vertex = vertices[i] as LayerVertex;
		const at = {
			x: x[column[i] as number] as number,
			y: (anchors[i] as number) - (slots[i] as Slot).inset + shift,
		};
		boxes.set(vertex.id, at);
		columnOf.set(vertex.id, column[i] as number);
		width = Math.max(width, at.x + vertex.w);
		height = Math.max(height, at.y + vertex.h);
	}
	return { boxes, columns: columnOf, width, height, crossings };
}

/** The edges with every back edge of a depth-first walk turned round. */
function breakCycles(
	n: number,
	edges: ReadonlyArray<readonly [number, number]>,
): Array<[number, number]> {
	const out: number[][] = Array.from({ length: n }, () => []);
	for (const [a, b] of edges) out[a]?.push(b);
	const state = new Array<number>(n).fill(0); // 1 on the stack, 2 done
	const back = new Set<string>();
	const visit = (v: number) => {
		state[v] = 1;
		for (const w of out[v] ?? []) {
			if (state[w] === 1) back.add(`${v}>${w}`);
			else if (state[w] === 0) visit(w);
		}
		state[v] = 2;
	};
	for (let v = 0; v < n; v++) if (state[v] === 0) visit(v);
	const kept = new Set<string>();
	const dag: Array<[number, number]> = [];
	for (const [a, b] of edges) {
		const [from, to] = back.has(`${a}>${b}`) ? [b, a] : [a, b];
		const key = `${from}>${to}`;
		if (kept.has(key)) continue;
		kept.add(key);
		dag.push([from, to]);
	}
	return dag;
}

function assignColumns(
	vertices: readonly LayerVertex[],
	dag: ReadonlyArray<readonly [number, number]>,
): number[] {
	const n = vertices.length;
	const preds: number[][] = Array.from({ length: n }, () => []);
	const succs: number[][] = Array.from({ length: n }, () => []);
	for (const [a, b] of dag) {
		preds[b]?.push(a);
		succs[a]?.push(b);
	}
	// Longest path, visiting in topological order (lowest index first).
	const column = new Array<number>(n).fill(0);
	const waiting = preds.map((p) => p.length);
	const ready: number[] = [];
	for (let v = 0; v < n; v++) if (waiting[v] === 0) ready.push(v);
	while (ready.length > 0) {
		ready.sort((a, b) => a - b);
		const v = ready.shift() as number;
		for (const w of succs[v] ?? []) {
			column[w] = Math.max(column[w] as number, (column[v] as number) + 1);
			waiting[w] = (waiting[w] as number) - 1;
			if (waiting[w] === 0) ready.push(w);
		}
	}
	// Data goes to the last column, past every app that uses it. Cards that
	// are not data stay where their links put them, so a leaf app (a
	// renderer nothing else uses) may share the last column with the data.
	const last = Math.max(...column);
	for (let v = 0; v < n; v++) {
		const vertex = vertices[v] as LayerVertex;
		if (
			vertex.data === true &&
			(preds[v]?.length ?? 0) > 0 &&
			(succs[v]?.length ?? 0) === 0
		) {
			column[v] = last;
		}
	}
	for (let v = 0; v < n; v++) {
		const vertex = vertices[v] as LayerVertex;
		const targets = succs[v] ?? [];
		if (
			(preds[v]?.length ?? 0) === 0 &&
			targets.length > 0 &&
			vertex.entry !== true
		) {
			column[v] = Math.min(...targets.map((w) => column[w] as number)) - 1;
		}
	}
	// No empty columns.
	const used = [...new Set(column)].sort((a, b) => a - b);
	return column.map((c) => used.indexOf(c));
}

/** Crossings between column `c` and the next, from the current order. */
function crossingsBetween(
	slots: readonly Slot[],
	columns: readonly number[][],
	position: readonly number[],
	c: number,
): number {
	const pairs: Array<[number, number]> = [];
	for (const s of columns[c] ?? []) {
		for (const t of slots[s]?.down ?? []) {
			pairs.push([position[s] as number, position[t] as number]);
		}
	}
	let count = 0;
	for (let i = 0; i < pairs.length; i++) {
		const p = pairs[i] as [number, number];
		for (let j = i + 1; j < pairs.length; j++) {
			const q = pairs[j] as [number, number];
			if ((p[0] - q[0]) * (p[1] - q[1]) < 0) count += 1;
		}
	}
	return count;
}

function totalCrossings(
	slots: readonly Slot[],
	columns: readonly number[][],
	position: readonly number[],
): number {
	let total = 0;
	for (let c = 0; c < columns.length - 1; c++) {
		total += crossingsBetween(slots, columns, position, c);
	}
	return total;
}

/** Barycentre sweeps and adjacent swaps; reorders `columns` in place. */
function orderColumns(slots: readonly Slot[], columns: number[][]): number {
	const position = new Array<number>(slots.length).fill(0);
	const refresh = () => {
		for (const members of columns) {
			members.forEach((s, i) => {
				position[s] = i;
			});
		}
	};
	// Entries first: a column's entries stay above its other cards.
	const rank = (s: number) => (slots[s]?.entry ? 0 : 1);
	for (const members of columns) members.sort((a, b) => rank(a) - rank(b));
	refresh();
	const sweep = (c: number, towards: "up" | "down") => {
		const members = columns[c] as number[];
		const keyed = members.map((s, i) => {
			const neighbours = slots[s]?.[towards] ?? [];
			const mean =
				neighbours.length === 0
					? i
					: neighbours.reduce((sum, t) => sum + (position[t] as number), 0) /
						neighbours.length;
			return { s, i, mean };
		});
		keyed.sort((a, b) => rank(a.s) - rank(b.s) || a.mean - b.mean || a.i - b.i);
		columns[c] = keyed.map((k) => k.s);
		refresh();
	};

	let best = columns.map((members) => [...members]);
	let bestCount = totalCrossings(slots, columns, position);
	for (let round = 0; round < 24 && bestCount > 0; round++) {
		for (let c = 1; c < columns.length; c++) sweep(c, "up");
		for (let c = columns.length - 2; c >= 0; c--) sweep(c, "down");
		const count = totalCrossings(slots, columns, position);
		if (count < bestCount) {
			bestCount = count;
			best = columns.map((members) => [...members]);
		}
	}
	for (let c = 0; c < columns.length; c++)
		columns[c] = [...(best[c] as number[])];
	refresh();

	// Adjacent swaps while one removes a crossing.
	for (let pass = 0; pass < 12; pass++) {
		let improved = false;
		for (let c = 0; c < columns.length; c++) {
			const members = columns[c] as number[];
			for (let i = 0; i + 1 < members.length; i++) {
				const a = members[i] as number;
				const b = members[i + 1] as number;
				if (rank(a) === 0 && rank(b) !== 0) continue;
				const around = (k: number) =>
					(k > 0 ? crossingsBetween(slots, columns, position, k - 1) : 0) +
					(k < columns.length - 1
						? crossingsBetween(slots, columns, position, k)
						: 0);
				const before = around(c);
				members[i] = b;
				members[i + 1] = a;
				position[a] = i + 1;
				position[b] = i;
				if (around(c) < before) {
					improved = true;
				} else {
					members[i] = a;
					members[i + 1] = b;
					position[a] = i;
					position[b] = i + 1;
				}
			}
		}
		if (!improved) break;
	}
	return totalCrossings(slots, columns, position);
}

/** Anchor heights for every slot: neighbours' mean height, on the lattice. */
function placeRows(
	slots: readonly Slot[],
	columns: readonly number[][],
): number[] {
	const anchor = new Array<number>(slots.length).fill(0);
	const gapOf = (a: number, b: number) =>
		(slots[a] as Slot).real >= 0 && (slots[b] as Slot).real >= 0
			? NODE_GAP
			: LANE_GAP;
	// The anchors' least distance: box edge to box edge plus the gap, on the lattice.
	const stepOf = (a: number, b: number) => {
		const upper = slots[a] as Slot;
		const lower = slots[b] as Slot;
		return ceilGrid(upper.h - upper.inset + lower.inset + gapOf(a, b));
	};
	const steps = columns.map((members) =>
		members.slice(1).map((s, i) => stepOf(members[i] as number, s)),
	);

	for (const [c, members] of columns.entries()) {
		let at = 0;
		members.forEach((s, i) => {
			if (i > 0) at += (steps[c] as number[])[i - 1] as number;
			anchor[s] = at;
		});
	}

	const centre = (s: number) => {
		const slot = slots[s] as Slot;
		return (anchor[s] as number) - slot.inset + slot.h / 2;
	};
	const settle = (c: number, towards: "up" | "down") => {
		const members = columns[c] as number[];
		const wanted = members.map((s) => {
			const neighbours = (slots[s] as Slot)[towards];
			const slot = slots[s] as Slot;
			if (neighbours.length === 0) return anchor[s] as number;
			const mean =
				neighbours.reduce((sum, t) => sum + centre(t), 0) / neighbours.length;
			return mean - slot.h / 2 + slot.inset;
		});
		// Closest anchors that keep the order and the steps: pool adjacent violators.
		const offsets: number[] = [0];
		for (const step of steps[c] ?? []) {
			offsets.push((offsets.at(-1) as number) + step);
		}
		const blocks: Array<{
			sum: number;
			weight: number;
			count: number;
			value: number;
		}> = [];
		for (const [i, w] of wanted.entries()) {
			const weight =
				(slots[members[i] as number] as Slot).real >= 0 ? 1 : LANE_WEIGHT;
			const z = w - (offsets[i] as number);
			blocks.push({ sum: z * weight, weight, count: 1, value: z });
			while (blocks.length > 1) {
				const last = blocks[blocks.length - 1] as (typeof blocks)[number];
				const before = blocks[blocks.length - 2] as (typeof blocks)[number];
				if (before.value <= last.value) break;
				const sum = before.sum + last.sum;
				const weight = before.weight + last.weight;
				blocks.splice(-2, 2, {
					sum,
					weight,
					count: before.count + last.count,
					value: sum / weight,
				});
			}
		}
		let i = 0;
		for (const block of blocks) {
			for (let k = 0; k < block.count; k++, i++) {
				anchor[members[i] as number] =
					roundGrid(block.value) + (offsets[i] as number);
			}
		}
	};

	for (let round = 0; round < 8; round++) {
		for (let c = 1; c < columns.length; c++) settle(c, "up");
		for (let c = columns.length - 2; c >= 0; c--) settle(c, "down");
	}
	return anchor;
}
