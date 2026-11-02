import { afterEach, beforeEach, describe, expect, test } from "bun:test";
import { act, render, waitFor } from "@testing-library/react";
import { useRef } from "react";
import { type CanvasStub, stubCanvas2d } from "../canvas-stub";
import type { DitherLook } from "./look";
import { gradientRgba, stubImages, stubObservers } from "./test-env";
import {
	type Dithered,
	loadImage,
	paintFrame,
	rasterize,
	useDithered,
	useNaturalSize,
} from "./use-dithered";

let images: ReturnType<typeof stubImages>;
let observers: ReturnType<typeof stubObservers>;
let canvas: CanvasStub;
let reads: Array<[number, number]>;

beforeEach(() => {
	images = stubImages({
		width: 400,
		height: 300,
		fail: (src) => src.includes("broken"),
	});
	observers = stubObservers();
	reads = [];
	canvas = stubCanvas2d({
		imageData: (w, h) => {
			reads.push([w, h]);
			return gradientRgba(w, h);
		},
	});
});
afterEach(() => {
	images.restore();
	observers.restore();
	canvas.restore();
});

let counter = 0;
const unique = (name: string) => `/${name}-${++counter}.png`;

describe("loadImage", () => {
	test("resolves with the decoded image and asks for async decoding", async () => {
		const src = unique("a");
		const image = await loadImage(src);
		expect(image.naturalWidth).toBe(400);
		expect((image as unknown as { decoding: string }).decoding).toBe("async");
	});

	test("one request per src: concurrent callers share the promise", async () => {
		const src = unique("shared");
		const [a, b] = await Promise.all([loadImage(src), loadImage(src)]);
		expect(a).toBe(b);
		expect(images.loaded.filter((s) => s === src)).toHaveLength(1);
	});

	test("a different crossOrigin is a different request, and is set on the image", async () => {
		const src = unique("cors");
		const plain = await loadImage(src);
		const anon = await loadImage(src, "anonymous");
		expect(anon).not.toBe(plain);
		expect(anon.crossOrigin).toBe("anonymous");
		expect(plain.crossOrigin).toBeNull();
	});

	test("rejects when the image cannot be loaded, and a retry loads it again", async () => {
		const src = unique("broken");
		await expect(loadImage(src)).rejects.toThrow(`Could not load ${src}`);
		await expect(loadImage(src)).rejects.toThrow(`Could not load ${src}`);
		expect(images.loaded.filter((s) => s === src)).toHaveLength(2);
	});
});

function NaturalProbe(props: { src: string; enabled: boolean }) {
	const size = useNaturalSize(props.src, props.enabled);
	return (
		<p data-testid="size">{size ? `${size.width}x${size.height}` : "none"}</p>
	);
}

describe("useNaturalSize", () => {
	test("is null until the image has loaded, then its natural size", async () => {
		const { getByTestId } = render(<NaturalProbe src={unique("n")} enabled />);
		expect(getByTestId("size")).toHaveTextContent("none");
		await waitFor(() =>
			expect(getByTestId("size")).toHaveTextContent("400x300"),
		);
	});

	test("loads nothing while disabled, and starts when enabled", async () => {
		const src = unique("lazy");
		const { getByTestId, rerender } = render(
			<NaturalProbe src={src} enabled={false} />,
		);
		await act(async () => {});
		expect(images.loaded).not.toContain(src);
		expect(getByTestId("size")).toHaveTextContent("none");
		rerender(<NaturalProbe src={src} enabled />);
		await waitFor(() =>
			expect(getByTestId("size")).toHaveTextContent("400x300"),
		);
	});

	test("a changed src hides the previous size until the new one loads", async () => {
		const first = unique("one");
		const { getByTestId, rerender } = render(
			<NaturalProbe src={first} enabled />,
		);
		await waitFor(() =>
			expect(getByTestId("size")).toHaveTextContent("400x300"),
		);
		rerender(<NaturalProbe src={unique("two")} enabled={false} />);
		expect(getByTestId("size")).toHaveTextContent("none");
	});

	test("a failing image leaves it null", async () => {
		const { getByTestId } = render(
			<NaturalProbe src={unique("broken")} enabled />,
		);
		await act(async () => {});
		expect(getByTestId("size")).toHaveTextContent("none");
	});
});

