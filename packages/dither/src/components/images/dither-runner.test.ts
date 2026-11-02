import { afterEach, beforeEach, describe, expect, test } from "bun:test";
import {
	DEFAULT_RASTER_OPTIONS,
	ditherRGBA,
	type RasterOptions,
} from "./algorithms";
import type {
	DitherWorkerRequest,
	DitherWorkerResponse,
} from "./dither.worker";
import { gradientRgba } from "./test-env";

// dither-runner keeps a worker, a cache and an in-flight table in module
// state, and modules are shared across test files: every test loads its own copy.
let copy = 0;
const load = (): Promise<typeof import("./dither-runner")> =>
	import(`./dither-runner.ts?runner${++copy}`);

const options: RasterOptions = {
	...DEFAULT_RASTER_OPTIONS,
	algorithm: "bayer",
};

/** More pixels than the worker threshold (256 × 1024). */
const BIG = { width: 600, height: 450 };
const SMALL = { width: 16, height: 12 };

type FakeWorkerBehaviour = (
	worker: FakeWorker,
	request: DitherWorkerRequest,
) => void;

class FakeWorker {
	static instances: FakeWorker[] = [];
	static behaviour: FakeWorkerBehaviour = (worker, request) =>
		worker.reply(request);
	static throwOnCreate = false;
	onmessage: ((event: MessageEvent<DitherWorkerResponse>) => void) | null =
		null;
	onerror: (() => void) | null = null;
	requests: DitherWorkerRequest[] = [];
	terminated = false;
	url: URL;
	init: WorkerOptions;
	constructor(url: URL, init: WorkerOptions) {
		this.url = url;
		this.init = init;
		if (FakeWorker.throwOnCreate) throw new Error("no workers here");
		FakeWorker.instances.push(this);
	}
	postMessage(request: DitherWorkerRequest, transfer: Transferable[]) {
		this.requests.push(request);
		expect(transfer).toEqual([request.buffer]);
		queueMicrotask(() => FakeWorker.behaviour(this, request));
	}
	terminate() {
		this.terminated = true;
	}
	/** Answer the way the real worker does: dither with the same function. */
	reply(request: DitherWorkerRequest) {
		const out = ditherRGBA(
			new Uint8ClampedArray(request.buffer),
			request.width,
			request.height,
			request.options,
		);
		this.onmessage?.({
			data: { id: request.id, ok: true, buffer: out.buffer as ArrayBuffer },
		} as MessageEvent<DitherWorkerResponse>);
	}
}

const RealWorker = globalThis.Worker;
beforeEach(() => {
	FakeWorker.instances = [];
	FakeWorker.behaviour = (worker, request) => worker.reply(request);
	FakeWorker.throwOnCreate = false;
	globalThis.Worker = FakeWorker as unknown as typeof Worker;
});
afterEach(() => {
	globalThis.Worker = RealWorker;
});

describe("ditherCached on the main thread", () => {
	test("small bitmaps are dithered inline, never touching a worker", async () => {
		const { ditherCached } = await load();
		const source = gradientRgba(SMALL.width, SMALL.height);
		const frame = await ditherCached(
			"small",
			SMALL.width,
			SMALL.height,
			options,
			() => source,
		);
		expect(frame.width).toBe(SMALL.width);
		expect(frame.height).toBe(SMALL.height);
		expect(Array.from(frame.data)).toEqual(
			Array.from(ditherRGBA(source, SMALL.width, SMALL.height, options)),
		);
		expect(FakeWorker.instances).toHaveLength(0);
	});

	test("the source is read only on a miss", async () => {
		const { ditherCached } = await load();
		let reads = 0;
		const source = () => {
			reads++;
			return gradientRgba(SMALL.width, SMALL.height);
		};
		const first = await ditherCached(
			"k",
			SMALL.width,
			SMALL.height,
			options,
			source,
		);
		const second = await ditherCached(
			"k",
			SMALL.width,
			SMALL.height,
			options,
			source,
		);
		expect(reads).toBe(1);
		expect(second).toBe(first);
	});

	test("identical requests in flight share one job", async () => {
		const { ditherCached } = await load();
		let reads = 0;
		const source = () => {
			reads++;
			return gradientRgba(SMALL.width, SMALL.height);
		};
		const a = ditherCached("same", SMALL.width, SMALL.height, options, source);
		const b = ditherCached("same", SMALL.width, SMALL.height, options, source);
		expect(b).toBe(a);
		expect(await a).toBe(await b);
		expect(reads).toBe(1);
	});

	test("different keys are different frames", async () => {
		const { ditherCached } = await load();
		const a = await ditherCached("a", 8, 8, options, () => gradientRgba(8, 8));
		const b = await ditherCached("b", 8, 8, options, () => gradientRgba(8, 8));
		expect(b).not.toBe(a);
	});

	test("an empty palette yields a blank frame instead of failing", async () => {
		const { ditherCached } = await load();
		const frame = await ditherCached(
			"empty",
			4,
			4,
			{ ...options, palette: [] },
			() => gradientRgba(4, 4),
		);
		expect(Array.from(frame.data).every((v) => v === 0)).toBe(true);
	});

	test("a failing source rejects and is not cached", async () => {
		const { ditherCached } = await load();
		const bad = () => {
			throw new Error("getImageData failed");
		};
		expect(() => ditherCached("bad", 4, 4, options, bad)).toThrow(
			"getImageData failed",
		);
		const ok = await ditherCached("bad", 4, 4, options, () =>
			gradientRgba(4, 4),
		);
		expect(ok.width).toBe(4);
	});
});

