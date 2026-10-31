/**
 * Blue-noise threshold tiles by void-and-cluster (Ulichney, 1993). Written
 * from the algorithm: no table is shipped, the tile is generated on first use
 * (a 64×64 tile takes a few milliseconds) and cached.
 *
 * A blue-noise tile ranks every cell 0 … side²−1 so that the first k cells
 * are spread as evenly as the grid allows, for every k. As a threshold matrix
 * it has the same "denser tone is a superset of a sparser one" property as
 * Bayer, with no visible crosshatch.
 *
 * Deterministic: the same side always gives the same tile.
 */

/** Side of the tile uploaded to the GPU and used by images. */
export const BLUE_NOISE_SIDE = 64;
/** Side of the small tile used by SVG patterns and CSS masks (keeps the markup compact). */
export const BLUE_NOISE_PATTERN_SIDE = 16;

const SIGMA = 1.5;
const RADIUS = 5;
const KERNEL_SIDE = RADIUS * 2 + 1;

const kernel = (() => {
	const k = new Float32Array(KERNEL_SIDE * KERNEL_SIDE);
	for (let dy = -RADIUS; dy <= RADIUS; dy += 1) {
		for (let dx = -RADIUS; dx <= RADIUS; dx += 1) {
			k[(dy + RADIUS) * KERNEL_SIDE + dx + RADIUS] = Math.exp(
				-(dx * dx + dy * dy) / (2 * SIGMA * SIGMA),
			);
		}
	}
	return k;
})();

/** Small seeded PRNG (mulberry32) so tiles are reproducible. */
function prng(seed: number): () => number {
	let a = seed >>> 0;
	return () => {
		a = (a + 0x6d2b79f5) >>> 0;
		let t = a;
		t = Math.imul(t ^ (t >>> 15), t | 1);
		t ^= t + Math.imul(t ^ (t >>> 7), t | 61);
		return ((t ^ (t >>> 14)) >>> 0) / 4294967296;
	};
}

class Grid {
	readonly side: number;
	readonly bits: Uint8Array;
	readonly energy: Float32Array;
	constructor(side: number) {
		this.side = side;
		this.bits = new Uint8Array(side * side);
		this.energy = new Float32Array(side * side);
	}

	/** Set or clear one cell and update the toroidal gaussian energy around it. */
	set(index: number, on: boolean): void {
		const { side, energy } = this;
		this.bits[index] = on ? 1 : 0;
		const sign = on ? 1 : -1;
		const x = index % side;
		const y = (index - x) / side;
		for (let dy = -RADIUS; dy <= RADIUS; dy += 1) {
			const row = (((y + dy) % side) + side) % side;
			for (let dx = -RADIUS; dx <= RADIUS; dx += 1) {
				const col = (((x + dx) % side) + side) % side;
				const k = kernel[(dy + RADIUS) * KERNEL_SIDE + dx + RADIUS] ?? 0;
				energy[row * side + col] = (energy[row * side + col] ?? 0) + sign * k;
			}
		}
	}

	/** The inked cell with the most inked neighbours. */
	tightestCluster(): number {
		let best = -1;
		let bestEnergy = Number.NEGATIVE_INFINITY;
		for (let i = 0; i < this.bits.length; i += 1) {
			const e = this.energy[i] ?? 0;
			if (this.bits[i] === 1 && e > bestEnergy) {
				bestEnergy = e;
				best = i;
			}
		}
		return best;
	}

	/** The empty cell with the fewest inked neighbours. */
	largestVoid(): number {
		let best = -1;
		let bestEnergy = Number.POSITIVE_INFINITY;
		for (let i = 0; i < this.bits.length; i += 1) {
			const e = this.energy[i] ?? 0;
			if (this.bits[i] === 0 && e < bestEnergy) {
				bestEnergy = e;
				best = i;
			}
		}
		return best;
	}
}

const cache = new Map<number, Uint16Array>();

/**
 * Rank of every cell of a `side × side` blue-noise tile, row-major, 0 =
 * inked first. `side` must be at least 8. Cached: the same array is returned
 * on every call, so do not mutate it.
 */
export function blueNoiseRanks(side: number): Uint16Array {
	const hit = cache.get(side);
	if (hit) return hit;
	if (!Number.isInteger(side) || side < 8) {
		throw new RangeError("blue-noise side must be an integer >= 8");
	}
	const cells = side * side;
	const random = prng(0x9e3779b9 ^ side);

	// Initial binary pattern: ~10% random cells, relaxed until the tightest
	// cluster and the largest void are the same cell.
	const grid = new Grid(side);
	const seeds = Math.max(1, Math.floor(cells * 0.1));
	for (let placed = 0; placed < seeds; ) {
		const index = Math.floor(random() * cells);
		if (grid.bits[index] === 0) {
			grid.set(index, true);
			placed += 1;
		}
	}
	for (let guard = 0; guard < cells * 4; guard += 1) {
		const cluster = grid.tightestCluster();
		grid.set(cluster, false);
		const hole = grid.largestVoid();
		grid.set(hole, true);
		if (hole === cluster) break;
	}
	const initial = Uint8Array.from(grid.bits);
	const ranks = new Uint16Array(cells);

	// Phase 1: peel the seed cells off, tightest cluster first. They get the
	// highest ranks among the seeds, so the seeds are the first `seeds` cells.
	for (let rank = seeds - 1; rank >= 0; rank -= 1) {
		const cluster = grid.tightestCluster();
		ranks[cluster] = rank;
		grid.set(cluster, false);
	}

	// Phases 2 and 3: restore the seeds, then keep filling the largest void.
	for (let i = 0; i < cells; i += 1) {
		if (initial[i] === 1) grid.set(i, true);
	}
	for (let rank = seeds; rank < cells; rank += 1) {
		const hole = grid.largestVoid();
		ranks[hole] = rank;
		grid.set(hole, true);
	}

	cache.set(side, ranks);
	return ranks;
}

/** The 64×64 tile as ranks 0 … 4095, row-major. */
export function blueNoiseTile(): Uint16Array {
	return blueNoiseRanks(BLUE_NOISE_SIDE);
}

/** The 64×64 tile as thresholds in [0, 1), `(rank + 0.5) / cells`. Cached. */
let thresholds: Float32Array | undefined;
export function blueNoiseThresholds(): Float32Array {
	if (!thresholds) {
		const ranks = blueNoiseTile();
		thresholds = Float32Array.from(
			ranks,
			(rank) => (rank + 0.5) / (BLUE_NOISE_SIDE * BLUE_NOISE_SIDE),
		);
	}
	return thresholds;
}

/** Threshold in [0, 1) at any integer cell, tiling the 64×64 tile in both directions. */
export function blueNoiseThreshold(x: number, y: number): number {
	const side = BLUE_NOISE_SIDE;
	const col = ((Math.floor(x) % side) + side) % side;
	const row = ((Math.floor(y) % side) + side) % side;
	return blueNoiseThresholds()[row * side + col] ?? 0;
}

/** Same as `blueNoiseThreshold` for the 8-bit texture the GPU path samples (0..255). */
export function blueNoiseBytes(): Uint8Array {
	const ranks = blueNoiseTile();
	const cells = BLUE_NOISE_SIDE * BLUE_NOISE_SIDE;
	return Uint8Array.from(ranks, (rank) =>
		Math.min(255, Math.floor(((rank + 0.5) / cells) * 256)),
	);
}
