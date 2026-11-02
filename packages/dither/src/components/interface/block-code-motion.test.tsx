import {
	afterAll,
	afterEach,
	beforeEach,
	describe,
	expect,
	mock,
	spyOn,
	test,
} from "bun:test";
import { act, render } from "@testing-library/react";
import * as realMotion from "../../lib/motion";
import {
	type CanvasStub,
	type FakeImage,
	inkedPixels,
	stubCanvas2d,
} from "../canvas-stub";
import { PRINT_SECONDS } from "./block-digits";
import { GLITCH_FRAME_SECONDS } from "./block-glitch";

// The shared clock is pinned to reduced motion by the test preload, and is
// module state shared across test files. These tests drive BlockCode's frame
// callback by hand instead: a stand-in `useDitherFrame` hands it to the test,
// which then plays the animation at chosen times. The real hook is put back
// afterwards for every other file.
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
const ANIMATED: string = "./block-code.tsx?animated";
const { BlockCode } = (await import(ANIMATED)) as typeof import("./block-code");
afterAll(() => {
	mock.module("../../lib/motion", () => real);
});

let stub: CanvasStub;
let restoreResize: () => void;
beforeEach(() => {
	frame = null;
	stub = stubCanvas2d({
		metrics: (_t, font) => {
			const px = Number.parseFloat(/700 ([\d.]+)px/.exec(font)?.[1] ?? "100");
			return {
				actualBoundingBoxLeft: 0,
				actualBoundingBoxRight: px,
				actualBoundingBoxAscent: px * 0.3,
				actualBoundingBoxDescent: 0,
			};
		},
		imageData: (w, h) => {
			const data = new Uint8ClampedArray(w * h * 4);
			for (let i = 3; i < data.length; i += 4) data[i] = 255;
			return data;
		},
	});
	const original = window.ResizeObserver;
	class Fixed {
		private cb: ResizeObserverCallback;
		constructor(cb: ResizeObserverCallback) {
			this.cb = cb;
		}
		observe(el: Element) {
			queueMicrotask(() =>
				this.cb(
					[{ target: el, contentRect: { width: 400 } } as ResizeObserverEntry],
					this as unknown as ResizeObserver,
				),
			);
		}
		unobserve() {}
		disconnect() {}
	}
	window.ResizeObserver = Fixed as unknown as typeof ResizeObserver;
	restoreResize = () => {
		window.ResizeObserver = original;
	};
});
afterEach(() => {
	stub.restore();
	restoreResize();
});

async function mount(props: Partial<Parameters<typeof BlockCode>[0]> = {}) {
	const view = render(
		<BlockCode code="404" cell={4} color="#ff0000" {...props} />,
	);
	await act(async () => {});
	const canvas = view.container.querySelector("canvas") as HTMLCanvasElement;
	const ctx = stub.contextOf(canvas);
	if (!ctx || !frame) throw new Error("block did not mount");
	const play = (time: number, still?: "reduced" | "disabled") =>
		act(() => frame?.run(time, 0.05, still));
	const last = (): FakeImage => ctx.puts.at(-1) as FakeImage;
	return {
		...view,
		canvas,
		ctx,
		play,
		last,
		frameOptions: () => frame?.options,
	};
}

