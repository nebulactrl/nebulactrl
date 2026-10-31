import { describe, expect, test } from "bun:test";
import { bayerMatrix } from "../../lib/bayer";
import { blueNoiseThreshold } from "../../lib/blue-noise";
import type { DitherPalette } from "../../lib/tokens";
import { cssTextColor, resolveTextRgba } from "./colors";
import { defaultStagger, resolveDissolveTiming } from "./dissolve";
import {
	DEFAULT_FILL_SPEC,
	defaultGhost,
	directionVector,
	type FillSpec,
	fillTone,
	paintTextGrid,
	rampColor,
	type TextRgba,
	thresholdAt,
} from "./fills";
import { edgeBandRows } from "./masks";
import { numberParts, rollSteps } from "./number-parts";
import {
	blockStep,
	leadingSlot,
	planScramble,
	resolveScrambleTiming,
	scrambleAt,
} from "./scramble";

const spec = (over: Partial<FillSpec> = {}): FillSpec => ({
	...DEFAULT_FILL_SPEC,
	...over,
});

describe("text colours", () => {
	test("names, tokens and CSS colours become CSS values", () => {
		expect(cssTextColor("blue")).toBe("var(--dither-blue)");
		expect(cssTextColor("ink")).toBe("var(--dither-ink)");
		expect(cssTextColor("paper")).toBe("var(--dither-paper)");
		expect(cssTextColor("accent")).toBe("var(--color-accent-brand)");
		expect(cssTextColor("--foreground")).toBe("var(--foreground)");
		expect(cssTextColor("#ff0000")).toBe("#ff0000");
		expect(cssTextColor("transparent")).toBe("transparent");
	});

	test("canvas colours come from the resolved palette", () => {
		const palette = {
			ink: "#102030",
			paper: "#000000",
			accent: "#ffffff",
			muted: "#808080",
			named: { blue: "#0000ff", green: "#00ff00" },
		} as unknown as DitherPalette;
		expect(resolveTextRgba("ink", palette)).toEqual([16, 32, 48, 255]);
		expect(resolveTextRgba("blue", palette)).toEqual([0, 0, 255, 255]);
		expect(resolveTextRgba("green", palette)).toEqual([0, 255, 0, 255]);
		expect(resolveTextRgba("#ff8000", palette)).toEqual([255, 128, 0, 255]);
		expect(resolveTextRgba("transparent", palette)[3]).toBe(0);
	});
});

describe("text fill directions and matrices", () => {
	test("the four sides are exact unit vectors and angles follow CSS", () => {
		expect(directionVector("down")).toEqual({ dx: 0, dy: 1 });
		expect(directionVector("up")).toEqual({ dx: 0, dy: -1 });
		expect(directionVector("right")).toEqual({ dx: 1, dy: 0 });
		expect(directionVector("left")).toEqual({ dx: -1, dy: 0 });
		const right = directionVector(90);
		expect(right.dx).toBeCloseTo(1, 9);
		expect(right.dy).toBeCloseTo(0, 9);
		const down = directionVector(180);
		expect(down.dx).toBeCloseTo(0, 9);
		expect(down.dy).toBeCloseTo(1, 9);
		const up = directionVector(0);
		expect(up.dy).toBeCloseTo(-1, 9);
	});

	test("an angle of 90 thins out like `right`, and a gradient is dense at its start", () => {
		const right = fillTone(spec({ direction: "right" }), 2, 5, 40, 20, 0);
		expect(fillTone(spec({ direction: 90 }), 2, 5, 40, 20, 0)).toBeCloseTo(
			right,
			6,
		);
		const s = spec({ direction: "right" });
		expect(fillTone(s, 1, 10, 40, 20, 0)).toBeGreaterThan(
			fillTone(s, 38, 10, 40, 20, 0),
		);
		const diagonal = spec({ direction: 135 });
		expect(fillTone(diagonal, 1, 1, 40, 20, 0)).toBeGreaterThan(
			fillTone(diagonal, 38, 18, 40, 20, 0),
		);
	});

	test("noise scale is a frequency: a higher scale changes faster across the box", () => {
		const variation = (scale: number) => {
			const s = spec({ fill: "noise", from: 1, to: 0, scale });
			let sum = 0;
			for (let x = 1; x < 60; x += 1)
				sum += Math.abs(
					fillTone(s, x, 6, 60, 12, 0) - fillTone(s, x - 1, 6, 60, 12, 0),
				);
			return sum;
		};
		expect(variation(8)).toBeGreaterThan(variation(1));
	});

	test("thresholds follow the chosen matrix", () => {
		const bayer = bayerMatrix(4);
		expect(thresholdAt(spec(), 0, 0)).toBe(bayer[0] as number);
		expect(thresholdAt(spec(), 5, 6)).toBe(
			bayer[(6 % 4) * 4 + (5 % 4)] as number,
		);
		expect(thresholdAt(spec(), -1, -1)).toBe(bayer[15] as number);
		const eight = spec({ matrix: "bayer8" });
		expect(thresholdAt(eight, 9, 1)).toBe(bayerMatrix(8)[1 * 8 + 1] as number);
		const blue = spec({ matrix: "blue-noise" });
		expect(thresholdAt(blue, 3, 7)).toBe(blueNoiseThreshold(3, 7));
	});

	test("blue noise inks exactly the tone's share of a tile", () => {
		const blue = spec({ matrix: "blue-noise" });
		let inked = 0;
		for (let y = 0; y < 64; y += 1)
			for (let x = 0; x < 64; x += 1)
				if (0.5 > thresholdAt(blue, x, y)) inked += 1;
		expect(inked).toBe(2048);
	});

	test("ghost defaults to 0.2 for textures and 0 otherwise", () => {
		expect(defaultGhost("dotted")).toBe(0.2);
		expect(defaultGhost("hatched")).toBe(0.2);
		expect(defaultGhost("gradient")).toBe(0);
		expect(defaultGhost("noise")).toBe(0);
		expect(defaultGhost("solid")).toBe(0);
	});
});

