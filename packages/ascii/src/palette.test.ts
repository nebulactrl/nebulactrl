import { afterEach, beforeEach, describe, expect, test } from "bun:test";
import { NEBULA_PALETTE, readPalette } from "./palette";

type Rgba = [number, number, number, number];

interface Probe {
	style: { cssText: string; color: string };
	removed: boolean;
	attributes: Record<string, string>;
}

const globals = ["document", "getComputedStyle"] as const;
const saved = new Map<string, PropertyDescriptor | undefined>();
const define = (name: string, value: unknown) =>
	Object.defineProperty(globalThis, name, {
		configurable: true,
		writable: true,
		value,
	});

let probe: Probe;
let has2d: boolean;
/** What the cascade resolves each token to, by custom property. */
let resolved: Record<string, string>;
/** The bytes the 1x1 canvas reads back for a fill style. */
let paint: Record<string, Rgba>;

beforeEach(() => {
	for (const name of globals) {
		saved.set(name, Object.getOwnPropertyDescriptor(globalThis, name));
	}
	has2d = true;
	resolved = {};
	paint = {};
	probe = { style: { cssText: "", color: "" }, removed: false, attributes: {} };

	let fill = "";
	const context = {
		canvas: { width: 0, height: 0 },
		set fillStyle(value: string) {
			// An unparsable colour leaves the previous fill style in place.
			if (value in paint) fill = value;
			else if (value === "#010203") fill = value;
		},
		get fillStyle() {
			return fill;
		},
		clearRect: () => {},
		fillRect: () => {},
		getImageData: () => ({
			data: fill === "#010203" ? [1, 2, 3, 255] : (paint[fill] ?? [0, 0, 0, 0]),
		}),
	};
	define("document", {
		createElement: (tag: string) =>
			tag === "canvas"
				? { getContext: () => (has2d ? context : null) }
				: {
						style: probe.style,
						setAttribute: (k: string, v: string) => {
							probe.attributes[k] = v;
						},
						remove: () => {
							probe.removed = true;
						},
					},
	});
	define("getComputedStyle", () => {
		const match = /var\((--[\w-]+)\)/.exec(probe.style.color);
		return { color: resolved[match?.[1] ?? ""] ?? "" };
	});
});

afterEach(() => {
	for (const [name, descriptor] of saved) {
		if (descriptor) Object.defineProperty(globalThis, name, descriptor);
		else Reflect.deleteProperty(globalThis, name);
	}
});

function scope() {
	const appended: unknown[] = [];
	return {
		element: {
			appendChild: (child: unknown) => {
				appended.push(child);
			},
		} as unknown as HTMLElement,
		appended,
	};
}

describe("readPalette", () => {
	test("is the shipped palette when the page has no 2D canvas", () => {
		has2d = false;
		expect(readPalette(scope().element)).toEqual(NEBULA_PALETTE);
	});

	test("resolves every token through the cascade and reads sRGB bytes back", () => {
		resolved = {
			"--ascii-dim": "rgb(1, 1, 1)",
			"--ascii-ink": "rgb(2, 2, 2)",
			"--color-accent-brand": "rgb(3, 3, 3)",
			"--color-accent-ink": "rgb(4, 4, 4)",
			"--brand-mark-star": "rgb(5, 5, 5)",
		};
		paint = {
			"rgb(1, 1, 1)": [10, 20, 30, 255],
			"rgb(2, 2, 2)": [40, 50, 60, 255],
			"rgb(3, 3, 3)": [70, 80, 90, 255],
			"rgb(4, 4, 4)": [100, 110, 120, 255],
			"rgb(5, 5, 5)": [130, 140, 150, 255],
		};
		const host = scope();
		expect(readPalette(host.element)).toEqual({
			dim: [10, 20, 30],
			ink: [40, 50, 60],
			accent: [70, 80, 90],
			accentInk: [100, 110, 120],
			star: [130, 140, 150],
		});
		// The probe lives under the given scope only for the reading.
		expect(host.appended).toHaveLength(1);
		expect(probe.removed).toBe(true);
		expect(probe.attributes["aria-hidden"]).toBe("true");
	});

	test("a token the browser cannot parse keeps its shipped colour", () => {
		resolved = { "--ascii-dim": "not-a-colour", "--ascii-ink": "rgb(2, 2, 2)" };
		paint = { "rgb(2, 2, 2)": [40, 50, 60, 255] };
		const palette = readPalette(scope().element);
		expect(palette.dim).toEqual(NEBULA_PALETTE.dim);
		expect(palette.ink).toEqual([40, 50, 60]);
	});

	test("a token that resolves to nothing keeps its shipped colour", () => {
		resolved = { "--brand-mark-star": "rgba(0, 0, 0, 0)" };
		paint = { "rgba(0, 0, 0, 0)": [0, 0, 0, 0] };
		expect(readPalette(scope().element).star).toEqual(NEBULA_PALETTE.star);
	});

	test("does not change the shipped palette", () => {
		const before = structuredClone(NEBULA_PALETTE);
		resolved = { "--ascii-dim": "rgb(1, 1, 1)" };
		paint = { "rgb(1, 1, 1)": [9, 9, 9, 255] };
		readPalette(scope().element);
		expect(NEBULA_PALETTE).toEqual(before);
	});
});