describe("ditherCached with a worker", () => {
	test("a big bitmap goes to a module worker as a transferred copy and comes back dithered", async () => {
		const { ditherCached } = await load();
		const source = gradientRgba(BIG.width, BIG.height);
		const before = Array.from(source.slice(0, 64));
		const frame = await ditherCached(
			"big",
			BIG.width,
			BIG.height,
			options,
			() => source,
		);
		const [worker] = FakeWorker.instances;
		expect(FakeWorker.instances).toHaveLength(1);
		expect(worker?.init).toEqual({ type: "module" });
		expect(worker?.url.pathname).toEndWith("dither.worker.ts");
		const request = worker?.requests[0];
		expect(request).toMatchObject({
			width: BIG.width,
			height: BIG.height,
			options,
		});
		// The caller's bitmap survives: a copy was transferred.
		expect(source.byteLength).toBe(BIG.width * BIG.height * 4);
		expect(Array.from(source.slice(0, 64))).toEqual(before);
		expect(frame.data).toHaveLength(BIG.width * BIG.height * 4);
		expect(frame.width).toBe(BIG.width);
	}, 20_000);

	test("the worker is created once and reused", async () => {
		const { ditherCached } = await load();
		const src = () => gradientRgba(BIG.width, BIG.height);
		await ditherCached("w1", BIG.width, BIG.height, options, src);
		await ditherCached("w2", BIG.width, BIG.height, options, src);
		expect(FakeWorker.instances).toHaveLength(1);
		expect(FakeWorker.instances[0]?.requests.map((r) => r.id)).toEqual([1, 2]);
	}, 20_000);

	test("a worker that reports an error makes the job fall back to the main thread", async () => {
		FakeWorker.behaviour = (worker, request) =>
			worker.onmessage?.({
				data: { id: request.id, ok: false, error: "boom" },
			} as MessageEvent<DitherWorkerResponse>);
		const { ditherCached } = await load();
		const source = gradientRgba(BIG.width, BIG.height);
		const frame = await ditherCached(
			"fail",
			BIG.width,
			BIG.height,
			options,
			() => source,
		);
		expect(Array.from(frame.data.slice(0, 256))).toEqual(
			Array.from(
				ditherRGBA(source, BIG.width, BIG.height, options).slice(0, 256),
			),
		);
	}, 20_000);

	test("a crashed worker is terminated, stays off, and later jobs run inline", async () => {
		FakeWorker.behaviour = (worker) => worker.onerror?.();
		const { ditherCached } = await load();
		const src = () => gradientRgba(BIG.width, BIG.height);
		const first = await ditherCached(
			"crash",
			BIG.width,
			BIG.height,
			options,
			src,
		);
		expect(first.width).toBe(BIG.width);
		expect(FakeWorker.instances[0]?.terminated).toBe(true);
		await ditherCached("after", BIG.width, BIG.height, options, src);
		expect(FakeWorker.instances).toHaveLength(1);
		expect(FakeWorker.instances[0]?.requests).toHaveLength(1);
	}, 20_000);

	test("a worker that cannot be created falls back, and is not retried", async () => {
		FakeWorker.throwOnCreate = true;
		const { ditherCached } = await load();
		const src = () => gradientRgba(BIG.width, BIG.height);
		const frame = await ditherCached(
			"nocreate",
			BIG.width,
			BIG.height,
			options,
			src,
		);
		expect(frame.width).toBe(BIG.width);
		FakeWorker.throwOnCreate = false;
		await ditherCached("nocreate2", BIG.width, BIG.height, options, src);
		expect(FakeWorker.instances).toHaveLength(0);
	}, 20_000);

	test("a reply for an unknown request is ignored", async () => {
		const { ditherCached } = await load();
		const source = gradientRgba(BIG.width, BIG.height);
		FakeWorker.behaviour = (worker, request) => {
			worker.onmessage?.({
				data: { id: 999, ok: true, buffer: new ArrayBuffer(4) },
			} as MessageEvent<DitherWorkerResponse>);
			worker.reply(request);
		};
		const frame = await ditherCached(
			"stray",
			BIG.width,
			BIG.height,
			options,
			() => source,
		);
		expect(frame.data).toHaveLength(BIG.width * BIG.height * 4);
	}, 20_000);

	test("without Worker support a big bitmap is dithered inline", async () => {
		// @ts-expect-error simulating an environment without workers
		globalThis.Worker = undefined;
		const { ditherCached } = await load();
		const frame = await ditherCached(
			"noworker",
			BIG.width,
			BIG.height,
			options,
			() => gradientRgba(BIG.width, BIG.height),
		);
		expect(frame.width).toBe(BIG.width);
	}, 20_000);
});

describe("cache budget", () => {
	test("the oldest frames are dropped once the cache outgrows 48 MiB; recently used ones stay", async () => {
		const { ditherCached } = await load();
		let reads = 0;
		const frameSide = 1500; // 1500 × 1500 × 4 = 9 MB, so six frames pass the limit
		const make = () => {
			reads++;
			return new Uint8ClampedArray(frameSide * frameSide * 4);
		};
		const cheap: RasterOptions = { ...options, palette: [] };
		await ditherCached("f0", frameSide, frameSide, cheap, make);
		await ditherCached("f1", frameSide, frameSide, cheap, make);
		// Touch f0 so f1 is the least recently used.
		await ditherCached("f0", frameSide, frameSide, cheap, make);
		for (const key of ["f2", "f3", "f4", "f5"]) {
			await ditherCached(key, frameSide, frameSide, cheap, make);
		}
		expect(reads).toBe(6);
		await ditherCached("f0", frameSide, frameSide, cheap, make);
		expect(reads).toBe(6);
		await ditherCached("f1", frameSide, frameSide, cheap, make);
		expect(reads).toBe(7);
	}, 30_000);
});
