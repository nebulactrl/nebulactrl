import { afterEach, beforeEach, describe, expect, test } from "bun:test";
import { act, render } from "@testing-library/react";
import { type CanvasStub, inkedPixels, stubCanvas2d } from "../canvas-stub";
import { BlockCode, rasterise } from "./block-code";

let stub: CanvasStub;
/** Glyph height as a share of its width: tall by default, so the height cap bites. */
let ascentRatio = 0.7;
beforeEach(() => {
	ascentRatio = 0.7;
	stub = stubCanvas2d({
		metrics: (_text, font) => {
			// Ink is as wide as the font size, so the fitted size is visible.
			const px = Number.parseFloat(/700 ([\d.]+)px/.exec(font)?.[1] ?? "100");
			return {
				actualBoundingBoxLeft: 0,
				actualBoundingBoxRight: px,
				actualBoundingBoxAscent: px * ascentRatio,
				actualBoundingBoxDescent: 0,
			};
		},
		imageData: (w, h) => {
			// A solid block of ink in the middle of the glyph box.
			const data = new Uint8ClampedArray(w * h * 4);
			for (let y = 0; y < h; y++)
				for (let x = 0; x < w; x++) data[(y * w + x) * 4 + 3] = 255;
			return data;
		},
	});
});
afterEach(() => stub.restore());

describe("rasterise", () => {
	test("returns null without text", () => {
		expect(rasterise("", 100, 4, "monospace")).toBeNull();
	});

	test("sets the text bold in the given family, fitted to the columns less the extrusion", () => {
		const mask = rasterise("404", 100, 4, "Menlo");
		expect(mask).not.toBeNull();
		const ctx = stub.all.at(-1);
		const drawn = ctx?.texts.at(-1);
		expect(drawn?.text).toBe("404");
		expect(drawn?.font).toMatch(/^700 [\d.]+px Menlo$/);
		// Capped by the 0.36 height share: ink is 0.7 high, so font px = 100*0.36/0.7.
		const px = Number.parseFloat(
			/700 ([\d.]+)px/.exec(drawn?.font ?? "")?.[1] ?? "0",
		);
		expect(px).toBeCloseTo((100 * 0.36) / 0.7, 5);
		expect(mask?.width).toBe(Math.ceil(px));
	});

	test("a narrow grid scales by width rather than height", () => {
		ascentRatio = 0.1;
		rasterise("404", 20, 4, "Menlo");
		const px = Number.parseFloat(
			/700 ([\d.]+)px/.exec(stub.all.at(-1)?.texts.at(-1)?.font ?? "")?.[1] ??
				"0",
		);
		// room = 16 columns; ink width equals the font size.
		expect(px).toBeCloseTo(16, 5);
	});

	test("never fits into fewer than 8 columns", () => {
		ascentRatio = 0.1;
		rasterise("404", 6, 4, "Menlo");
		const px = Number.parseFloat(
			/700 ([\d.]+)px/.exec(stub.all.at(-1)?.texts.at(-1)?.font ?? "")?.[1] ??
				"0",
		);
		expect(px).toBeCloseTo(8, 5);
	});

	test("null when the glyphs have no measurable width", () => {
		stub.restore();
		stub = stubCanvas2d({
			metrics: () => ({ actualBoundingBoxLeft: 0, actualBoundingBoxRight: 0 }),
		});
		expect(rasterise("404", 100, 4, "Menlo")).toBeNull();
	});

	test("null without a 2d canvas", () => {
		stub.restore();
		expect(rasterise("404", 100, 4, "Menlo")).toBeNull();
	});
});

