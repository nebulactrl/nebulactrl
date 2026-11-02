import {
	afterAll,
	afterEach,
	beforeEach,
	describe,
	expect,
	mock,
	test,
} from "bun:test";
import { act, render, waitFor } from "@testing-library/react";
import * as realMotion from "../../lib/motion";
import { type CanvasStub, type FakeImage, stubCanvas2d } from "../canvas-stub";
import { stubImages, stubObservers } from "./test-env";

// The shared clock caches the (preload-pinned) reduced-motion query, so the
// frame loop is driven by hand here: a stand-in `useDitherFrame` hands the
// logo's frame callback and options to the test. The real hook is put back
// for every other file.
const real = { ...realMotion };
type FrameFn = (
	time: number,
	delta: number,
	still?: "reduced" | "disabled",
) => void;
let frame: {
	run: FrameFn;
	options: Parameters<typeof realMotion.useDitherFrame>[1];
} | null = null;
mock.module("../../lib/motion", () => ({
	...real,
	useDitherFrame: (run: FrameFn, options: never) => {
		frame = { run, options };
		return "running";
	},
}));
const ANIMATED: string = "./dithered-logo.tsx?animated";
const { DitheredLogo } = (await import(
	ANIMATED
)) as typeof import("./dithered-logo");
afterAll(() => {
	mock.module("../../lib/motion", () => real);
});

const W = 40;
const H = 20;
/** An opaque 20 × 10 square. */
const square = (w: number, h: number) => {
	const data = new Uint8ClampedArray(w * h * 4);
	for (let y = 5; y < h - 5; y++) {
		for (let x = 10; x < w - 10; x++) {
			data.set([100, 100, 100, 255], (y * w + x) * 4);
		}
	}
	return data;
};

let images: ReturnType<typeof stubImages>;
let observers: ReturnType<typeof stubObservers>;
let canvas: CanvasStub;
beforeEach(() => {
	frame = null;
	images = stubImages({ width: 80, height: 40 });
	observers = stubObservers();
	canvas = stubCanvas2d({ imageData: square });
});
afterEach(() => {
	images.restore();
	observers.restore();
	canvas.restore();
});

let n = 0;
async function mount(props: Partial<Parameters<typeof DitheredLogo>[0]> = {}) {
	const view = render(
		<DitheredLogo
			src={`/motion-${++n}.svg`}
			alt="x"
			color="#646464"
			fill="solid"
			lazy={false}
			{...props}
		/>,
	);
	const host = view.container.firstElementChild as HTMLElement;
	observers.resize(host, W, H);
	await waitFor(() => expect(host.querySelector("canvas")).not.toBeNull());
	const ctx = canvas.contextOf(
		host.querySelector("canvas") as HTMLCanvasElement,
	);
	if (!ctx || !frame) throw new Error("logo did not mount");
	const tick = (time: number) => act(() => frame?.run(time, 0.03));
	const last = (): FakeImage => ctx.puts.at(-1) as FakeImage;
	return { ...view, host, ctx, tick, last };
}

const red = (f: FakeImage, x: number, y: number) =>
	f.data[(y * f.width + x) * 4] ?? 0;
const litRows = (f: FakeImage) => {
	const rows: number[] = [];
	for (let y = 0; y < f.height; y++) if (red(f, 15, y) > 100) rows.push(y);
	return rows;
};

describe("DitheredLogo frame loop", () => {
	test("the loop runs at 30 fps only for an effect, once the logo is drawn", async () => {
		const plain = await mount({ effect: "none" });
		expect(frame?.options).toMatchObject({ fps: 30, enabled: false });
		plain.unmount();
		const shimmer = await mount({ effect: "shimmer" });
		expect(frame?.options).toMatchObject({ fps: 30, enabled: true });
		shimmer.unmount();
	});

	test("the still frame is the moment before the effect starts: -phase × period", async () => {
		await mount({ effect: "scan", period: 4, phase: 0.25 });
		expect(frame?.options?.stillTime).toBe(-1);
	});

	test("a scan line lights the row it is on in a brighter shade of the ink, and moves down with time", async () => {
		const view = await mount({ effect: "scan", period: 2 });
		view.tick(0);
		expect(litRows(view.last())).toHaveLength(0);
		// The logo spans rows 5..14; the line is at 5 + progress × 15 − 3.
		view.tick(0.9);
		expect(litRows(view.last())).toEqual([8]);
		expect(red(view.last(), 15, 8)).toBe(Math.round(100 + (255 - 100) * 0.55));
		view.tick(1.3);
		expect(litRows(view.last())).toEqual([11]);
		// Cells off the line keep the plain ink, and nothing outside the logo is drawn.
		expect(red(view.last(), 15, 8)).toBe(100);
		expect(view.last().data[(2 * W + 15) * 4 + 3]).toBe(0);
	});

	test("the scan loops: a full period later it is the same frame", async () => {
		const view = await mount({ effect: "scan", period: 2 });
		view.tick(0.5);
		const a = Array.from(view.last().data).join();
		view.tick(2.5);
		expect(Array.from(view.last().data).join()).toBe(a);
	});

	test("phase offsets a logo within the loop", async () => {
		const a = await mount({ effect: "scan", period: 2, phase: 0 });
		a.tick(0.8);
		const rowsA = litRows(a.last());
		a.unmount();
		const b = await mount({ effect: "scan", period: 2, phase: 0.1 });
		b.tick(0.8);
		expect(litRows(b.last())[0]).toBeGreaterThan(rowsA[0] as number);
	});

	test("a shimmer changes the picture while the band crosses and is plain outside it", async () => {
		const view = await mount({
			effect: "shimmer",
			fill: "gradient",
			floor: 0,
			outline: false,
			period: 2,
		});
		view.tick(0);
		const rest = Array.from(view.last().data).join();
		let moved = false;
		for (let t = 0.1; t < 1.2; t += 0.1) {
			view.tick(t);
			if (Array.from(view.last().data).join() !== rest) moved = true;
		}
		expect(moved).toBe(true);
	});
});
