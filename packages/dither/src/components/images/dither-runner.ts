/**
 * Runs `ditherRGBA` where it is cheapest: inline for small bitmaps, in a Web
 * Worker above `WORKER_PIXEL_THRESHOLD` pixels, and inline again if the worker
 * is unavailable or fails. Results are cached per src + grid + options in a
 * byte-bounded LRU, and identical requests in flight share one promise.
 */

import { ditherRGBA, type RasterOptions } from "./algorithms";
import type {
	DitherWorkerRequest,
	DitherWorkerResponse,
} from "./dither.worker";

/** Bitmaps with more pixels than this (about 256k) go to the worker. */
const WORKER_PIXEL_THRESHOLD = 256 * 1024;

/** Total bytes of dithered bitmaps the cache keeps before evicting the oldest. */
const CACHE_BYTE_LIMIT = 48 * 1024 * 1024;

function shouldUseWorker(pixels: number, workerSupported: boolean): boolean {
	return workerSupported && pixels > WORKER_PIXEL_THRESHOLD;
}

interface Pending {
	resolve: (buffer: ArrayBuffer) => void;
	reject: (error: Error) => void;
}

let worker: Worker | null = null;
let workerBroken = false;
let nextId = 1;
const pending = new Map<number, Pending>();

function failAll(error: Error) {
	for (const entry of pending.values()) entry.reject(error);
	pending.clear();
}

function getWorker(): Worker | null {
	if (workerBroken || typeof Worker === "undefined") return null;
	if (worker) return worker;
	try {
		const created = new Worker(new URL("./dither.worker.ts", import.meta.url), {
			type: "module",
		});
		created.onmessage = (event: MessageEvent<DitherWorkerResponse>) => {
			const message = event.data;
			const entry = pending.get(message.id);
			if (!entry) return;
			pending.delete(message.id);
			if (message.ok) entry.resolve(message.buffer);
			else entry.reject(new Error(message.error));
		};
		created.onerror = () => {
			workerBroken = true;
			worker?.terminate();
			worker = null;
			failAll(new Error("dither worker failed"));
		};
		worker = created;
		return created;
	} catch {
		workerBroken = true;
		return null;
	}
}

function runInWorker(
	rgba: Uint8ClampedArray,
	width: number,
	height: number,
	options: RasterOptions,
): Promise<Uint8ClampedArray<ArrayBuffer>> | null {
	const target = getWorker();
	if (!target) return null;
	const id = nextId++;
	return new Promise<ArrayBuffer>((resolve, reject) => {
		pending.set(id, { resolve, reject });
		// A copy is transferred so the caller's bitmap survives a fallback.
		const copy = rgba.slice().buffer as ArrayBuffer;
		const request: DitherWorkerRequest = {
			id,
			width,
			height,
			buffer: copy,
			options,
		};
		target.postMessage(request, [copy]);
	}).then((buffer) => new Uint8ClampedArray(buffer));
}

/**
 * Dither a bitmap, picking worker or main thread by size. Falls back to the
 * main thread when the worker cannot be created or errors.
 */
async function ditherPixels(
	rgba: Uint8ClampedArray,
	width: number,
	height: number,
	options: RasterOptions,
): Promise<Uint8ClampedArray<ArrayBuffer>> {
	if (
		shouldUseWorker(
			width * height,
			typeof Worker !== "undefined" && !workerBroken,
		)
	) {
		const viaWorker = runInWorker(rgba, width, height, options);
		if (viaWorker) {
			try {
				return await viaWorker;
			} catch {
				// Fall through to the main thread.
			}
		}
	}
	return ditherRGBA(rgba, width, height, options);
}

export interface RasterFrame {
	width: number;
	height: number;
	/** Dithered RGBA. Shared through the cache: treat as read-only. */
	data: Uint8ClampedArray<ArrayBuffer>;
}

const cache = new Map<string, RasterFrame>();
const inflight = new Map<string, Promise<RasterFrame>>();
let cachedBytes = 0;

function remember(key: string, frame: RasterFrame) {
	const previous = cache.get(key);
	if (previous) cachedBytes -= previous.data.byteLength;
	cache.delete(key);
	cache.set(key, frame);
	cachedBytes += frame.data.byteLength;
	for (const oldest of cache.keys()) {
		if (cachedBytes <= CACHE_BYTE_LIMIT || oldest === key) break;
		cachedBytes -= cache.get(oldest)?.data.byteLength ?? 0;
		cache.delete(oldest);
	}
}

/** A cached frame, refreshed to most-recently-used. */
function cachedFrame(key: string): RasterFrame | undefined {
	const hit = cache.get(key);
	if (hit) {
		cache.delete(key);
		cache.set(key, hit);
	}
	return hit;
}

/**
 * Dither with caching. `source` is only called on a miss, so callers can defer
 * the `getImageData` read until it is needed.
 */
export function ditherCached(
	key: string,
	width: number,
	height: number,
	options: RasterOptions,
	source: () => Uint8ClampedArray,
): Promise<RasterFrame> {
	const hit = cachedFrame(key);
	if (hit) return Promise.resolve(hit);
	const running = inflight.get(key);
	if (running) return running;
	const job = ditherPixels(source(), width, height, options)
		.then((data) => {
			const frame: RasterFrame = { width, height, data };
			remember(key, frame);
			return frame;
		})
		.finally(() => inflight.delete(key));
	inflight.set(key, job);
	return job;
}