describe("text palette ramps", () => {
	const clear: TextRgba = [0, 0, 0, 0];
	const mid: TextRgba = [10, 20, 30, 255];
	const dark: TextRgba = [200, 100, 50, 255];

	test("gradient and noise pick the nearest colours through the matrix", () => {
		const full = spec({ from: 1, to: 1 });
		const none = spec({ from: 0, to: 0 });
		for (let x = 0; x < 8; x += 1) {
			expect(rampColor([clear, dark], full, x, 0, 8, 8, 0)).toEqual(dark);
			expect(rampColor([clear, dark], none, x, 0, 8, 8, 0)).toEqual(clear);
		}
		// A tone of exactly the middle of a 3-colour ramp is the middle colour.
		const half = spec({ from: 0.5, to: 0.5 });
		expect(rampColor([clear, mid, dark], half, 3, 3, 8, 8, 0)).toEqual(mid);
	});

	test("textures use the first colour in the gaps and the last on the strokes", () => {
		const dots = spec({ fill: "dotted" });
		expect(rampColor([clear, dark], dots, 0, 0, 8, 8, 0)).toEqual(dark);
		expect(rampColor([clear, dark], dots, 1, 0, 8, 8, 0)).toEqual(clear);
		expect(
			rampColor([clear, dark], spec({ fill: "solid" }), 1, 1, 8, 8, 0),
		).toEqual(dark);
	});

	test("a palette replaces ink and ghost when painting", () => {
		const glyph = new Uint8ClampedArray([255, 255, 0]);
		const out = paintTextGrid(
			glyph,
			3,
			1,
			spec({ fill: "solid" }),
			[1, 2, 3],
			0.5,
			0,
			undefined,
			[clear, dark],
		);
		expect(Array.from(out.slice(0, 4))).toEqual([200, 100, 50, 255]);
		expect(Array.from(out.slice(4, 8))).toEqual([200, 100, 50, 255]);
		expect(out[11]).toBe(0);
	});
});

describe("dissolve timing", () => {
	test("defaults are 600 ms, 28 ms a character and 90 ms a word or line", () => {
		expect(defaultStagger("char")).toBe(28);
		expect(defaultStagger("word")).toBe(90);
		expect(defaultStagger("line")).toBe(90);
		expect(resolveDissolveTiming({ by: "char" })).toEqual({
			duration: 600,
			stagger: 28,
			delay: 0,
		});
		expect(resolveDissolveTiming({ by: "word" }).stagger).toBe(90);
	});

	test("speed divides every timing; a non-positive speed counts as 1", () => {
		expect(resolveDissolveTiming({ by: "char", delay: 100, speed: 2 })).toEqual(
			{ duration: 300, stagger: 14, delay: 50 },
		);
		expect(
			resolveDissolveTiming({ by: "char", duration: 400, speed: 0 }).duration,
		).toBe(400);
	});
});

