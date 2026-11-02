import { afterEach, beforeEach, describe, expect, test } from "bun:test";
import { act, render, waitFor } from "@testing-library/react";
import { type CanvasStub, type FakeImage, stubCanvas2d } from "../canvas-stub";
import { DitheredLogo, type DitheredLogoProps } from "./dithered-logo";
import { setBrandCatalog } from "./logo-source";
import { stubImages, stubObservers } from "./test-env";

/** A 60-wide logo: an opaque red square in the middle of a transparent field. */
const GRID_W = 40;
const GRID_H = 20;
function squareLogo(w: number, h: number): Uint8ClampedArray {
	const data = new Uint8ClampedArray(w * h * 4);
	for (let y = 5; y < h - 5; y++) {
		for (let x = 10; x < w - 10; x++) {
			const i = (y * w + x) * 4;
			data[i] = 200;
			data[i + 1] = 40;
			data[i + 2] = 20;
			data[i + 3] = 255;
		}
	}
	return data;
}

let images: ReturnType<typeof stubImages>;
let observers: ReturnType<typeof stubObservers>;
let canvas: CanvasStub;
let pixels: (w: number, h: number) => Uint8ClampedArray;
const realFetch = globalThis.fetch;

beforeEach(() => {
	images = stubImages({
		width: 80,
		height: 40,
		fail: (s) => s.includes("broken"),
	});
	observers = stubObservers();
	pixels = squareLogo;
	canvas = stubCanvas2d({ imageData: (w, h) => pixels(w, h) });
});
afterEach(() => {
	images.restore();
	observers.restore();
	canvas.restore();
	globalThis.fetch = realFetch;
});

let n = 0;
const logoSrc = (name: string) => `/logo-${name}-${++n}.svg`;

const host = (view: ReturnType<typeof render>) =>
	view.container.firstElementChild as HTMLElement;
const show = (view: ReturnType<typeof render>, w = GRID_W, h = GRID_H) => {
	observers.intersect(host(view), true);
	observers.resize(host(view), w, h);
};
const logo = (props: Partial<DitheredLogoProps> = {}) => (
	<DitheredLogo src={logoSrc("l")} alt="Acme" color="#00ff00" {...props} />
);
/** The last frame painted onto the logo's canvas. */
const painted = (view: ReturnType<typeof render>): FakeImage => {
	const el = host(view).querySelector("canvas") as HTMLCanvasElement;
	return canvas.contextOf(el)?.puts.at(-1) as FakeImage;
};
const alphaAt = (frame: FakeImage, x: number, y: number) =>
	frame.data[(y * frame.width + x) * 4 + 3] ?? 0;
const rgbAt = (frame: FakeImage, x: number, y: number) =>
	Array.from(
		frame.data.slice((y * frame.width + x) * 4, (y * frame.width + x) * 4 + 3),
	);
const inked = (frame: FakeImage) => {
	let count = 0;
	for (let i = 3; i < frame.data.length; i += 4)
		if ((frame.data[i] ?? 0) > 0) count++;
	return count;
};

