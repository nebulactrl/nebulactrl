import { afterEach, describe, expect, spyOn, test } from "bun:test";
import { DITHER_COLOR_FALLBACKS, DITHER_COLOR_NAMES } from "../../lib/colors";
import { type DitherPalette, readDitherPalette } from "../../lib/tokens";
import { cssTextColor, resolveTextRgba } from "./colors";

const palette: DitherPalette = {
	...readDitherPalette(undefined),
	ink: "#102030",
	paper: "#f0e0d0",
	accent: "#aa0000",
	muted: "#808080",
	named: {
		blue: "#0000ff",
		purple: "#800080",
		green: "#00ff00",
		pink: "#ffc0cb",
		orange: "#ffa500",
		red: "#ff0000",
		grey: "#888888",
	},
};

describe("cssTextColor", () => {
	test("base keywords map to the theme tokens", () => {
		expect(cssTextColor("ink")).toBe("var(--dither-ink)");
		expect(cssTextColor("paper")).toBe("var(--dither-paper)");
		expect(cssTextColor("accent")).toBe("var(--color-accent-brand)");
		expect(cssTextColor("muted")).toBe("var(--color-ink-4)");
	});

	test("palette names become their dither variable", () => {
		for (const name of DITHER_COLOR_NAMES) {
			expect(cssTextColor(name)).toBe(`var(--dither-${name})`);
		}
	});

	test("custom properties are wrapped in var(); other CSS passes through", () => {
		expect(cssTextColor("--foreground")).toBe("var(--foreground)");
		expect(cssTextColor("#123456")).toBe("#123456");
		expect(cssTextColor("oklch(0.7 0.1 200)")).toBe("oklch(0.7 0.1 200)");
		expect(cssTextColor("var(--x)")).toBe("var(--x)");
	});

	test("surrounding whitespace is ignored", () => {
		expect(cssTextColor("  ink ")).toBe("var(--dither-ink)");
	});
});

describe("resolveTextRgba", () => {
	test("base tokens read the resolved palette, fully opaque", () => {
		expect(resolveTextRgba("ink", palette)).toEqual([0x10, 0x20, 0x30, 255]);
		expect(resolveTextRgba("paper", palette)).toEqual([0xf0, 0xe0, 0xd0, 255]);
		expect(resolveTextRgba("accent", palette)).toEqual([0xaa, 0, 0, 255]);
		expect(resolveTextRgba("muted", palette)).toEqual([128, 128, 128, 255]);
	});

	test("palette names read their named colour", () => {
		expect(resolveTextRgba("blue", palette)).toEqual([0, 0, 255, 255]);
		expect(resolveTextRgba(" orange ", palette)).toEqual([255, 165, 0, 255]);
		expect(resolveTextRgba("green", palette)).toEqual([0, 255, 0, 255]);
		expect(resolveTextRgba("grey", palette)).toEqual([0x88, 0x88, 0x88, 255]);
	});

	test("transparent has alpha 0", () => {
		expect(resolveTextRgba("transparent", palette)).toEqual([0, 0, 0, 0]);
	});

	test("any colour with alpha 0 has alpha 0, and alpha is kept otherwise", () => {
		expect(resolveTextRgba("#00000000", palette)[3]).toBe(0);
		expect(resolveTextRgba("rgba(1, 2, 3, 0)", palette)[3]).toBe(0);
		expect(resolveTextRgba("rgb(1 2 3 / 0%)", palette)[3]).toBe(0);
		expect(resolveTextRgba("#ff000080", palette)[3]).toBe(128);
		expect(resolveTextRgba("rgb(1 2 3 / 50%)", palette)[3]).toBe(128);
	});

	test("a hex colour parses directly, three digits too", () => {
		expect(resolveTextRgba("#336699", palette)).toEqual([
			0x33, 0x66, 0x99, 255,
		]);
		expect(resolveTextRgba("#fa0", palette)).toEqual([255, 170, 0, 255]);
	});

	describe("custom properties", () => {
		let spy: { mockRestore: () => void } | undefined;
		afterEach(() => spy?.mockRestore());

		const computedAs = (color: string) => {
			const real = window.getComputedStyle.bind(window);
			let probed = "";
			spy = spyOn(window, "getComputedStyle").mockImplementation(((
				el: Element,
			) => {
				if ((el as HTMLElement).style?.display === "none") {
					probed = (el as HTMLElement).style.color;
					return { color } as CSSStyleDeclaration;
				}
				return real(el);
			}) as typeof window.getComputedStyle);
			return () => probed;
		};

		test("a --token is probed as var(--token) on a hidden element and read back as the computed colour", () => {
			const probed = computedAs("#445566");
			expect(resolveTextRgba("--brand", palette)).toEqual([
				0x44, 0x55, 0x66, 255,
			]);
			expect(probed()).toBe("var(--brand)");
			expect(document.querySelector("span[style*='display: none']")).toBeNull();
		});

		test("a var() expression is probed as given", () => {
			const probed = computedAs("#010203");
			expect(resolveTextRgba("var(--x, red)", palette)).toEqual([1, 2, 3, 255]);
			expect(probed()).toBe("var(--x, red)");
		});
	});

	test("unknown input reads as mid grey with full alpha", () => {
		expect(resolveTextRgba("definitely-not-a-colour", palette)).toEqual([
			128, 128, 128, 255,
		]);
	});
});

test("fallback palette names are all covered by the dither colour list", () => {
	for (const name of DITHER_COLOR_NAMES) {
		expect(DITHER_COLOR_FALLBACKS[name]).toMatch(/^#[0-9a-f]{6}$/);
	}
});
