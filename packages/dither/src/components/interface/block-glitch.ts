/**
 * The block digits' glitch: mostly still, then a short burst of faults, then
 * the clean digits again. Pure maths on cell grids; `block-code.tsx` draws it.
 *
 * A burst is 3 to 6 frames of 50 ms (150 to 300 ms). Every frame is stepped,
 * never eased: 1 to 3 horizontal slices of the faces shift sideways by 1 to 4
 * cells, a few short runs of cells drop out or repeat one row down, and on one
 * frame the extruded side separates from the faces by a cell. Only the ink the
 * digits already use is drawn, so no colour is added.
 */

/** A source of numbers in [0, 1); `Math.random` outside the tests. */
export type Rand = () => number;

/** Seconds one frame of a burst holds: the block's 20 fps. */
export const GLITCH_FRAME_SECONDS = 0.05;
/** Seconds of stillness between bursts: 3 to 6. */
export const GLITCH_GAP_MIN = 3;
export const GLITCH_GAP_MAX = 6;
/** Frames in a burst: 3 to 6, which is 150 to 300 ms. */
export const GLITCH_FRAMES_MIN = 3;
export const GLITCH_FRAMES_MAX = 6;
/** The most a slice shifts, in cells. */
export const GLITCH_SHIFT_MAX = 4;
/** Slices per frame, 1 to 3, and runs of dropped or repeated cells per frame, 2 to 4. */
export const GLITCH_SLICES_MAX = 3;
export const GLITCH_RUNS_MIN = 2;
export const GLITCH_RUNS_MAX = 4;
/** A run is 2 to 4 cells wide. */
export const GLITCH_RUN_MIN = 2;
export const GLITCH_RUN_MAX = 4;

/** Seconds from one burst's end to the next one's start. */
export function nextBurstGap(rand: Rand): number {
	return GLITCH_GAP_MIN + (GLITCH_GAP_MAX - GLITCH_GAP_MIN) * rand();
}

/** A band of rows that moves sideways: positive `shift` is right. */
export interface GlitchSlice {
	top: number;
	rows: number;
	shift: number;
}

/** A short run of cells that drops out, or repeats on the row below. */
export interface GlitchRun {
	x: number;
	y: number;
	length: number;
	kind: "drop" | "repeat";
}

export interface GlitchFrame {
	slices: GlitchSlice[];
	runs: GlitchRun[];
	/** The extruded side sits one cell further down and right than usual. */
	separate: boolean;
}

/** The block's grid: its size in cells and the rows its faces cover. */
export interface GlitchBounds {
	width: number;
	height: number;
	faceTop: number;
	faceBottom: number;
}

const int = (rand: Rand, lo: number, hi: number) =>
	lo + Math.min(hi - lo, Math.floor(rand() * (hi - lo + 1)));

/**
 * The most rows all slices of one frame may cover together: a third of the
 * digit's height at most, so the digits never lose more than that.
 */
export function maxSliceRows(bounds: GlitchBounds): number {
	const rows = bounds.faceBottom - bounds.faceTop + 1;
	return Math.max(1, Math.floor(rows / 3));
}

function planSlices(rand: Rand, bounds: GlitchBounds): GlitchSlice[] {
	const budget = maxSliceRows(bounds);
	const each = Math.max(1, Math.floor(budget / GLITCH_SLICES_MAX));
	const count = int(rand, 1, GLITCH_SLICES_MAX);
	const slices: GlitchSlice[] = [];
	let used = 0;
	for (let i = 0; i < count; i++) {
		const rows = int(rand, 1, each);
		if (used + rows > budget) break;
		const span = bounds.faceBottom - bounds.faceTop + 1 - rows;
		// A few tries for a place that does not overlap a slice already placed.
		for (let attempt = 0; attempt < 6; attempt++) {
			const top = bounds.faceTop + int(rand, 0, Math.max(0, span));
			const clash = slices.some(
				(s) => top < s.top + s.rows && s.top < top + rows,
			);
			if (clash) continue;
			const size = int(rand, 1, GLITCH_SHIFT_MAX);
			slices.push({ top, rows, shift: rand() < 0.5 ? -size : size });
			used += rows;
			break;
		}
	}
	return slices;
}