describe("DitheredLogo structure and state", () => {
	test("a named logo is an image; alt='' hides it", () => {
		const view = render(logo());
		expect(host(view)).toHaveAttribute("role", "img");
		expect(host(view)).toHaveAttribute("aria-label", "Acme");
		view.unmount();
		const hidden = render(logo({ alt: "" }));
		expect(host(hidden)).toHaveAttribute("aria-hidden", "true");
		expect(host(hidden)).not.toHaveAttribute("role");
	});

	test("a lazy logo stays idle, and loads nothing, until it nears the viewport", async () => {
		const s = logoSrc("lazy");
		const view = render(logo({ src: s }));
		await act(async () => {});
		expect(host(view)).toHaveAttribute("data-state", "idle");
		expect(images.loaded).not.toContain(s);
		observers.intersect(host(view), true);
		await waitFor(() => expect(images.loaded).toContain(s));
	});

	test("lazy={false} loads right away", async () => {
		const s = logoSrc("eager");
		render(logo({ src: s, lazy: false }));
		await waitFor(() => expect(images.loaded).toContain(s));
	});

	test("goes loading, then ready once the logo is drawn on the grid", async () => {
		const view = render(logo());
		observers.intersect(host(view), true);
		await waitFor(() =>
			expect(host(view)).toHaveAttribute("data-state", "loading"),
		);
		observers.resize(host(view), GRID_W, GRID_H);
		await waitFor(() =>
			expect(host(view)).toHaveAttribute("data-state", "ready"),
		);
		expect(host(view).querySelector("canvas")).not.toBeNull();
	});

	test("a logo that cannot be loaded is an error with no canvas", async () => {
		const view = render(logo({ src: logoSrc("broken") }));
		show(view);
		await waitFor(() =>
			expect(host(view)).toHaveAttribute("data-state", "error"),
		);
		expect(host(view).querySelector("canvas")).toBeNull();
	});

	test("reserves the file's own ratio, or the one given", async () => {
		const view = render(logo());
		show(view);
		await waitFor(() => expect(host(view).style.aspectRatio).toBe("2 / 1"));
		view.unmount();
		expect(host(render(logo({ ratio: 3 }))).style.aspectRatio).toBe("3 / 1");
		expect(host(render(logo())).style.aspectRatio).toBe("1 / 1");
	});

	test("className and style are applied", () => {
		const view = render(logo({ className: "h-8", style: { color: "red" } }));
		expect(host(view)).toHaveClass("h-8", "inline-block");
		expect(host(view).style.color).toBe("red");
	});

	test("the canvas covers the cell grid of the box, crisp", async () => {
		const view = render(logo({ cell: 2 }));
		show(view, 41, 20);
		await waitFor(() =>
			expect(host(view).querySelector("canvas")).not.toBeNull(),
		);
		const el = host(view).querySelector("canvas") as HTMLCanvasElement;
		// 2px cells: 21 × 10 cover 41 × 20.
		expect(el.width).toBe(21);
		expect(el.height).toBe(10);
		expect(el.style.width).toBe("42px");
		expect(el.style.height).toBe("20px");
		expect(el.style.imageRendering).toBe("pixelated");
		expect(el).toHaveClass("pointer-events-none");
	});
});

describe("DitheredLogo sources", () => {
	test("our own mark is drawn when there is neither src nor brand", async () => {
		// Images are cached by src; a theme colour no other test uses makes this one new.
		document.documentElement.style.setProperty("--brand-mark-from", "#123456");
		try {
			render(<DitheredLogo alt="NebulaCtrl" lazy={false} />);
			await waitFor(() => expect(images.loaded).toHaveLength(1));
			expect(images.loaded[0]).toStartWith("data:image/svg+xml");
			expect(decodeURIComponent(images.loaded[0] ?? "")).toContain(
				"rgb(18,52,86)",
			);
		} finally {
			document.documentElement.style.removeProperty("--brand-mark-from");
		}
	});

	test("a brand is drawn from the host's catalog in its own colour", async () => {
		globalThis.fetch = (async () =>
			new Response(
				'<svg><symbol id="brand-redis" viewBox="0 0 24 24"><path d="M0 0h24v24z"/></symbol></svg>',
			)) as unknown as typeof fetch;
		setBrandCatalog({ spriteUrl: `/sprite-${n}.svg`, hex: () => "#dc382d" });
		render(<DitheredLogo brand="redis" alt="Redis" lazy={false} />);
		await waitFor(() => expect(images.loaded).toHaveLength(1));
		expect(decodeURIComponent(images.loaded[0] ?? "")).toContain(
			'fill="#dc382d"',
		);
	});

	test("an unknown brand is an error", async () => {
		globalThis.fetch = (async () =>
			new Response("<svg></svg>")) as unknown as typeof fetch;
		setBrandCatalog({
			spriteUrl: `/sprite-empty-${++n}.svg`,
			hex: () => "#000",
		});
		const view = render(<DitheredLogo brand="nope" alt="?" lazy={false} />);
		await waitFor(() =>
			expect(host(view)).toHaveAttribute("data-state", "error"),
		);
	});

	test("src wins over brand", async () => {
		const s = logoSrc("wins");
		render(<DitheredLogo src={s} brand="redis" alt="x" lazy={false} />);
		await waitFor(() => expect(images.loaded).toEqual([s]));
	});
});

