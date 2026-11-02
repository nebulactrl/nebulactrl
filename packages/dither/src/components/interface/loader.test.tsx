import { afterEach, beforeEach, describe, expect, test } from "bun:test";
import { render, screen } from "@testing-library/react";
import { type CanvasStub, inkedPixels, stubCanvas2d } from "../canvas-stub";
import { DitherLoader } from "./loader";
import { LOADER_HEIGHT } from "./loader-math";

let stub: CanvasStub;
beforeEach(() => {
	stub = stubCanvas2d();
});
afterEach(() => stub.restore());

const rectsOf = (root: Element) => root.querySelectorAll("svg rect");

describe("DitherLoader inline", () => {
	test("is a status region named by its label, a 16px grid of 4x4 cells by default", () => {
		render(<DitherLoader />);
		const loader = screen.getByRole("status");
		expect(loader).toHaveAttribute("data-slot", "dither-loader");
		expect(loader).toHaveTextContent("Loading");
		const svg = loader.querySelector("svg") as SVGElement;
		expect(svg).toHaveAttribute("aria-hidden", "true");
		expect(svg).toHaveAttribute("width", "16");
		expect(rectsOf(loader)).toHaveLength(16);
	});

	test("label is the spoken text", () => {
		render(<DitherLoader label="Deploying" />);
		expect(screen.getByRole("status")).toHaveTextContent("Deploying");
	});

	test("a numeric size sets the edge; a named size uses the loader heights", () => {
		const { unmount } = render(<DitherLoader size={20} />);
		expect(document.querySelector("svg")).toHaveAttribute("width", "20");
		unmount();
		// `sm` for an inline loader is its height token, 24px.
		render(<DitherLoader size="sm" />);
		expect(document.querySelector("svg")).toHaveAttribute(
			"width",
			String(LOADER_HEIGHT.sm),
		);
	});

	test("cell derives the number of cells from the edge, at least 2 per side", () => {
		const { unmount } = render(<DitherLoader size={16} cell={4} />);
		expect(rectsOf(document.body)).toHaveLength(16);
		unmount();
		render(<DitherLoader size={16} cell={2} />);
		expect(rectsOf(document.body)).toHaveLength(64);
		document.body.innerHTML = "";
		render(<DitherLoader size={4} cell={16} />);
		expect(rectsOf(document.body)).toHaveLength(4);
	});

	test("cells light in a diagonal wave: delay grows with x+y and speed shortens it", () => {
		render(<DitherLoader />);
		const rects = Array.from(rectsOf(document.body)) as SVGRectElement[];
		const delays = rects.map((r) => Number.parseFloat(r.style.animationDelay));
		expect(delays[0]).toBe(0);
		expect(delays.at(-1)).toBeGreaterThan(delays[1] ?? 0);
		expect(rects[0]?.style.animationDuration).toBe("1200ms");
		document.body.innerHTML = "";
		render(<DitherLoader speed={2} />);
		const fast = rectsOf(document.body)[0] as SVGRectElement;
		expect(fast.style.animationDuration).toBe("600ms");
	});

	test("at rest the grid is a dithered gradient: some cells lit, some dim", () => {
		render(<DitherLoader />);
		const opacities = Array.from(
			rectsOf(document.body),
			(r) => (r as SVGRectElement).style.opacity,
		);
		expect(opacities).toContain("1");
		expect(opacities).toContain("0.14");
		// The brightest corner is the first cell, the dimmest the last.
		expect(opacities[0]).toBe("1");
		expect(opacities.at(-1)).toBe("0.14");
	});

	test("a matrix changes which cells are lit", () => {
		render(<DitherLoader size={32} cell={2} />);
		const bayer = Array.from(
			rectsOf(document.body),
			(r) => (r as SVGRectElement).style.opacity,
		);
		document.body.innerHTML = "";
		render(<DitherLoader size={32} cell={2} matrix="blue-noise" />);
		const noise = Array.from(
			rectsOf(document.body),
			(r) => (r as SVGRectElement).style.opacity,
		);
		expect(noise).not.toEqual(bayer);
	});

	test("accent ink by default; color sets an inline colour instead", () => {
		const { unmount } = render(<DitherLoader />);
		expect(screen.getByRole("status")).toHaveClass("text-accent-ink");
		expect(screen.getByRole("status").style.color).toBe("");
		unmount();
		render(<DitherLoader color="green" />);
		const status = screen.getByRole("status");
		expect(status).not.toHaveClass("text-accent-ink");
		expect(status.style.color).toBe("var(--dither-green)");
	});

	test("bloom adds a drop-shadow filter in the loader's colour", () => {
		const { unmount } = render(<DitherLoader />);
		expect(screen.getByRole("status").style.filter).toBe("");
		unmount();
		render(<DitherLoader bloom="high" color="red" />);
		expect(screen.getByRole("status").style.filter).toContain("drop-shadow(");
		expect(screen.getByRole("status").style.filter).toContain(
			"var(--dither-red)",
		);
	});

	test("className is merged", () => {
		render(<DitherLoader className="ml-2" />);
		expect(screen.getByRole("status")).toHaveClass("ml-2", "inline-flex");
	});
});