describe("BlockCode frame loop", () => {
	test("runs at 20 fps while animate is on and is held still when it is off", async () => {
		const on = await mount();
		expect(on.frameOptions()).toMatchObject({
			fps: 20,
			enabled: true,
			stillTime: 0,
		});
		on.unmount();
		const off = await mount({ animate: false });
		expect(off.frameOptions()).toMatchObject({ enabled: false });
	});

	test("prints in level by level: no ink at the start, more each level, full at PRINT_SECONDS", async () => {
		const block = await mount({ depth: 0 });
		const ink: number[] = [];
		for (const share of [0, 0.25, 0.5, 0.75, 1]) {
			block.play(PRINT_SECONDS * share);
			ink.push(inkedPixels(block.last()));
		}
		expect(ink[0]).toBe(0);
		for (let i = 1; i < ink.length; i++) {
			expect(ink[i]).toBeGreaterThanOrEqual(ink[i - 1] as number);
		}
		expect(ink.at(-1)).toBeGreaterThan(ink[1] as number);
		// Levels are whole steps: two times inside one step paint the same frame.
		block.play(PRINT_SECONDS * 0.5);
		const mid = Array.from(block.last().data).join();
		block.play(PRINT_SECONDS * 0.52);
		expect(Array.from(block.last().data).join()).toBe(mid);
	});

	test("the still frame under reduced motion is the finished print", async () => {
		const block = await mount({ depth: 0 });
		block.play(0);
		const first = inkedPixels(block.last());
		block.play(0, "reduced");
		const still = block.last();
		block.play(PRINT_SECONDS + 5.0);
		expect(first).toBe(0);
		expect(inkedPixels(still)).toBeGreaterThan(0);
		expect(Array.from(still.data).join()).toBe(
			Array.from(block.last().data).join(),
		);
	});

	test("a band of light adds ink while it crosses and leaves the finished frame after", async () => {
		const block = await mount({ depth: 0 });
		const start = 0;
		block.play(start);
		block.play(start + PRINT_SECONDS);
		const finished = Array.from(block.last().data).join();
		const finishedInk = inkedPixels(block.last());
		// The sweep starts 0.4 s after the print and crosses in 1.3 s.
		block.play(start + PRINT_SECONDS + 0.4 + 0.65);
		const during = block.last();
		expect(Array.from(during.data).join()).not.toBe(finished);
		expect(inkedPixels(during)).toBeGreaterThanOrEqual(finishedInk);
		block.play(start + PRINT_SECONDS + 0.4 + 3);
		expect(Array.from(block.last().data).join()).toBe(finished);
	});

	test("glitch motion has no sweep: the finished frame stays clean between bursts", async () => {
		const random = spyOn(Math, "random").mockReturnValue(0);
		try {
			const block = await mount({ depth: 0, motion: "glitch" });
			const start = 0;
			block.play(start);
			block.play(start + PRINT_SECONDS);
			const clean = Array.from(block.last().data).join();
			block.play(start + PRINT_SECONDS + 0.4 + 0.65);
			expect(Array.from(block.last().data).join()).toBe(clean);
		} finally {
			random.mockRestore();
		}
	});

	test("glitch: still between bursts, one stepped fault frame, then the clean letters again", async () => {
		const random = spyOn(Math, "random").mockReturnValue(0);
		try {
			const block = await mount({ depth: 0, motion: "glitch" });
			const start = 0;
			block.play(start);
			// The clock starts when printing ends; with random() = 0 the gap is 3 s.
			const ready = start + PRINT_SECONDS;
			block.play(ready);
			const clean = Array.from(block.last().data).join();
			const paintsWhenClean = block.ctx.puts.length;

			block.play(ready + 1);
			block.play(ready + 2.5);
			expect(block.ctx.puts.length).toBe(paintsWhenClean);

			block.play(ready + 3);
			expect(block.ctx.puts.length).toBe(paintsWhenClean + 1);
			const fault = Array.from(block.last().data).join();
			expect(fault).not.toBe(clean);
			// The fault only moves or drops the ink already there: no new colour.
			const colours = new Set<string>();
			const data = block.last().data;
			for (let i = 0; i < data.length; i += 4) {
				if ((data[i + 3] ?? 0) > 0)
					colours.add(`${data[i]},${data[i + 1]},${data[i + 2]}`);
			}
			expect(Array.from(colours)).toEqual(["255,0,0"]);

			// The burst is three 50 ms frames, then the letters snap back.
			block.play(ready + 3 + 3 * GLITCH_FRAME_SECONDS + 0.01);
			expect(Array.from(block.last().data).join()).toBe(clean);
			const after = block.ctx.puts.length;
			block.play(ready + 3 + 3 * GLITCH_FRAME_SECONDS + 1);
			expect(block.ctx.puts.length).toBe(after);
		} finally {
			random.mockRestore();
		}
	});

	test("a paused block shows one clean frame, never a fault", async () => {
		const random = spyOn(Math, "random").mockReturnValue(0);
		try {
			const block = await mount({ depth: 0, motion: "glitch" });
			const start = 0;
			block.play(start);
			block.play(start + PRINT_SECONDS);
			const clean = Array.from(block.last().data).join();
			// Past the first burst's time, but delivered as a still frame.
			block.play(start + PRINT_SECONDS + 3.2, "disabled");
			expect(Array.from(block.last().data).join()).toBe(clean);
		} finally {
			random.mockRestore();
		}
	});

	test("a new colour repaints the frame on screen without waiting for the clock", async () => {
		const block = await mount({ depth: 0, color: "#ff0000" });
		block.play(PRINT_SECONDS);
		const before = block.ctx.puts.length;
		block.rerender(<BlockCode code="404" cell={4} depth={0} color="#0000ff" />);
		await act(async () => {});
		expect(block.ctx.puts.length).toBeGreaterThan(before);
		const data = block.last().data;
		const at = Array.from(data).findIndex((v, i) => i % 4 === 3 && v > 0);
		expect(Array.from(data.slice(at - 3, at + 1))).toEqual([0, 0, 255, 255]);
	});
});
