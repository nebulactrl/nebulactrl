import { describe, expect, test } from "bun:test";
import { clamp, cssColor, INK, inkMix, PAPER, shaderBase } from "./common";
import { bool, MATRIX_OPTIONS, matrixOf, num, str } from "./registry";

describe("shaderBase", () => {
	test("falls back to bayer4, the caller's still moment and the engine's levels", () => {
		const base = shaderBase({}, { staticTime: 7 });
		expect(base.matrix).toBe("bayer4");
		expect(base.staticTime).toBe(7);
		expect(base.levels).toBeUndefined();
		expect(base["aria-hidden"]).toBe(true);
	});

	test("`n` picks a Bayer matrix and `matrix` wins over it", () => {
		expect(shaderBase({ n: 8 }, { staticTime: 0 }).matrix).toBe("bayer8");
		expect(
			shaderBase({ matrix: "blue-noise", n: 2 }, { staticTime: 0 }).matrix,
		).toBe("blue-noise");
	});

	test("the default matrix comes from the background", () => {
		expect(shaderBase({}, { staticTime: 0, matrix: "blue-noise" }).matrix).toBe(
			"blue-noise",
		);
	});

	test('a default of "full" levels becomes the tone count of the chosen matrix', () => {
		const full = { staticTime: 0, levels: "full" } as const;
		expect(shaderBase({}, full).levels).toBe(17);
		expect(shaderBase({ n: 2 }, full).levels).toBe(5);
		expect(shaderBase({ matrix: "blue-noise" }, full).levels).toBe(256);
	});

	test("a numeric default applies, an explicit `levels` beats any default", () => {
		expect(shaderBase({}, { staticTime: 0, levels: 9 }).levels).toBe(9);
		expect(shaderBase({ levels: 3 }, { staticTime: 0, levels: 9 }).levels).toBe(
			3,
		);
		expect(
			shaderBase({ levels: "stops" }, { staticTime: 0, levels: "full" }).levels,
		).toBe("stops");
	});

	test("`stillTime` overrides the background's still moment", () => {
		expect(shaderBase({ stillTime: 1.5 }, { staticTime: 7 }).staticTime).toBe(
			1.5,
		);
		expect(shaderBase({ stillTime: 0 }, { staticTime: 7 }).staticTime).toBe(0);
	});

	test("forwards the engine props and nothing else", () => {
		const fallback = "no gl";
		const style = { opacity: 0.4 };
		const base = shaderBase(
			{
				cell: 4,
				shape: "dot",
				fps: 10,
				animate: false,
				speed: 2,
				className: "x",
				style,
				fallback,
				palette: ["a", "b"],
				clear: [],
			},
			{ staticTime: 0 },
		);
		expect(base).toEqual({
			matrix: "bayer4",
			levels: undefined,
			staticTime: 0,
			cell: 4,
			shape: "dot",
			fps: 10,
			animate: false,
			speed: 2,
			className: "x",
			style,
			fallback,
			"aria-hidden": true,
		});
	});
});

describe("colour helpers", () => {
	test("tokens", () => {
		expect(PAPER).toBe("--dither-paper");
		expect(INK).toBe("--dither-ink");
	});

	test("cssColor resolves names and tokens to var() and leaves other CSS alone", () => {
		expect(cssColor("blue")).toBe("var(--dither-blue)");
		expect(cssColor("--chart-2")).toBe("var(--chart-2)");
		expect(cssColor("  --chart-2 ")).toBe("var(--chart-2)");
		expect(cssColor("#ff0000")).toBe("#ff0000");
		expect(cssColor("var(--x)")).toBe("var(--x)");
	});

	test("inkMix tints ink towards paper by a percentage of ink", () => {
		expect(inkMix(28)).toBe(
			"color-mix(in oklch, var(--dither-ink) 28%, var(--dither-paper))",
		);
		expect(inkMix(0)).toContain(" 0%,");
	});

	test("clamp holds a value inside its range", () => {
		expect(clamp(5, 0, 1)).toBe(1);
		expect(clamp(-5, 0, 1)).toBe(0);
		expect(clamp(0.4, 0, 1)).toBe(0.4);
		expect(clamp(0, 0, 1)).toBe(0);
	});
});

describe("registry knob readers", () => {
	const values = { n: 3, s: "x", b: true, zero: 0, off: false, empty: "" };

	test("num returns numbers only, including 0", () => {
		expect(num(values, "n", 9)).toBe(3);
		expect(num(values, "zero", 9)).toBe(0);
		expect(num(values, "s", 9)).toBe(9);
		expect(num(values, "missing", 9)).toBe(9);
	});

	test("str returns strings only, including the empty one", () => {
		expect(str<string>(values, "s", "d")).toBe("x");
		expect(str<string>(values, "empty", "d")).toBe("");
		expect(str(values, "n", "d")).toBe("d");
		expect(str(values, "missing", "d")).toBe("d");
	});

	test("bool returns booleans only, including false", () => {
		expect(bool(values, "b", false)).toBe(true);
		expect(bool(values, "off", true)).toBe(false);
		expect(bool(values, "n", true)).toBe(true);
		expect(bool(values, "missing", false)).toBe(false);
	});

	test("matrixOf maps the gallery's matrix knob, and \"default\" keeps the background's own", () => {
		expect(MATRIX_OPTIONS[0]).toBe("default");
		for (const m of ["bayer2", "bayer4", "bayer8", "blue-noise"] as const) {
			expect(matrixOf({ matrix: m })).toBe(m);
		}
		expect(matrixOf({ matrix: "default" })).toBeUndefined();
		expect(matrixOf({ matrix: "bayer16" })).toBeUndefined();
		expect(matrixOf({})).toBeUndefined();
		expect(matrixOf({ matrix: 4 })).toBeUndefined();
	});
});
