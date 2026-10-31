import { describe, expect, test } from "bun:test";
import {
	applyGlitch,
	GLITCH_FRAME_SECONDS,
	GLITCH_FRAMES_MAX,
	GLITCH_FRAMES_MIN,
	GLITCH_GAP_MAX,
	GLITCH_GAP_MIN,
	GLITCH_RUN_MAX,
	GLITCH_RUN_MIN,
	GLITCH_RUNS_MAX,
	GLITCH_SHIFT_MAX,
	GLITCH_SLICES_MAX,
	type GlitchBounds,
	type GlitchFrame,
	glitchClock,
	glitchFrameAt,
	maxSliceRows,
	nextBurstGap,
	planBurst,
	separatedSideIndex,
} from "./block-glitch";

/** A deterministic generator (mulberry32), so a failing seed can be replayed. */
function seeded(seed: number) {
	let a = seed;
	return () => {
		a = (a + 0x6d2b79f5) | 0;
		let t = Math.imul(a ^ (a >>> 15), 1 | a);
		t = (t + Math.imul(t ^ (t >>> 7), 61 | t)) ^ t;
		return ((t ^ (t >>> 14)) >>> 0) / 4294967296;
	};
}

/** The grid of a "404" at 4 px cells in a 400 px block: about 95 wide and 40 tall. */
const bounds: GlitchBounds = {
	width: 100,
	height: 44,
	faceTop: 2,
	faceBottom: 38,
};
const inkAnywhere = () => true;
const SEEDS = Array.from({ length: 200 }, (_, i) => i + 1);

describe("nextBurstGap", () => {
	test("is 3 to 6 seconds at both ends of the generator", () => {
		expect(nextBurstGap(() => 0)).toBe(GLITCH_GAP_MIN);
		expect(nextBurstGap(() => 0.999999)).toBeLessThan(GLITCH_GAP_MAX);
		expect(nextBurstGap(() => 0.999999)).toBeGreaterThan(5.99);
	});
});

describe("planBurst", () => {
	test("a burst is 3 to 6 stepped frames, 150 to 300 ms", () => {
		const seen = new Set<number>();
		for (const seed of SEEDS) {
			const frames = planBurst(seeded(seed), bounds, inkAnywhere);
			expect(frames.length).toBeGreaterThanOrEqual(GLITCH_FRAMES_MIN);
			expect(frames.length).toBeLessThanOrEqual(GLITCH_FRAMES_MAX);
			const ms = frames.length * GLITCH_FRAME_SECONDS * 1000;
			expect(ms).toBeGreaterThanOrEqual(150 - 1e-9);
			expect(ms).toBeLessThanOrEqual(300 + 1e-9);
			seen.add(frames.length);
		}
		expect([...seen].sort()).toEqual([3, 4, 5, 6]);
	});

	test("every frame has 1 to 3 slices that shift 1 to 4 cells and stay on the faces", () => {
		for (const seed of SEEDS) {
			for (const frame of planBurst(seeded(seed), bounds, inkAnywhere)) {
				expect(frame.slices.length).toBeGreaterThanOrEqual(1);
				expect(frame.slices.length).toBeLessThanOrEqual(GLITCH_SLICES_MAX);
				for (const slice of frame.slices) {
					expect(Math.abs(slice.shift)).toBeGreaterThanOrEqual(1);
					expect(Math.abs(slice.shift)).toBeLessThanOrEqual(GLITCH_SHIFT_MAX);
					expect(slice.rows).toBeGreaterThanOrEqual(1);
					expect(slice.top).toBeGreaterThanOrEqual(bounds.faceTop);
					expect(slice.top + slice.rows - 1).toBeLessThanOrEqual(
						bounds.faceBottom,
					);
				}
			}
		}
	});

	test("slices never overlap and together cover at most a third of the digit", () => {
		for (const seed of SEEDS) {
			for (const frame of planBurst(seeded(seed), bounds, inkAnywhere)) {
				const rows = frame.slices.reduce((n, s) => n + s.rows, 0);
				expect(rows).toBeLessThanOrEqual(maxSliceRows(bounds));
				const faceRows = bounds.faceBottom - bounds.faceTop + 1;
				expect(rows).toBeLessThanOrEqual(Math.ceil(faceRows / 3));
				const sorted = [...frame.slices].sort((a, b) => a.top - b.top);
				for (let i = 1; i < sorted.length; i++) {
					const before = sorted[i - 1];
					const at = sorted[i];
					expect(at && before && at.top >= before.top + before.rows).toBe(true);
				}
			}
		}
	});

	test("a few runs drop or repeat cells, 2 to 4 wide, on ink, inside the grid", () => {
		const kinds = new Set<string>();
		const inked = (x: number, y: number) => (x + y) % 3 === 0;
		for (const seed of SEEDS) {
			for (const frame of planBurst(seeded(seed), bounds, inked)) {
				expect(frame.runs.length).toBeLessThanOrEqual(GLITCH_RUNS_MAX);
				let cells = 0;
				for (const run of frame.runs) {
					kinds.add(run.kind);
					cells += run.length;
					expect(run.length).toBeGreaterThanOrEqual(GLITCH_RUN_MIN);
					expect(run.length).toBeLessThanOrEqual(GLITCH_RUN_MAX);
					expect(inked(run.x, run.y)).toBe(true);
					expect(run.x).toBeGreaterThanOrEqual(0);
					expect(run.x).toBeLessThan(bounds.width);
					expect(run.y).toBeGreaterThanOrEqual(bounds.faceTop);
					expect(run.y).toBeLessThanOrEqual(bounds.faceBottom);
				}
				// 16 cells of a ~400-cell digit: never more than 4%.
				expect(cells).toBeLessThanOrEqual(GLITCH_RUNS_MAX * GLITCH_RUN_MAX);
			}
		}
		expect([...kinds].sort()).toEqual(["drop", "repeat"]);
	});

	test("a grid with no ink gets no runs and does not loop", () => {
		const frames = planBurst(seeded(7), bounds, () => false);
		for (const frame of frames) expect(frame.runs).toEqual([]);
	});

	test("the side separates on exactly one frame, never the first or the last of a long burst", () => {
		for (const seed of SEEDS) {
			const frames = planBurst(seeded(seed), bounds, inkAnywhere);
			const apart = frames.filter((f) => f.separate);
			expect(apart).toHaveLength(1);
			expect(frames[0]?.separate).toBe(false);
		}
	});
});