describe("paintFrame", () => {
	test("sizes the canvas to the frame's grid and puts the pixels", () => {
		const el = document.createElement("canvas");
		const data = new Uint8ClampedArray(3 * 2 * 4).fill(7);
		paintFrame(el, { width: 3, height: 2, data });
		expect(el.width).toBe(3);
		expect(el.height).toBe(2);
		const put = canvas.contextOf(el)?.puts.at(-1);
		expect(put?.width).toBe(3);
		expect(put?.height).toBe(2);
		expect(Array.from(put?.data ?? [])).toEqual(Array.from(data));
	});

	test("does not throw without a 2d context", () => {
		canvas.restore();
		canvas = stubCanvas2d({ unavailable: true });
		const el = document.createElement("canvas");
		expect(() =>
			paintFrame(el, { width: 1, height: 1, data: new Uint8ClampedArray(4) }),
		).not.toThrow();
		expect(el.width).toBe(1);
	});
});

describe("rasterize", () => {
	const image = (w: number, h: number) =>
		({ naturalWidth: w, naturalHeight: h }) as HTMLImageElement;

	test("draws the image fitted to the grid and reads back one pixel per cell", () => {
		const data = rasterize(image(400, 300), 200, 150, "cover");
		expect(data).toHaveLength(200 * 150 * 4);
		expect(reads.at(-1)).toEqual([200, 150]);
		const ctx = canvas.all.at(-1);
		expect(ctx?.draws.at(-1)?.args).toEqual([0, 0, 400, 300, 0, 0, 200, 150]);
		expect(ctx?.imageSmoothingEnabled).toBe(true);
		expect(ctx?.imageSmoothingQuality).toBe("high");
	});

	test("cover crops the source to the box's aspect", () => {
		rasterize(image(400, 300), 200, 200, "cover");
		const args = canvas.all.at(-1)?.draws.at(-1)?.args ?? [];
		// Square target from a 4:3 source: a 300×300 slice, centred.
		expect(args.slice(0, 4)).toEqual([50, 0, 300, 300]);
		expect(args.slice(4)).toEqual([0, 0, 200, 200]);
	});

	test("contain letterboxes the whole source", () => {
		rasterize(image(400, 300), 200, 200, "contain");
		const args = canvas.all.at(-1)?.draws.at(-1)?.args ?? [];
		expect(args.slice(0, 4)).toEqual([0, 0, 400, 300]);
		// 200 wide, 150 tall, centred vertically.
		expect(args.slice(4)).toEqual([0, 25, 200, 150]);
	});

	test("a big downscale halves in steps first, so detail does not alias", () => {
		rasterize(image(1600, 1200), 100, 75, "cover");
		const draws = canvas.all.flatMap((ctx) => ctx.draws);
		// 1600×1200 → 800×600 → 400×300 → 200×150 → final 100×75.
		expect(draws.map((d) => d.args.slice(6, 8))).toEqual([
			[800, 600],
			[400, 300],
			[200, 150],
			[100, 75],
		]);
		expect(draws[0]?.args.slice(0, 4)).toEqual([0, 0, 1600, 1200]);
		expect(draws[1]?.args.slice(0, 4)).toEqual([0, 0, 800, 600]);
	});

	test("throws without a 2d canvas", () => {
		canvas.restore();
		canvas = stubCanvas2d({ unavailable: true });
		expect(() => rasterize(image(10, 10), 5, 5, "cover")).toThrow(
			"2D canvas is unavailable",
		);
	});
});

interface ProbeProps {
	src: string;
	look?: DitherLook;
	multipliers?: readonly number[];
	enabled?: boolean;
	onState?: (state: Dithered) => void;
}