describe("DitherLoader page", () => {
	test("fills its parent with a 28px grid and a visible caption", () => {
		render(<DitherLoader variant="page" label="Loading project" />);
		const loader = screen.getByRole("status");
		expect(loader).toHaveAttribute("data-variant", "page");
		expect(loader).toHaveClass("min-h-full", "w-full");
		expect(loader.querySelector("svg")).toHaveAttribute("width", "28");
		expect(rectsOf(loader)).toHaveLength(16);
		expect(screen.getByText("Loading project")).toBeVisible();
	});

	test("takes color and bloom", () => {
		render(<DitherLoader variant="page" color="blue" bloom="low" />);
		const loader = screen.getByRole("status");
		expect(loader.style.color).toBe("var(--dither-blue)");
		expect(loader).not.toHaveClass("text-accent-ink");
		expect(
			loader.querySelector<HTMLElement>("svg")?.parentElement?.style.filter,
		).toContain("drop-shadow");
	});

	test("default page loader uses accent ink and no glow", () => {
		render(<DitherLoader variant="page" />);
		const loader = screen.getByRole("status");
		expect(loader).toHaveClass("text-accent-ink");
		expect(
			loader.querySelector<HTMLElement>("svg")?.parentElement?.style.filter,
		).toBe("");
		expect(loader).toHaveTextContent("Loading");
	});
});

describe("DitherLoader drawn shapes", () => {
	test("each shape is a status canvas whose size follows the shape's aspect", () => {
		const dims = {
			ring: [40, 40],
			pulse: [40, 40],
			dots: [80, 40],
			bar: [120, 40],
		} as const;
		for (const [shape, [w, h]] of Object.entries(dims)) {
			const { unmount } = render(
				<DitherLoader variant={shape as "ring"} cell={1} />,
			);
			const loader = screen.getByRole("status");
			expect(loader).toHaveAttribute("data-variant", shape);
			const canvas = loader.querySelector("canvas") as HTMLCanvasElement;
			expect(canvas).toHaveAttribute("aria-hidden", "true");
			expect(canvas.width).toBe(w);
			expect(canvas.height).toBe(h);
			unmount();
		}
	});

	test("size picks the height (sm 24, md 40, lg 64) and the default cell (1 at sm, else 2)", () => {
		const grid = (size: "sm" | "md" | "lg") => {
			const { unmount } = render(<DitherLoader variant="ring" size={size} />);
			const canvas = document.querySelector("canvas") as HTMLCanvasElement;
			const out = [canvas.width, canvas.style.width];
			unmount();
			return out;
		};
		expect(grid("sm")).toEqual([24, "24px"]);
		expect(grid("md")).toEqual([20, "40px"]);
		expect(grid("lg")).toEqual([32, "64px"]);
	});

	test("cell scales the canvas up crisp", () => {
		render(<DitherLoader variant="ring" size="lg" cell={4} />);
		const canvas = document.querySelector("canvas") as HTMLCanvasElement;
		expect(canvas.width).toBe(16);
		expect(canvas.style.width).toBe("64px");
		expect(canvas.style.imageRendering).toBe("pixelated");
	});

	test("it paints a still frame in the ink colour, leaving cells outside the shape clear", () => {
		render(<DitherLoader variant="ring" color="#ff0000" />);
		const canvas = document.querySelector("canvas") as HTMLCanvasElement;
		const image = stub.contextOf(canvas)?.puts.at(-1);
		expect(image).toBeDefined();
		const inked = inkedPixels(image as never);
		expect(inked).toBeGreaterThan(20);
		expect(inked).toBeLessThan(canvas.width * canvas.height);
		// Corners are outside the ring.
		expect(image?.data[3]).toBe(0);
		for (let i = 0; i < (image?.data.length ?? 0); i += 4) {
			if ((image?.data[i + 3] ?? 0) > 0) {
				expect(Array.from(image?.data.slice(i, i + 4) ?? [])).toEqual([
					255, 0, 0, 255,
				]);
			}
		}
	});

	test("a different colour repaints the frame already on screen", () => {
		const { rerender } = render(<DitherLoader variant="bar" color="#ff0000" />);
		const canvas = document.querySelector("canvas") as HTMLCanvasElement;
		const ctx = stub.contextOf(canvas);
		const before = ctx?.puts.length ?? 0;
		rerender(<DitherLoader variant="bar" color="#0000ff" />);
		expect(ctx?.puts.length).toBeGreaterThan(before);
		const last = ctx?.puts.at(-1);
		const first = Array.from(last?.data ?? []).findIndex(
			(value, i) => i % 4 === 3 && value > 0,
		);
		expect(Array.from(last?.data.slice(first - 3, first + 1) ?? [])).toEqual([
			0, 0, 255, 255,
		]);
	});

	test("the shapes paint different frames", () => {
		const frame = (shape: "ring" | "pulse") => {
			const { unmount } = render(<DitherLoader variant={shape} />);
			const canvas = document.querySelector("canvas") as HTMLCanvasElement;
			const out = Array.from(
				stub.contextOf(canvas)?.puts.at(-1)?.data ?? [],
			).join();
			unmount();
			return out;
		};
		expect(frame("ring")).not.toBe(frame("pulse"));
	});

	test("a matrix changes the dither of the frame", () => {
		const frame = (matrix?: "blue-noise") => {
			const { unmount } = render(
				<DitherLoader variant="bar" matrix={matrix} />,
			);
			const canvas = document.querySelector("canvas") as HTMLCanvasElement;
			const out = Array.from(
				stub.contextOf(canvas)?.puts.at(-1)?.data ?? [],
			).join();
			unmount();
			return out;
		};
		expect(frame("blue-noise")).not.toBe(frame());
	});

	test("bloom puts a drop-shadow on the canvas", () => {
		render(<DitherLoader variant="pulse" bloom="low" />);
		expect(document.querySelector("canvas")?.style.filter).toContain(
			"drop-shadow",
		);
	});

	test("label is spoken; className merged", () => {
		render(<DitherLoader variant="dots" label="Syncing" className="mx-2" />);
		const loader = screen.getByRole("status");
		expect(loader).toHaveClass("mx-2");
		expect(loader.querySelector(".sr-only")).toHaveTextContent("Syncing");
	});
});