describe("glitchFrameAt: the schedule", () => {
	const one: GlitchFrame = { slices: [], runs: [], separate: false };
	const plan = (n: number) => () =>
		Array.from({ length: n }, (_, i) => ({ ...one, separate: i === 1 }));

	test("clean until the first burst, then the frames in order, then clean again", () => {
		const rand = seeded(3);
		const clock = glitchClock(1.1, rand);
		expect(clock.nextAt).toBeGreaterThanOrEqual(1.1 + GLITCH_GAP_MIN);
		expect(clock.nextAt).toBeLessThan(1.1 + GLITCH_GAP_MAX);
		const start = clock.nextAt;
		expect(glitchFrameAt(clock, 1.1, plan(4), rand)).toBeNull();
		expect(glitchFrameAt(clock, start - 0.01, plan(4), rand)).toBeNull();
		expect(glitchFrameAt(clock, start, plan(4), rand)).not.toBeNull();
		const seen: boolean[] = [];
		for (let i = 0; i < 4; i++) {
			const frame = glitchFrameAt(
				clock,
				start + (i + 0.5) * GLITCH_FRAME_SECONDS,
				plan(4),
				rand,
			);
			expect(frame).not.toBeNull();
			seen.push(frame?.separate ?? false);
		}
		expect(seen).toEqual([false, true, false, false]);
		// the burst is over: the digits settle clean
		const after = start + 4.5 * GLITCH_FRAME_SECONDS;
		expect(glitchFrameAt(clock, after, plan(4), rand)).toBeNull();
		expect(clock.burst).toBeNull();
	});

	test("the next burst comes 3 to 6 seconds after this one ends", () => {
		for (const seed of SEEDS) {
			const rand = seeded(seed);
			const clock = glitchClock(0, rand);
			const start = clock.nextAt;
			const end = start + 4 * GLITCH_FRAME_SECONDS;
			glitchFrameAt(clock, start, plan(4), rand);
			glitchFrameAt(clock, end + 0.001, plan(4), rand);
			expect(clock.nextAt - end).toBeGreaterThanOrEqual(GLITCH_GAP_MIN - 1e-9);
			expect(clock.nextAt - end).toBeLessThan(GLITCH_GAP_MAX);
		}
	});

	test("asking again for the same moment gives the same frame, so a repaint does not replan", () => {
		const rand = seeded(5);
		const clock = glitchClock(0, rand);
		const at = clock.nextAt + 0.06;
		let planned = 0;
		const count = () => {
			planned += 1;
			return plan(5)();
		};
		const first = glitchFrameAt(clock, clock.nextAt, count, rand);
		const a = glitchFrameAt(clock, at, count, rand);
		const b = glitchFrameAt(clock, at, count, rand);
		expect(a).toBe(b);
		expect(first).not.toBe(a);
		expect(planned).toBe(1);
	});

	test("a long stall (a hidden tab) ends in clean digits, not a stuck burst", () => {
		const rand = seeded(9);
		const clock = glitchClock(0, rand);
		glitchFrameAt(clock, clock.nextAt, plan(5), rand);
		expect(glitchFrameAt(clock, clock.nextAt + 120, plan(5), rand)).toBeNull();
	});
});

