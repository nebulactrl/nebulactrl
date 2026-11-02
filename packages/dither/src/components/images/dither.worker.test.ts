import { afterAll, afterEach, describe, expect, spyOn, test } from "bun:test";
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

// The worker module installs `self.onmessage` when it is loaded; in the DOM
// `self` is the window, so the handler is exercised here without a thread.
type Scope = {
	onmessage: ((event: MessageEvent<DitherWorkerRequest>) => void) | null;
	postMessage: (
		message: DitherWorkerResponse,
		transfer: Transferable[],
	) => void;
};
const scope = globalThis as unknown as Scope;
const previous = scope.onmessage;
let posted: Array<{ message: DitherWorkerResponse; transfer: Transferable[] }> =
	[];
const WORKER: string = "./dither.worker.ts?worker";
const post = spyOn(scope, "postMessage").mockImplementation(((
	message: DitherWorkerResponse,
	transfer: Transferable[],
) => {
	posted.push({ message, transfer });
}) as never);

await import(WORKER);
const handler = scope.onmessage as NonNullable<Scope["onmessage"]>;

afterEach(() => {
	posted = [];
});

const options: RasterOptions = {
	...DEFAULT_RASTER_OPTIONS,
	algorithm: "bayer",
};
const send = (request: DitherWorkerRequest) =>
	handler({ data: request } as MessageEvent<DitherWorkerRequest>);

describe("dither worker", () => {
	test("installs a message handler", () => {
		expect(typeof handler).toBe("function");
		expect(handler).not.toBe(previous);
	});

	test("dithers the buffer it receives and transfers the result back under the same id", () => {
		const width = 12;
		const height = 9;
		const source = gradientRgba(width, height);
		send({
			id: 7,
			width,
			height,
			buffer: source.slice().buffer as ArrayBuffer,
			options,
		});
		expect(posted).toHaveLength(1);
		const { message, transfer } = posted[0] ?? {};
		expect(message?.id).toBe(7);
		expect(message?.ok).toBe(true);
		if (!message?.ok) throw new Error("expected a success response");
		expect(transfer).toEqual([message.buffer]);
		expect(Array.from(new Uint8ClampedArray(message.buffer))).toEqual(
			Array.from(ditherRGBA(source, width, height, options)),
		);
	});

	test("answers each request with its own id", () => {
		for (const id of [1, 2, 3]) {
			send({ id, width: 2, height: 2, buffer: new ArrayBuffer(16), options });
		}
		expect(posted.map((p) => p.message.id)).toEqual([1, 2, 3]);
	});

	test("reports a failure as a message instead of throwing, and transfers nothing", () => {
		const broken = { ...options, palette: null } as unknown as RasterOptions;
		expect(() =>
			send({
				id: 4,
				width: 4,
				height: 4,
				buffer: new ArrayBuffer(64),
				options: broken,
			}),
		).not.toThrow();
		const { message, transfer } = posted[0] ?? {};
		expect(message?.id).toBe(4);
		expect(message?.ok).toBe(false);
		if (message && !message.ok) expect(message.error.length).toBeGreaterThan(0);
		expect(transfer).toEqual([]);
	});

	test("a thrown non-Error is reported as its string", () => {
		const throwing = {
			...options,
			get palette(): never {
				throw "plain string";
			},
		} as unknown as RasterOptions;
		send({
			id: 5,
			width: 1,
			height: 1,
			buffer: new ArrayBuffer(4),
			options: throwing,
		});
		const { message } = posted[0] ?? {};
		expect(message?.ok).toBe(false);
		if (message && !message.ok) expect(message.error).toBe("plain string");
	});
});

afterAll(() => {
	post.mockRestore();
	scope.onmessage = previous;
});
