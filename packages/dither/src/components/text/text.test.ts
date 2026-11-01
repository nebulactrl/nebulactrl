import { describe, expect, test } from "bun:test";
import {
	arrivalRanks,
	DISSOLVE_LEVELS,
	levelSequence,
	splitUnits,
	totalDuration,
	unitLevel,
	unitStart,
} from "./dissolve";
import {
	DEFAULT_FILL_SPEC,
	type FillSpec,
	fillInk,
	fillTone,
	hash2,
	paintTextGrid,
	valueNoise,
} from "./fills";
import {
	changedRanks,
	formatNumber,
	numberParts,
	rollDirection,
} from "./number-parts";
import {
	frameRandom,
	mulberry32,
	planScramble,
	SCRAMBLE_BLOCKS,
	scrambleAt,
	scrambleFrame,
} from "./scramble";

describe("scramble sequence", () => {
	test("mulberry32 is deterministic per seed and differs across seeds", () => {
		const a = mulberry32(42);
		const b = mulberry32(42);
		const first = [a(), a(), a()];
		expect([b(), b(), b()]).toEqual(first);
		expect(mulberry32(43)()).not.toBe(first[0] as number);
		for (const value of first) {
			expect(value).toBeGreaterThanOrEqual(0);
			expect(value).toBeLessThan(1);
		}
	});

	test("frameRandom is stateless: same inputs, same output", () => {
		expect(frameRandom(7, 3, 11)).toBe(frameRandom(7, 3, 11));
		expect(frameRandom(7, 3, 11)).not.toBe(frameRandom(7, 4, 11));
		expect(frameRandom(7, 3, 11)).not.toBe(frameRandom(8, 3, 11));
	});

	test("the same seed gives the same frames; another seed gives different glyphs", () => {
		const a = planScramble("", "Every frame", { seed: 5 });
		const b = planScramble("", "Every frame", { seed: 5 });
		const c = planScramble("", "Every frame", { seed: 6 });
		const times = [0, 60, 150, 300, 480, 700, 900];
		const run = (plan: typeof a) =>
			times.map((t) => scrambleFrame(plan, t).join(""));
		expect(run(a)).toEqual(run(b));
		expect(run(a)).not.toEqual(run(c));
	});

	test("a fresh mount animates every visible character and keeps spaces", () => {
		const plan = planScramble("", "ab cd", {
			stagger: 10,
			duration: 100,
			delay: 0,
		});
		expect(plan.slots.map((s) => s.changes)).toEqual([
			true,
			true,
			false,
			true,
			true,
		]);
		// Staggered in reading order, skipping the space.
		expect(plan.slots.map((s) => s.start)).toEqual([0, 10, 0, 20, 30]);
		expect(plan.total).toBe(130);
	});

	test("final frame is the real text; frame 0 shows nothing for letters yet to start", () => {
		const plan = planScramble("", "Hello", { duration: 200, stagger: 50 });
		expect(scrambleFrame(plan, plan.total)).toEqual([..."Hello"]);
		expect(scrambleFrame(plan, plan.total + 5000).join("")).toBe("Hello");
		const start = scrambleFrame(plan, 0);
		// Slot 0 has started (block); slot 4 has not (old char is empty).
		expect(SCRAMBLE_BLOCKS).toContain(
			start[0] as (typeof SCRAMBLE_BLOCKS)[number],
		);
		expect(start[4]).toBe("");
	});

	test("only characters that differ scramble when the text changes", () => {
		const plan = planScramble("Build ready", "Build failed");
		const flags = plan.slots.map((s) => s.changes);
		expect(flags.slice(0, 6)).toEqual([
			false,
			false,
			false,
			false,
			false,
			false,
		]);
		expect(flags.slice(6).every(Boolean)).toBe(true);
		expect(plan.slots[6]?.from).toBe("r");
		const mid = scrambleAt(plan, 0, 10);
		expect(mid.kind).toBe("final");
	});

	test("old text stays until a slot starts; blocks thicken; glyphs flicker from the text's own letters", () => {
		const plan = planScramble("abcd", "wxyz", {
			stagger: 100,
			duration: 400,
			mode: "dither",
		});
		expect(scrambleAt(plan, 3, 50)).toMatchObject({ kind: "from", char: "d" });
		const levels = [10, 60, 120, 200].map((t) => scrambleAt(plan, 0, t));
		expect(levels[0]?.kind).toBe("block");
		const blockLevels = levels
			.filter((c) => c.kind === "block")
			.map((c) => c.level);
		expect([...blockLevels].sort()).toEqual(blockLevels);
		const flicker = scrambleAt(plan, 0, 350);
		expect(flicker.kind).toBe("glyph");
		expect("wxyzabcd").toContain(flicker.char);
	});

	test("blocks mode never flickers glyphs and glyphs mode never shows blocks", () => {
		const blocks = planScramble("", "abc", {
			mode: "blocks",
			duration: 300,
			stagger: 0,
		});
		const glyphs = planScramble("", "abc", {
			mode: "glyphs",
			duration: 300,
			stagger: 0,
		});
		for (let t = 0; t < 300; t += 25) {
			expect(scrambleAt(blocks, 0, t).kind).toBe("block");
			expect(scrambleAt(glyphs, 0, t).kind).toBe("glyph");
		}
	});

	test("a custom character set is used for the flicker", () => {
		const plan = planScramble("", "ab", {
			mode: "glyphs",
			characters: "01",
			duration: 300,
		});
		for (let t = 0; t < 300; t += 20)
			expect("01").toContain(scrambleAt(plan, 0, t).char);
	});
});