describe("BlockCode", () => {
	const widthOf = (width: number) => {
		const originalObserver = window.ResizeObserver;
		class Fixed {
			private cb: ResizeObserverCallback;
			constructor(cb: ResizeObserverCallback) {
				this.cb = cb;
			}
			observe(el: Element) {
				queueMicrotask(() =>
					this.cb(
						[{ target: el, contentRect: { width } } as ResizeObserverEntry],
						this as unknown as ResizeObserver,
					),
				);
			}
			unobserve() {}
			disconnect() {}
		}
		window.ResizeObserver = Fixed as unknown as typeof ResizeObserver;
		return () => {
			window.ResizeObserver = originalObserver;
		};
	};

	test("is decoration and reserves its height before it is measured", async () => {
		const restore = widthOf(0);
		try {
			const { container } = render(<BlockCode code="404" cell={5} />);
			await act(async () => {});
			const host = container.querySelector(
				"[data-slot=dither-code]",
			) as HTMLElement;
			expect(host).toHaveAttribute("aria-hidden", "true");
			expect(host.style.minHeight).toBe("120px");
			expect(host.querySelector("canvas")).toBeNull();
		} finally {
			restore();
		}
	});

	test("keeps the code as text for forced colours", () => {
		const { container } = render(<BlockCode code="404" />);
		expect(container.querySelector("span")).toHaveTextContent("404");
	});

	test("a box narrower than eight cells draws nothing", async () => {
		const restore = widthOf(30);
		try {
			const { container } = render(<BlockCode code="404" cell={4} />);
			await act(async () => {});
			expect(container.querySelector("canvas")).toBeNull();
		} finally {
			restore();
		}
	});

	test("once measured it draws a canvas one pixel per cell, scaled by the cell size", async () => {
		const restore = widthOf(400);
		try {
			const { container } = render(
				<BlockCode code="404" cell={4} animate={false} />,
			);
			await act(async () => {});
			const canvas = container.querySelector("canvas") as HTMLCanvasElement;
			expect(canvas).not.toBeNull();
			expect(canvas.style.imageRendering).toBe("pixelated");
			expect(canvas.style.width).toBe(`${canvas.width * 4}px`);
			expect(canvas.style.height).toBe(`${canvas.height * 4}px`);
			// 100 columns across, minus nothing: the mask plus the 4-cell extrusion.
			expect(canvas.width).toBeLessThanOrEqual(100);
			expect(
				(container.querySelector("[data-slot=dither-code]") as HTMLElement)
					.style.minHeight,
			).toBe("");
		} finally {
			restore();
		}
	});

	test("the width caps at 672px", async () => {
		ascentRatio = 0.1;
		const restore = widthOf(2000);
		try {
			const { container } = render(
				<BlockCode code="404" cell={4} animate={false} />,
			);
			await act(async () => {});
			const canvas = container.querySelector("canvas") as HTMLCanvasElement;
			expect(canvas.width).toBeLessThanOrEqual(168);
			expect(canvas.width).toBeGreaterThan(100);
		} finally {
			restore();
		}
	});

	test("paints the finished still frame in the ink colour with a lit face", async () => {
		const restore = widthOf(400);
		try {
			const { container } = render(
				<BlockCode code="404" cell={4} color="#00ff00" animate={false} />,
			);
			await act(async () => {});
			const canvas = container.querySelector("canvas") as HTMLCanvasElement;
			const image = stub.contextOf(canvas)?.puts.at(-1);
			expect(image).toBeDefined();
			expect(inkedPixels(image as never)).toBeGreaterThan(50);
			for (let i = 0; i < (image?.data.length ?? 0); i += 4) {
				if ((image?.data[i + 3] ?? 0) > 0) {
					expect([
						image?.data[i],
						image?.data[i + 1],
						image?.data[i + 2],
					]).toEqual([0, 255, 0]);
				}
			}
		} finally {
			restore();
		}
	});

	test("the face is densest at the top and thins towards the baseline", async () => {
		const restore = widthOf(400);
		try {
			const { container } = render(
				<BlockCode code="404" cell={4} depth={0} animate={false} />,
			);
			await act(async () => {});
			const canvas = container.querySelector("canvas") as HTMLCanvasElement;
			const image = stub.contextOf(canvas)?.puts.at(-1);
			const rowInk = (y: number) => {
				let n = 0;
				for (let x = 0; x < canvas.width; x++)
					if ((image?.data[(y * canvas.width + x) * 4 + 3] ?? 0) > 0) n++;
				return n;
			};
			const third = Math.floor(canvas.height / 3);
			const top = Array.from({ length: third }, (_, y) => rowInk(y)).reduce(
				(a, b) => a + b,
				0,
			);
			const bottom = Array.from({ length: third }, (_, y) =>
				rowInk(canvas.height - 1 - y),
			).reduce((a, b) => a + b, 0);
			expect(top).toBeGreaterThan(bottom);
		} finally {
			restore();
		}
	});

	test("depth 0 gives flat letters: less ink than an extruded block of the same code", async () => {
		const restore = widthOf(400);
		try {
			const ink = async (depth: number) => {
				const { container, unmount } = render(
					<BlockCode code="404" cell={4} depth={depth} animate={false} />,
				);
				await act(async () => {});
				const canvas = container.querySelector("canvas") as HTMLCanvasElement;
				const n = inkedPixels(stub.contextOf(canvas)?.puts.at(-1) as never);
				unmount();
				return n;
			};
			expect(await ink(6)).toBeGreaterThan(await ink(0));
		} finally {
			restore();
		}
	});

	test("bloom adds a drop-shadow filter to the canvas", async () => {
		const restore = widthOf(400);
		try {
			const { container } = render(
				<BlockCode
					code="404"
					cell={4}
					bloom="high"
					color="red"
					animate={false}
				/>,
			);
			await act(async () => {});
			const canvas = container.querySelector("canvas") as HTMLCanvasElement;
			expect(canvas.style.filter).toContain("drop-shadow");
			expect(canvas.style.filter).toContain("var(--dither-red)");
		} finally {
			restore();
		}
	});

	test("without bloom there is no filter", async () => {
		const restore = widthOf(400);
		try {
			const { container } = render(
				<BlockCode code="404" cell={4} animate={false} />,
			);
			await act(async () => {});
			expect(
				(container.querySelector("canvas") as HTMLCanvasElement).style.filter,
			).toBe("");
		} finally {
			restore();
		}
	});

	test("fill and matrix change what is painted", async () => {
		const restore = widthOf(400);
		try {
			const frame = async (props: object) => {
				const { container, unmount } = render(
					<BlockCode code="404" cell={4} animate={false} {...props} />,
				);
				await act(async () => {});
				const canvas = container.querySelector("canvas") as HTMLCanvasElement;
				const out = Array.from(
					stub.contextOf(canvas)?.puts.at(-1)?.data ?? [],
				).join();
				unmount();
				return out;
			};
			const base = await frame({});
			expect(await frame({ fill: "dotted" })).not.toBe(base);
			expect(await frame({ matrix: "blue-noise" })).not.toBe(base);
		} finally {
			restore();
		}
	});

	test("a font that loads later re-measures the type", async () => {
		const fontsReady = Promise.resolve();
		const listeners = new Map<string, () => void>();
		const fonts = {
			ready: fontsReady,
			addEventListener: (type: string, fn: () => void) =>
				listeners.set(type, fn),
			removeEventListener: (type: string) => listeners.delete(type),
		};
		Object.defineProperty(document, "fonts", {
			value: fonts,
			configurable: true,
		});
		const restore = widthOf(400);
		try {
			const { unmount } = render(
				<BlockCode code="404" cell={4} animate={false} />,
			);
			await act(async () => {});
			const measuredBefore = stub.all.reduce(
				(n, c) => n + c.measured.length,
				0,
			);
			await act(async () => {
				listeners.get("loadingdone")?.();
			});
			const measuredAfter = stub.all.reduce((n, c) => n + c.measured.length, 0);
			expect(measuredAfter).toBeGreaterThan(measuredBefore);
			unmount();
			expect(listeners.has("loadingdone")).toBe(false);
		} finally {
			restore();
			delete (document as unknown as { fonts?: unknown }).fonts;
		}
	});
});