describe("applyGlitch", () => {
	/** A grid whose cell (x, y) is ink with alpha 255 and red = x, or empty. */
	const grid = (width: number, height: number) => {
		const px = new Uint8Array(width * height * 4);
		for (let y = 0; y < height; y++) {
			for (let x = 0; x < width; x++) {
				const at = (y * width + x) * 4;
				px[at] = x + 1;
				px[at + 3] = 255;
			}
		}
		return px;
	};
	const alphaRow = (px: Uint8Array, width: number, y: number) =>
		Array.from({ length: width }, (_, x) => px[(y * width + x) * 4 + 3]);
	const redRow = (px: Uint8Array, width: number, y: number) =>
		Array.from({ length: width }, (_, x) => px[(y * width + x) * 4]);

	test("a slice moves its rows sideways and leaves the others alone", () => {
		const px = grid(8, 4);
		applyGlitch(px, 8, 4, {
			slices: [{ top: 1, rows: 1, shift: 3 }],
			runs: [],
			separate: false,
		});
		expect(redRow(px, 8, 1)).toEqual([0, 0, 0, 1, 2, 3, 4, 5]);
		expect(alphaRow(px, 8, 1)).toEqual([0, 0, 0, 255, 255, 255, 255, 255]);
		expect(redRow(px, 8, 0)).toEqual([1, 2, 3, 4, 5, 6, 7, 8]);
		expect(redRow(px, 8, 2)).toEqual([1, 2, 3, 4, 5, 6, 7, 8]);
	});

	test("a negative shift moves left and clips at the edge", () => {
		const px = grid(8, 2);
		applyGlitch(px, 8, 2, {
			slices: [{ top: 0, rows: 2, shift: -2 }],
			runs: [],
			separate: false,
		});
		expect(redRow(px, 8, 0)).toEqual([3, 4, 5, 6, 7, 8, 0, 0]);
	});

	test("a drop clears its cells and a repeat copies them one row down", () => {
		const px = grid(8, 3);
		applyGlitch(px, 8, 3, {
			slices: [],
			runs: [
				{ x: 1, y: 0, length: 2, kind: "drop" },
				{ x: 4, y: 1, length: 3, kind: "repeat" },
			],
			separate: false,
		});
		expect(alphaRow(px, 8, 0)).toEqual([255, 0, 0, 255, 255, 255, 255, 255]);
		// repeated cells land on row 2 with the red of the row above (same x here)
		expect(redRow(px, 8, 2)).toEqual([1, 2, 3, 4, 5, 6, 7, 8]);
		const hole = new Uint8Array(8 * 3 * 4);
		hole[(1 * 8 + 5) * 4] = 99;
		hole[(1 * 8 + 5) * 4 + 3] = 255;
		applyGlitch(hole, 8, 3, {
			slices: [],
			runs: [{ x: 4, y: 1, length: 3, kind: "repeat" }],
			separate: false,
		});
		expect(hole[(2 * 8 + 5) * 4]).toBe(99);
		expect(hole[(2 * 8 + 5) * 4 + 3]).toBe(255);
	});

	test("an empty frame changes nothing, and a repeat on the last row is ignored", () => {
		const px = grid(6, 2);
		const before = px.slice();
		applyGlitch(px, 6, 2, { slices: [], runs: [], separate: false });
		expect(px).toEqual(before);
		applyGlitch(px, 6, 2, {
			slices: [],
			runs: [{ x: 0, y: 1, length: 4, kind: "repeat" }],
			separate: false,
		});
		expect(px).toEqual(before);
	});

	test("a planned frame never clears more than a third of a digit's cells", () => {
		const width = bounds.width;
		const height = bounds.height;
		for (const seed of SEEDS) {
			const px = new Uint8Array(width * height * 4);
			let ink = 0;
			// a solid block over the faces, as a worst case
			for (let y = bounds.faceTop; y <= bounds.faceBottom; y++) {
				for (let x = 0; x < width; x++) {
					px[(y * width + x) * 4 + 3] = 255;
					ink += 1;
				}
			}
			const inked = (x: number, y: number) =>
				px[(y * width + x) * 4 + 3] === 255;
			for (const frame of planBurst(seeded(seed), bounds, inked)) {
				const copy = px.slice();
				applyGlitch(copy, width, height, frame);
				let left = 0;
				for (let i = 3; i < copy.length; i += 4) if (copy[i] === 255) left += 1;
				// slices shift cells (a sliver clips at the edge) and runs drop a few
				expect(ink - left).toBeLessThanOrEqual(ink / 3);
			}
		}
	});
});

describe("separatedSideIndex", () => {
	test("is the cell itself when together", () => {
		expect(separatedSideIndex(3, 2, 10, 8, false)).toBe(2 * 10 + 3);
	});

	test("reads the cell up and left when apart, so the side sits one cell down and right", () => {
		expect(separatedSideIndex(3, 2, 10, 8, true)).toBe(1 * 10 + 2);
	});

	test("is -1 off the grid", () => {
		expect(separatedSideIndex(0, 4, 10, 8, true)).toBe(-1);
		expect(separatedSideIndex(4, 0, 10, 8, true)).toBe(-1);
	});
});