describe("dissolve scheduling", () => {
	test("splitUnits keeps spaces unanimated and counts the rest", () => {
		const chars = splitUnits("ab cd", "char");
		expect(chars.map((u) => u.text)).toEqual(["a", "b", " ", "c", "d"]);
		expect(chars.map((u) => u.index)).toEqual([0, 1, -1, 2, 3]);
		const words = splitUnits("hello  big world", "word");
		expect(words.filter((u) => !u.space).map((u) => u.text)).toEqual([
			"hello",
			"big",
			"world",
		]);
		const lines = splitUnits("one\ntwo", "line");
		expect(lines.filter((u) => !u.space).map((u) => u.text)).toEqual([
			"one",
			"two",
		]);
	});

	test("arrival ranks are permutations in the requested order", () => {
		expect(arrivalRanks(5, "start")).toEqual([0, 1, 2, 3, 4]);
		expect(arrivalRanks(5, "end")).toEqual([4, 3, 2, 1, 0]);
		// Centre first: unit 2 arrives first, the ends last.
		expect(arrivalRanks(5, "center")[2]).toBe(0);
		expect(arrivalRanks(5, "center")[0]).toBeGreaterThan(2);
		// Edges first: the ends arrive first, the centre last.
		expect(arrivalRanks(5, "edges")[2]).toBe(4);
		for (const direction of [
			"start",
			"end",
			"center",
			"edges",
			"random",
		] as const) {
			expect([...arrivalRanks(9, direction, 3)].sort((a, b) => a - b)).toEqual([
				0, 1, 2, 3, 4, 5, 6, 7, 8,
			]);
		}
	});

	test("random order is a fixed shuffle per seed", () => {
		expect(arrivalRanks(12, "random", 9)).toEqual(
			arrivalRanks(12, "random", 9),
		);
		expect(arrivalRanks(12, "random", 9)).not.toEqual(
			arrivalRanks(12, "random", 10),
		);
		expect(arrivalRanks(12, "random", 9)).not.toEqual(
			arrivalRanks(12, "start"),
		);
	});

	test("levels rise from 0 to the end level over the duration, staggered by rank", () => {
		const timing = { delay: 100, stagger: 50, duration: 400 };
		expect(unitStart(2, timing)).toBe(200);
		expect(totalDuration(3, timing)).toBe(600);
		expect(unitLevel(0, 0, timing)).toBe(0);
		expect(unitLevel(300, 0, timing)).toBe(8);
		expect(unitLevel(500, 0, timing)).toBe(DISSOLVE_LEVELS);
		expect(unitLevel(300, 2, timing)).toBe(4);
		expect(unitLevel(500, 0, timing, 10)).toBe(10);
	});

	test("level sequences are whole Bayer steps, reversible for dissolving out", () => {
		expect(levelSequence(4)).toEqual([0, 1, 2, 3, 4]);
		expect(levelSequence(3, true)).toEqual([3, 2, 1, 0]);
		expect(levelSequence(99).length).toBe(DISSOLVE_LEVELS + 1);
	});
});