describe("scramble blocks, caret and timing", () => {
	test("block cells carry how filled they are, other cells carry none", () => {
		const blocks = planScramble("", "ab", {
			mode: "blocks",
			duration: 100,
			stagger: 0,
			delay: 0,
		});
		expect(scrambleAt(blocks, 0, 50).tone).toBeCloseTo(0.5, 6);
		expect(scrambleAt(blocks, 0, 99).tone).toBeGreaterThan(0.9);
		const glyphs = planScramble("", "ab", {
			mode: "glyphs",
			duration: 100,
			stagger: 0,
			delay: 0,
		});
		expect(scrambleAt(glyphs, 0, 50).tone).toBe(0);
		expect(scrambleAt(blocks, 0, 500).tone).toBe(0);
	});

	test("a block is never empty and reaches the 16th step at full tone", () => {
		expect(blockStep(0)).toBe(1);
		expect(blockStep(0.5)).toBe(8);
		expect(blockStep(1)).toBe(16);
		expect(blockStep(2)).toBe(16);
	});

	test("the leading slot is the first animated one still resolving", () => {
		const plan = planScramble("", "a b", {
			stagger: 10,
			duration: 100,
			delay: 0,
		});
		expect(leadingSlot(plan, 0)).toBe(0);
		expect(leadingSlot(plan, 99)).toBe(0);
		// Slot 1 is a space and never animates; slot 2 starts at 10 and lands at 110.
		expect(leadingSlot(plan, 105)).toBe(2);
		expect(leadingSlot(plan, 110)).toBe(-1);
	});

	test("timing defaults match the docs and speed scales every timing and the glyph rate", () => {
		expect(resolveScrambleTiming()).toEqual({
			duration: 520,
			stagger: 34,
			delay: 0,
			frameMs: 50,
		});
		expect(
			resolveScrambleTiming({
				duration: 400,
				stagger: 20,
				delay: 100,
				speed: 2,
				rate: 10,
			}),
		).toEqual({ duration: 200, stagger: 10, delay: 50, frameMs: 50 });
		expect(resolveScrambleTiming({ speed: -1, rate: 0 }).frameMs).toBe(50);
	});
});

describe("number rolls", () => {
	const digits = (n: number) => [...String(n)];
	const steps = (from: number, to: number, index: number) =>
		rollSteps(digits(from), digits(to), index);

	test("a digit takes the short way round in the direction of the value", () => {
		expect(steps(3, 7, 0)).toBe(4);
		expect(steps(7, 3, 0)).toBe(-4);
		// 19 → 21: the units digit goes 9 → 0 → 1, the tens digit 1 → 2.
		expect(steps(19, 21, 1)).toBe(2);
		expect(steps(19, 21, 0)).toBe(1);
		// 21 → 19 rolls back down.
		expect(steps(21, 19, 1)).toBe(-2);
	});

	test("a new leading digit rolls up from 0", () => {
		expect(steps(99, 100, 0)).toBe(1);
		expect(steps(99, 100, 1)).toBe(1);
		expect(steps(99, 100, 2)).toBe(1);
	});

	test("a place that moved by ten or more spins an extra turn", () => {
		// 100 → 250: tens place went 0 → 5, but its place value moved 25 − 10 = 15.
		expect(steps(100, 250, 1)).toBe(15);
		expect(steps(250, 100, 1)).toBe(-15);
		// Hundreds moved by one only.
		expect(steps(100, 250, 0)).toBe(1);
	});

	test("only ASCII digits animate", () => {
		const locale = "ar-EG-u-nu-arab";
		const parts = numberParts(1234, { locale });
		expect(parts.map((p) => p.value).join("")).toBe(
			new Intl.NumberFormat(locale).format(1234),
		);
		for (const part of parts)
			if (part.type === "digit") expect(part.value).toMatch(/^[0-9]$/);
		const latin = numberParts(1234, { locale: "en-US" });
		expect(latin.filter((p) => p.type === "digit").length).toBe(4);
	});
});

describe("roll edge fades", () => {
	test("the outer rows of a band are sparser than the inner rows", () => {
		const count = (row: boolean[]) => row.filter(Boolean).length;
		const top = edgeBandRows(4, 8, "top");
		expect(count(top[0] ?? [])).toBeLessThan(count(top[7] ?? []));
		const bottom = edgeBandRows(4, 8, "bottom");
		expect(count(bottom[0] ?? [])).toBeGreaterThan(count(bottom[7] ?? []));
		expect(top.length).toBe(8);
		expect(top.every((row) => row.length === 4)).toBe(true);
	});
});