function DitheredProbe({
	src,
	look = { cell: 4 },
	multipliers,
	enabled,
	onState,
}: ProbeProps) {
	const boxRef = useRef<HTMLDivElement>(null);
	const state = useDithered({
		src,
		look,
		boxRef,
		...(multipliers ? { multipliers } : {}),
		...(enabled === undefined ? {} : { enabled }),
	});
	onState?.(state);
	return (
		<div ref={boxRef} data-testid="box" data-status={state.status}>
			{state.frames.length}
		</div>
	);
}

describe("useDithered", () => {
	test("stays idle until the box has a size, then loads and dithers to the cell grid", async () => {
		const src = unique("grid");
		let latest: Dithered | undefined;
		const { getByTestId } = render(
			<DitheredProbe src={src} onState={(s) => (latest = s)} />,
		);
		const box = getByTestId("box");
		await act(async () => {});
		expect(box).toHaveAttribute("data-status", "idle");
		expect(images.loaded).not.toContain(src);

		observers.resize(box, 200, 100);
		await waitFor(() => expect(box).toHaveAttribute("data-status", "ready"));
		// 4px cells over 200×100 CSS px.
		expect(latest?.frames[0]).toMatchObject({ width: 50, height: 25 });
		expect(latest?.cssSize).toEqual({ width: 200, height: 100 });
		expect(latest?.natural).toEqual({ width: 400, height: 300 });
		expect(latest?.frames[0]?.data).toHaveLength(50 * 25 * 4);
	});

	test("reports loading while the first frame is in flight", async () => {
		const states: string[] = [];
		const { getByTestId } = render(
			<DitheredProbe
				src={unique("loading")}
				onState={(s) => states.push(s.status)}
			/>,
		);
		observers.resize(getByTestId("box"), 80, 40);
		await waitFor(() =>
			expect(getByTestId("box")).toHaveAttribute("data-status", "ready"),
		);
		expect(states).toContain("loading");
		expect(states.indexOf("loading")).toBeLessThan(states.lastIndexOf("ready"));
	});

	test("the dither uses the look's palette: only its colours come out, both of them", async () => {
		let latest: Dithered | undefined;
		const { getByTestId } = render(
			<DitheredProbe
				src={unique("palette")}
				look={{ cell: 4, palette: ["#000000", "#ffffff"], method: "bayer4" }}
				onState={(s) => (latest = s)}
			/>,
		);
		observers.resize(getByTestId("box"), 200, 100);
		await waitFor(() =>
			expect(getByTestId("box")).toHaveAttribute("data-status", "ready"),
		);
		const colours = new Set<string>();
		const data = latest?.frames[0]?.data ?? new Uint8ClampedArray();
		for (let i = 0; i < data.length; i += 4) {
			colours.add(`${data[i]},${data[i + 1]},${data[i + 2]},${data[i + 3]}`);
		}
		expect(Array.from(colours).sort()).toEqual([
			"0,0,0,255",
			"255,255,255,255",
		]);
	});

	test("one frame per multiplier, coarser blocks first, with the css size of the base grid", async () => {
		let latest: Dithered | undefined;
		const { getByTestId } = render(
			<DitheredProbe
				src={unique("ladder")}
				multipliers={[4, 2, 1]}
				onState={(s) => (latest = s)}
			/>,
		);
		observers.resize(getByTestId("box"), 160, 80);
		await waitFor(() =>
			expect(getByTestId("box")).toHaveAttribute("data-status", "ready"),
		);
		expect(latest?.frames.map((f) => [f.width, f.height])).toEqual([
			[10, 5],
			[20, 10],
			[40, 20],
		]);
		expect(latest?.cssSize).toEqual({ width: 160, height: 80 });
	});

	test("an image that fails to load is an error, not a hang", async () => {
		const { getByTestId } = render(<DitheredProbe src={unique("broken")} />);
		observers.resize(getByTestId("box"), 100, 100);
		await waitFor(() =>
			expect(getByTestId("box")).toHaveAttribute("data-status", "error"),
		);
	});

	test("enabled=false does no work at all", async () => {
		const src = unique("off");
		const { getByTestId } = render(<DitheredProbe src={src} enabled={false} />);
		observers.resize(getByTestId("box"), 100, 100);
		await act(async () => {});
		expect(getByTestId("box")).toHaveAttribute("data-status", "idle");
		expect(images.loaded).not.toContain(src);
	});

	test("an empty box does no work", async () => {
		const src = unique("empty");
		const { getByTestId } = render(<DitheredProbe src={src} />);
		observers.resize(getByTestId("box"), 0, 0);
		await act(async () => {});
		expect(getByTestId("box")).toHaveAttribute("data-status", "idle");
		expect(images.loaded).not.toContain(src);
	});

	test("a resize re-dithers to the new grid and keeps showing the old frames meanwhile", async () => {
		let latest: Dithered | undefined;
		const { getByTestId } = render(
			<DitheredProbe src={unique("resize")} onState={(s) => (latest = s)} />,
		);
		const box = getByTestId("box");
		observers.resize(box, 100, 100);
		await waitFor(() => expect(latest?.frames[0]?.width).toBe(25));
		const statuses: string[] = [];
		observers.resize(box, 200, 100);
		statuses.push(latest?.status ?? "");
		await waitFor(() => expect(latest?.frames[0]?.width).toBe(50));
		// The old frame stayed on screen: never dropped back to loading.
		expect(statuses).toEqual(["ready"]);
		expect(latest?.status).toBe("ready");
	});

	test("the same grid is dithered once: a remount reads no pixels again", async () => {
		const src = unique("cached");
		const first = render(<DitheredProbe src={src} />);
		observers.resize(first.getByTestId("box"), 100, 100);
		await waitFor(() =>
			expect(first.getByTestId("box")).toHaveAttribute("data-status", "ready"),
		);
		const readsAfterFirst = reads.length;
		expect(readsAfterFirst).toBe(1);
		first.unmount();
		const second = render(<DitheredProbe src={src} />);
		observers.resize(second.getByTestId("box"), 100, 100);
		await waitFor(() =>
			expect(second.getByTestId("box")).toHaveAttribute("data-status", "ready"),
		);
		expect(reads).toHaveLength(readsAfterFirst);
	});

	test("a different look is a different dither of the same grid", async () => {
		const src = unique("looks");
		let latest: Dithered | undefined;
		const view = render(
			<DitheredProbe
				src={src}
				look={{ cell: 4, palette: ["#000000", "#ffffff"], invert: false }}
				onState={(s) => (latest = s)}
			/>,
		);
		observers.resize(view.getByTestId("box"), 100, 100);
		await waitFor(() => expect(latest?.status).toBe("ready"));
		const normal = Array.from(latest?.frames[0]?.data ?? []).join();
		view.rerender(
			<DitheredProbe
				src={src}
				look={{ cell: 4, palette: ["#000000", "#ffffff"], invert: true }}
				onState={(s) => (latest = s)}
			/>,
		);
		await waitFor(() =>
			expect(Array.from(latest?.frames[0]?.data ?? []).join()).not.toBe(normal),
		);
		expect(reads).toHaveLength(2);
	});

	test("an inline look object with the same content does not restart the work", async () => {
		const src = unique("stable");
		const view = render(<DitheredProbe src={src} look={{ cell: 4 }} />);
		observers.resize(view.getByTestId("box"), 100, 100);
		await waitFor(() =>
			expect(view.getByTestId("box")).toHaveAttribute("data-status", "ready"),
		);
		const loads = images.loaded.length;
		view.rerender(<DitheredProbe src={src} look={{ cell: 4 }} />);
		await act(async () => {});
		expect(images.loaded).toHaveLength(loads);
		expect(view.getByTestId("box")).toHaveAttribute("data-status", "ready");
	});
});
