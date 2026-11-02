import { afterEach, describe, expect, spyOn, test } from "bun:test";
import { cleanup, renderHook, waitFor } from "@testing-library/react";
import { DITHER_COLOR_FALLBACKS, DITHER_COLOR_NAMES } from "./colors";

// tokens.ts caches parsed colours and the 1x1 probe canvas at module level and
// bun shares modules across test files, so each test imports a private copy.
let copy = 0;
async function freshTokens(): Promise<typeof import("./tokens")> {
	copy += 1;
	return import(`./tokens?copy=${copy}`);
}

/**
 * A stand-in 2d context that "paints" by remembering the last valid fillStyle
 * and reads it back through `colors`. Unknown styles are ignored like a real
 * canvas, which keeps the previous value.
 */
function stubCanvas(colors: Record<string, [number, number, number]>) {
	const state = { fillStyle: "#000000", filled: "#000000", cleared: 0 };
	const ctx = {
		canvas: { width: 0, height: 0 },
		clearRect() {
			state.cleared += 1;
		},
		fillRect() {
			state.filled = state.fillStyle;
		},
		getImageData() {
			const [r, g, b] = colors[state.filled] ?? [0, 0, 0];
			return { data: new Uint8ClampedArray([r, g, b, 255]) };
		},
		set fillStyle(value: string) {
			if (value === "#808080" || value in colors) state.fillStyle = value;
		},
		get fillStyle() {
			return state.fillStyle;
		},
	};
	const getContext = spyOn(
		HTMLCanvasElement.prototype,
		"getContext",
	).mockImplementation((() => ctx) as never);
	return { ctx, state, getContext };
}

afterEach(() => {
	// Unmount first: resetting <html> while a palette hook is mounted would
	// wake its observer outside act().
	cleanup();
	const root = document.documentElement;
	root.removeAttribute("style");
	root.removeAttribute("class");
	root.removeAttribute("data-theme");
});

describe("toRGB", () => {
	test("parses 6 and 3 digit hex, any case, with surrounding space", async () => {
		const { toRGB } = await freshTokens();
		expect(toRGB("#ff8000")).toEqual([255, 128, 0]);
		expect(toRGB("#F80")).toEqual([255, 136, 0]);
		expect(toRGB("  #0a0B0c ")).toEqual([10, 11, 12]);
	});

	test("reads non-hex colours back from a 1x1 canvas", async () => {
		const canvas = stubCanvas({ "oklch(0.7 0.1 200)": [10, 200, 90] });
		try {
			const { toRGB } = await freshTokens();
			expect(toRGB("oklch(0.7 0.1 200)")).toEqual([10, 200, 90]);
			expect(canvas.ctx.canvas).toMatchObject({ width: 1, height: 1 });
		} finally {
			canvas.getContext.mockRestore();
		}
	});

	test("an invalid colour reads as mid grey, not the previous colour", async () => {
		const canvas = stubCanvas({
			"oklch(0.7 0.1 200)": [10, 200, 90],
			"#808080": [128, 128, 128],
		});
		try {
			const { toRGB } = await freshTokens();
			expect(toRGB("oklch(0.7 0.1 200)")).toEqual([10, 200, 90]);
			expect(toRGB("not a colour")).toEqual([128, 128, 128]);
		} finally {
			canvas.getContext.mockRestore();
		}
	});

	test("caches per input string and paints each colour once", async () => {
		const canvas = stubCanvas({ "oklch(0.7 0.1 200)": [10, 200, 90] });
		try {
			const { toRGB } = await freshTokens();
			const first = toRGB("oklch(0.7 0.1 200)");
			const second = toRGB("oklch(0.7 0.1 200)");
			expect(second).toBe(first);
			expect(canvas.state.cleared).toBe(1);
			// The probe context itself is created once, not per colour.
			toRGB("oklch(1 1 1)");
			expect(canvas.getContext).toHaveBeenCalledTimes(1);
		} finally {
			canvas.getContext.mockRestore();
		}
	});

	test("falls back to mid grey when there is no 2d context", async () => {
		const getContext = spyOn(
			HTMLCanvasElement.prototype,
			"getContext",
		).mockImplementation((() => null) as never);
		try {
			const { toRGB } = await freshTokens();
			expect(toRGB("oklch(0.7 0.1 200)")).toEqual([128, 128, 128]);
		} finally {
			getContext.mockRestore();
		}
	});
});