describe("DitheredLogo drawing", () => {
	test("paints only inside the logo, in one ink, fully opaque", async () => {
		const view = render(logo({ color: "#00ff00", fill: "solid" }));
		show(view);
		await waitFor(() =>
			expect(host(view)).toHaveAttribute("data-state", "ready"),
		);
		const frame = painted(view);
		expect(frame).toMatchObject({ width: GRID_W, height: GRID_H });
		// The silhouette is the square x 10..29, y 5..14.
		for (let y = 0; y < GRID_H; y++) {
			for (let x = 0; x < GRID_W; x++) {
				const insideSquare = x >= 10 && x < 30 && y >= 5 && y < 15;
				expect(alphaAt(frame, x, y)).toBe(insideSquare ? 255 : 0);
				if (insideSquare) expect(rgbAt(frame, x, y)).toEqual([0, 255, 0]);
			}
		}
	});

	test("tone='original' keeps the file's own colours", async () => {
		const view = render(logo({ tone: "original", fill: "solid" }));
		show(view);
		await waitFor(() =>
			expect(host(view)).toHaveAttribute("data-state", "ready"),
		);
		expect(rgbAt(painted(view), 15, 8)).toEqual([200, 40, 20]);
	});

	test("'ink' is the same as 'mono'", async () => {
		const mono = render(logo({ tone: "mono", fill: "solid" }));
		show(mono);
		await waitFor(() =>
			expect(host(mono).querySelector("canvas")).not.toBeNull(),
		);
		const a = Array.from(painted(mono).data).join();
		mono.unmount();
		const ink = render(logo({ tone: "ink", fill: "solid" }));
		show(ink);
		await waitFor(() =>
			expect(host(ink).querySelector("canvas")).not.toBeNull(),
		);
		expect(Array.from(painted(ink).data).join()).toBe(a);
	});

	test("the outline is solid even when the fill thins out; outline={false} lets it dither", async () => {
		const withOutline = render(logo({ fill: "gradient", floor: 0 }));
		show(withOutline);
		await waitFor(() =>
			expect(host(withOutline).querySelector("canvas")).not.toBeNull(),
		);
		const frame = painted(withOutline);
		for (let x = 10; x < 30; x++) {
			expect(alphaAt(frame, x, 5)).toBe(255);
			expect(alphaAt(frame, x, 14)).toBe(255);
		}
		for (let y = 5; y < 15; y++) {
			expect(alphaAt(frame, 10, y)).toBe(255);
			expect(alphaAt(frame, 29, y)).toBe(255);
		}
		withOutline.unmount();
		const bare = render(logo({ fill: "gradient", floor: 0, outline: false }));
		show(bare);
		await waitFor(() =>
			expect(host(bare).querySelector("canvas")).not.toBeNull(),
		);
		expect(inked(painted(bare))).toBeLessThan(inked(frame));
	});

	test("fills differ in density: solid is everything, dotted and hatched are partial", async () => {
		const density = async (fill: DitheredLogoProps["fill"]) => {
			const view = render(
				logo({ fill, outline: false } as Partial<DitheredLogoProps>),
			);
			show(view);
			await waitFor(() =>
				expect(host(view).querySelector("canvas")).not.toBeNull(),
			);
			const count = inked(painted(view));
			view.unmount();
			return count;
		};
		const solid = await density("solid");
		expect(solid).toBe(20 * 10);
		for (const partial of ["dotted", "hatched", "gradient"] as const) {
			const count = await density(partial);
			expect(count).toBeGreaterThan(0);
			expect(count).toBeLessThan(solid);
		}
	});

	test("floor sets how dense the gradient is at the bottom", async () => {
		const count = async (floor: number) => {
			const view = render(logo({ fill: "gradient", floor, outline: false }));
			show(view);
			await waitFor(() =>
				expect(host(view).querySelector("canvas")).not.toBeNull(),
			);
			const c = inked(painted(view));
			view.unmount();
			return c;
		};
		expect(await count(1)).toBeGreaterThan(await count(0));
	});

	test("matrix changes the pattern", async () => {
		const frame = async (matrix: DitheredLogoProps["matrix"]) => {
			const view = render(
				logo({
					fill: "gradient",
					matrix,
					outline: false,
				} as Partial<DitheredLogoProps>),
			);
			show(view);
			await waitFor(() =>
				expect(host(view).querySelector("canvas")).not.toBeNull(),
			);
			const out = Array.from(painted(view).data).join();
			view.unmount();
			return out;
		};
		expect(await frame("blue-noise")).not.toBe(await frame("bayer4"));
		expect(await frame(8)).not.toBe(await frame(2));
	});

	test("source='dark' takes dark pixels as the logo, 'alpha' takes opaque ones", async () => {
		// An opaque white background with a dark square.
		pixels = (w, h) => {
			const data = new Uint8ClampedArray(w * h * 4).fill(255);
			for (let y = 5; y < h - 5; y++) {
				for (let x = 10; x < w - 10; x++) {
					const i = (y * w + x) * 4;
					data[i] = data[i + 1] = data[i + 2] = 10;
				}
			}
			return data;
		};
		const dark = render(
			logo({ source: "dark", fill: "solid", outline: false }),
		);
		show(dark);
		await waitFor(() =>
			expect(host(dark).querySelector("canvas")).not.toBeNull(),
		);
		expect(inked(painted(dark))).toBe(20 * 10);
		dark.unmount();
		const alpha = render(
			logo({ source: "alpha", fill: "solid", outline: false }),
		);
		show(alpha);
		await waitFor(() =>
			expect(host(alpha).querySelector("canvas")).not.toBeNull(),
		);
		expect(inked(painted(alpha))).toBe(GRID_W * GRID_H);
	});

	test("a different colour repaints without reloading the image", async () => {
		const same = logoSrc("recolour");
		const view = render(logo({ src: same, color: "#ff0000", fill: "solid" }));
		show(view);
		await waitFor(() =>
			expect(host(view).querySelector("canvas")).not.toBeNull(),
		);
		const loads = images.loaded.length;
		expect(rgbAt(painted(view), 15, 8)).toEqual([255, 0, 0]);
		view.rerender(logo({ src: same, color: "#0000ff", fill: "solid" }));
		await waitFor(() =>
			expect(rgbAt(painted(view), 15, 8)).toEqual([0, 0, 255]),
		);
		expect(images.loaded).toHaveLength(loads);
	});

	test("a logo with nothing inside draws an empty canvas", async () => {
		pixels = (w, h) => new Uint8ClampedArray(w * h * 4);
		const view = render(logo());
		show(view);
		await waitFor(() =>
			expect(host(view).querySelector("canvas")).not.toBeNull(),
		);
		expect(inked(painted(view))).toBe(0);
	});

	test("no 2d canvas means no raster and it stays loading", async () => {
		canvas.restore();
		canvas = stubCanvas2d({ unavailable: true });
		const view = render(logo());
		show(view);
		await act(async () => {});
		expect(host(view)).toHaveAttribute("data-state", "loading");
		expect(host(view).querySelector("canvas")).toBeNull();
	});
});

describe("DitheredLogo effects", () => {
	test("under reduced motion a shimmer or scan logo is the still frame: same as no effect", async () => {
		const frame = async (effect: DitheredLogoProps["effect"]) => {
			const view = render(
				logo({ effect, fill: "gradient" } as Partial<DitheredLogoProps>),
			);
			show(view);
			await waitFor(() =>
				expect(host(view).querySelector("canvas")).not.toBeNull(),
			);
			const out = Array.from(painted(view).data).join();
			view.unmount();
			return out;
		};
		const plain = await frame("none");
		expect(await frame("scan")).toBe(plain);
		expect(await frame("shimmer")).toBe(plain);
	});
});