function planRuns(
	rand: Rand,
	bounds: GlitchBounds,
	inked: (x: number, y: number) => boolean,
): GlitchRun[] {
	const count = int(rand, GLITCH_RUNS_MIN, GLITCH_RUNS_MAX);
	const runs: GlitchRun[] = [];
	for (let i = 0; i < count; i++) {
		// Start on a cell that has ink, or the fault would be invisible.
		for (let attempt = 0; attempt < 16; attempt++) {
			const x = int(rand, 0, bounds.width - 1);
			const y = int(rand, bounds.faceTop, bounds.faceBottom);
			if (!inked(x, y)) continue;
			runs.push({
				x,
				y,
				length: int(rand, GLITCH_RUN_MIN, GLITCH_RUN_MAX),
				kind: rand() < 0.5 ? "drop" : "repeat",
			});
			break;
		}
	}
	return runs;
}

/**
 * One burst: its frames, in order. The side separates on the middle frame
 * only. After the last frame the block is clean again.
 */
export function planBurst(
	rand: Rand,
	bounds: GlitchBounds,
	inked: (x: number, y: number) => boolean,
): GlitchFrame[] {
	const frames = int(rand, GLITCH_FRAMES_MIN, GLITCH_FRAMES_MAX);
	const apart = Math.floor(frames / 2);
	return Array.from({ length: frames }, (_, index) => ({
		slices: planSlices(rand, bounds),
		runs: planRuns(rand, bounds, inked),
		separate: index === apart,
	}));
}

/** Where the glitch is in time: the next burst, and the one under way. */
export interface GlitchClock {
	nextAt: number;
	burst: { at: number; frames: GlitchFrame[] } | null;
}

/** A clock whose first burst comes a gap after `start`. */
export function glitchClock(start: number, rand: Rand): GlitchClock {
	return { nextAt: start + nextBurstGap(rand), burst: null };
}

/**
 * The frame to draw at `now` (seconds on the clock), or `null` for the clean
 * digits. Starts a burst when its time comes, and plans the next one once it
 * has ended. Calling it again with the same time gives the same frame.
 */
export function glitchFrameAt(
	clock: GlitchClock,
	now: number,
	plan: () => GlitchFrame[],
	rand: Rand,
): GlitchFrame | null {
	if (clock.burst === null && now >= clock.nextAt) {
		clock.burst = { at: now, frames: plan() };
	}
	const burst = clock.burst;
	if (burst === null) return null;
	const index = Math.floor((now - burst.at) / GLITCH_FRAME_SECONDS);
	const frame = burst.frames[index];
	if (frame !== undefined && index >= 0) return frame;
	clock.burst = null;
	clock.nextAt =
		burst.at + burst.frames.length * GLITCH_FRAME_SECONDS + nextBurstGap(rand);
	return null;
}

/**
 * Apply a frame's slices and runs to RGBA pixels, one pixel per cell, in
 * place. The separated side is not here: it needs the letters' layers, so
 * `block-code.tsx` reads it off `frame.separate` while it draws.
 */
export function applyGlitch(
	rgba: Uint8ClampedArray | Uint8Array,
	width: number,
	height: number,
	frame: GlitchFrame,
): void {
	const row = new Uint8Array(width * 4);
	for (const slice of frame.slices) {
		for (let y = slice.top; y < slice.top + slice.rows && y < height; y++) {
			const base = y * width * 4;
			row.set(rgba.subarray(base, base + width * 4));
			rgba.fill(0, base, base + width * 4);
			for (let x = 0; x < width; x++) {
				const from = x - slice.shift;
				if (from < 0 || from >= width) continue;
				rgba.set(row.subarray(from * 4, from * 4 + 4), base + x * 4);
			}
		}
	}
	for (const run of frame.runs) {
		const end = Math.min(width, run.x + run.length);
		for (let x = run.x; x < end; x++) {
			const at = (run.y * width + x) * 4;
			if (run.kind === "drop") {
				rgba.fill(0, at, at + 4);
			} else if (run.y + 1 < height) {
				const below = at + width * 4;
				rgba.copyWithin(below, at, at + 4);
			}
		}
	}
}

/**
 * Index into the side layer for the cell at (x, y) when the side has
 * separated: one cell further down and right, so it reads the cell up and left
 * of this one. -1 when that is off the grid.
 */
export function separatedSideIndex(
	x: number,
	y: number,
	width: number,
	height: number,
	separate: boolean,
): number {
	if (!separate) return y * width + x;
	const sx = x - 1;
	const sy = y - 1;
	return sx < 0 || sy < 0 || sx >= width || sy >= height ? -1 : sy * width + sx;
}