describe("text fills", () => {
	const spec = (over: Partial<FillSpec>): FillSpec => ({
		...DEFAULT_FILL_SPEC,
		...over,
	});

	test("solid inks everything", () => {
		for (let i = 0; i < 20; i += 1)
			expect(fillInk(spec({ fill: "solid" }), i, i % 3, 20, 3, 0)).toBe(true);
	});

	test("gradient down is denser at the top than at the bottom", () => {
		const s = spec({ fill: "gradient", direction: "down" });
		const count = (y0: number, y1: number) => {
			let n = 0;
			for (let y = y0; y < y1; y += 1)
				for (let x = 0; x < 40; x += 1) if (fillInk(s, x, y, 40, 40, 0)) n += 1;
			return n;
		};
		expect(count(0, 8)).toBeGreaterThan(count(32, 40) * 1.5);
	});

	test("gradient direction flips the density", () => {
		const down = fillTone(spec({ direction: "down" }), 5, 0, 10, 10, 0);
		const up = fillTone(spec({ direction: "up" }), 5, 0, 10, 10, 0);
		expect(down).toBeGreaterThan(up);
	});

	test("noise is deterministic, in range and drifts with time", () => {
		const s = spec({ fill: "noise", from: 1, to: 0 });
		const a = fillTone(s, 7, 4, 30, 12, 0);
		expect(fillTone(s, 7, 4, 30, 12, 0)).toBe(a);
		expect(a).toBeGreaterThanOrEqual(0);
		expect(a).toBeLessThanOrEqual(1);
		expect(fillTone(s, 7, 4, 30, 12, 5)).not.toBe(a);
		expect(hash2(3, 4)).toBe(hash2(3, 4));
		expect(valueNoise(2, 2)).toBe(hash2(2, 2));
	});

	test("dotted and hatched are fixed textures with the expected density", () => {
		const dots = spec({ fill: "dotted" });
		const hatch = spec({ fill: "hatched" });
		let d = 0;
		let h = 0;
		for (let y = 0; y < 12; y += 1) {
			for (let x = 0; x < 12; x += 1) {
				if (fillInk(dots, x, y, 12, 12, 0)) d += 1;
				if (fillInk(hatch, x, y, 12, 12, 0)) h += 1;
			}
		}
		expect(d).toBe(36);
		expect(h).toBe(48);
	});

	test("paintTextGrid keeps glyph shape: ink, ghost, and transparent outside", () => {
		const glyph = new Uint8ClampedArray([255, 255, 0, 255]);
		const out = paintTextGrid(
			glyph,
			4,
			1,
			spec({ fill: "hatched" }),
			[10, 20, 30],
			0.25,
			0,
		);
		// Hatched at (x + y) % 3 === 0: x = 0 is ink, x = 1 is ghost, x = 2 is outside, x = 3 is ink.
		expect(Array.from(out.slice(0, 4))).toEqual([10, 20, 30, 255]);
		expect(out[7]).toBe(64);
		expect(out[11]).toBe(0);
		expect(out[15]).toBe(255);
	});
});

describe("number parts", () => {
	test("digits and literals are split and keyed from the right", () => {
		const parts = numberParts(1234.5, {
			locale: "en-US",
			format: { minimumFractionDigits: 1 },
		});
		expect(parts.map((p) => p.value).join("")).toBe("1,234.5");
		expect(parts.filter((p) => p.type === "digit").map((p) => p.value)).toEqual(
			["1", "2", "3", "4", "5"],
		);
		expect(parts.find((p) => p.value === "5")?.key).toBe("d0");
		expect(parts.find((p) => p.value === "1")?.key).toBe("d4");
		expect(
			parts.filter((p) => p.type === "literal").map((p) => p.value),
		).toEqual([",", "."]);
	});

	test("prefix and suffix are literals", () => {
		const parts = numberParts(42, {
			locale: "en-US",
			prefix: "~",
			suffix: " ms",
		});
		expect(parts[0]).toMatchObject({ type: "literal", value: "~" });
		expect(parts.at(-1)).toMatchObject({ type: "literal", value: " ms" });
		expect(
			formatNumber(42, { locale: "en-US", prefix: "~", suffix: " ms" }),
		).toBe("~42 ms");
	});

	test("currency symbols stay literal", () => {
		const parts = numberParts(1200, {
			locale: "en-US",
			format: { style: "currency", currency: "USD", maximumFractionDigits: 0 },
		});
		expect(parts[0]).toMatchObject({ type: "literal", value: "$" });
		expect(parts.filter((p) => p.type === "digit")).toHaveLength(4);
	});

	test("changedRanks numbers only the digits that changed, left to right", () => {
		expect(changedRanks(["1", "2", "3"], ["1", "2", "3"])).toEqual([
			-1, -1, -1,
		]);
		expect(changedRanks(["1", "2", "3"], ["1", "4", "9"])).toEqual([-1, 0, 1]);
		// 99 → 100: aligned from the right, a new leading digit counts as changed.
		expect(changedRanks(["9", "9"], ["1", "0", "0"])).toEqual([0, 1, 2]);
	});

	test("rollDirection follows the numeric change", () => {
		expect(rollDirection("3", "7")).toBe(1);
		expect(rollDirection("7", "3")).toBe(-1);
		expect(rollDirection("3", "3")).toBe(0);
		expect(rollDirection(undefined, "3")).toBe(0);
	});
});

describe("bayer masks", () => {
	test("a step and its complement partition the tile", async () => {
		const { complementMaskUrl, maskUrl } = await import("./masks");
		const decode = (url: string) =>
			decodeURIComponent(url.slice('url("data:image/svg+xml,'.length, -2));
		const cellsOf = (svg: string) => {
			if (svg.includes("<rect")) return 16;
			const d = /d='([^']*)'|d="([^"]*)"/.exec(svg);
			const path = d?.[1] ?? d?.[2] ?? "";
			// Each run is `M x y h w v1 h -w z`; count its width.
			return [...path.matchAll(/M(\d+) (\d+)h(\d+)/g)].reduce(
				(sum, m) => sum + Number(m[3]),
				0,
			);
		};
		for (const step of [0, 4, 8, 12, 16]) {
			const inked = cellsOf(decode(maskUrl(step, 2)));
			const rest = cellsOf(decode(complementMaskUrl(step, 2)));
			expect(inked + rest).toBe(16);
		}
	});
});