describe("readDitherPalette", () => {
	test("uses the last-resort defaults when the page defines no tokens", async () => {
		const { readDitherPalette } = await freshTokens();
		const palette = readDitherPalette(document.documentElement);
		expect(palette.ink).toBe("#9aa4ff");
		expect(palette.paper).toBe("#1a1d20");
		expect(palette.accent).toBe("#6d6bff");
		expect(palette.status.live).toBe("#41d686");
		expect(palette.status.failed).toBe("#ff7d7c");
		expect(palette.series).toHaveLength(8);
		expect(palette.series[0]).toBe("#9aa4ff");
		for (const name of DITHER_COLOR_NAMES) {
			expect(palette.named[name]).toBe(DITHER_COLOR_FALLBACKS[name]);
		}
	});

	test("resolves the tokens in effect at the element to #rrggbb", async () => {
		const { readDitherPalette } = await freshTokens();
		const root = document.documentElement;
		root.style.setProperty("--dither-ink", "#102030");
		root.style.setProperty("--dither-paper", "#fff");
		root.style.setProperty("--dither-series-2", "#0a0b0c");
		root.style.setProperty("--dither-blue", "#123456");
		root.style.setProperty("--color-status-live", "#00ff00");

		const palette = readDitherPalette(root);
		expect(palette.ink).toBe("#102030");
		expect(palette.paper).toBe("#ffffff");
		expect(palette.series[1]).toBe("#0a0b0c");
		expect(palette.named.blue).toBe("#123456");
		expect(palette.status.live).toBe("#00ff00");
		// Untouched tokens keep their defaults.
		expect(palette.named.red).toBe(DITHER_COLOR_FALLBACKS.red);
		expect(palette.status.stopped).toBe("#7a818d");
	});

	test("reads a scoped override on a wrapper element, not the root", async () => {
		const { readDitherPalette } = await freshTokens();
		const wrapper = document.createElement("div");
		wrapper.style.setProperty("--dither-ink", "#abcdef");
		document.body.appendChild(wrapper);

		expect(readDitherPalette(wrapper).ink).toBe("#abcdef");
		expect(readDitherPalette(document.documentElement).ink).toBe("#9aa4ff");
	});

	test("converts oklch tokens through the canvas probe", async () => {
		const canvas = stubCanvas({ "oklch(0.7 0.1 200)": [10, 200, 90] });
		try {
			const { readDitherPalette } = await freshTokens();
			const root = document.documentElement;
			root.style.setProperty("--dither-ink", "oklch(0.7 0.1 200)");
			expect(readDitherPalette(root).ink).toBe("#0ac85a");
		} finally {
			canvas.getContext.mockRestore();
		}
	});

	test("without a document it returns the defaults", async () => {
		const { readDitherPalette } = await freshTokens();
		const descriptor = Object.getOwnPropertyDescriptor(globalThis, "document");
		if (!descriptor?.configurable) return;
		delete (globalThis as { document?: Document }).document;
		try {
			const palette = readDitherPalette();
			expect(palette.ink).toBe("#9aa4ff");
			expect(palette.paper).toBe("#1a1d20");
			expect(palette.series).toHaveLength(8);
			expect(palette.named.grey).toBe(DITHER_COLOR_FALLBACKS.grey);
		} finally {
			Object.defineProperty(globalThis, "document", descriptor);
		}
	});
});

describe("useDitherPalette", () => {
	test("starts from the current theme tokens", async () => {
		const { useDitherPalette } = await freshTokens();
		document.documentElement.style.setProperty("--dither-ink", "#112233");
		const { result } = renderHook(() => useDitherPalette());
		expect(result.current.ink).toBe("#112233");
	});

	test("re-reads when <html> style changes and keeps identity when nothing changed", async () => {
		const { useDitherPalette } = await freshTokens();
		const root = document.documentElement;
		const { result } = renderHook(() => useDitherPalette());
		expect(result.current.ink).toBe("#9aa4ff");
		const before = result.current;

		root.style.setProperty("--dither-ink", "#445566");
		await waitFor(() => expect(result.current.ink).toBe("#445566"));
		expect(result.current).not.toBe(before);

		// A class flip that leaves every resolved colour the same, followed by a
		// real change so we know the observer has processed both.
		const settled = result.current;
		root.className = "unrelated";
		root.style.setProperty("--dither-paper", "#010203");
		await waitFor(() => expect(result.current.paper).toBe("#010203"));
		expect({ ...result.current, paper: settled.paper }).toEqual(settled);
	});

	test("stops observing after unmount", async () => {
		const { useDitherPalette } = await freshTokens();
		const root = document.documentElement;
		const { result, unmount } = renderHook(() => useDitherPalette());
		unmount();
		root.style.setProperty("--dither-ink", "#778899");
		await Promise.resolve();
		await Promise.resolve();
		expect(result.current.ink).toBe("#9aa4ff");
	});
});
