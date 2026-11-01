import { describe, expect, test } from "bun:test";
import {
	DITHER_COLOR_NAMES,
	ditherColorToken,
	ditherColorVar,
	isDitherColorName,
} from "./colors";

describe("ditherColorVar", () => {
	test("maps every named colour onto its token", () => {
		for (const name of DITHER_COLOR_NAMES) {
			expect(ditherColorVar(name)).toBe(`var(--dither-${name})`);
		}
		expect(ditherColorVar("ink")).toBe("var(--dither-ink)");
		expect(ditherColorVar("paper")).toBe("var(--dither-paper)");
	});

	test("wraps custom property names and leaves real colours alone", () => {
		expect(ditherColorVar("--chart-1")).toBe("var(--chart-1)");
		expect(ditherColorVar(" blue ")).toBe("var(--dither-blue)");
		expect(ditherColorVar("#7c8cff")).toBe("#7c8cff");
		expect(ditherColorVar("oklch(0.72 0.15 272)")).toBe("oklch(0.72 0.15 272)");
		expect(ditherColorVar("var(--x)")).toBe("var(--x)");
		expect(ditherColorVar("transparent")).toBe("transparent");
	});
});

describe("names", () => {
	test("isDitherColorName", () => {
		expect(isDitherColorName("pink")).toBe(true);
		expect(isDitherColorName("ink")).toBe(true);
		expect(isDitherColorName("teal")).toBe(false);
	});
	test("ditherColorToken", () => {
		expect(ditherColorToken("red")).toBe("--dither-red");
		expect(ditherColorToken("#fff")).toBeNull();
	});
});
