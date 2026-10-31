/**
 * Off-main-thread dithering for large images. Loaded by `dither-runner.ts`
 * with `new Worker(new URL("./dither.worker.ts", import.meta.url), { type: "module" })`.
 * One request in, one response out; the pixel buffers are transferred, not copied.
 */

import { ditherRGBA, type RasterOptions } from "./algorithms";

export interface DitherWorkerRequest {
	id: number;
	width: number;
	height: number;
	buffer: ArrayBuffer;
	options: RasterOptions;
}

export type DitherWorkerResponse =
	| { id: number; ok: true; buffer: ArrayBuffer }
	| { id: number; ok: false; error: string };

/** The slice of `DedicatedWorkerGlobalScope` used here; the app compiles against the DOM lib. */
interface WorkerScope {
	onmessage: ((event: MessageEvent<DitherWorkerRequest>) => void) | null;
	postMessage(message: DitherWorkerResponse, transfer: Transferable[]): void;
}

const scope = self as unknown as WorkerScope;

scope.onmessage = (event) => {
	const { id, width, height, buffer, options } = event.data;
	try {
		const out = ditherRGBA(
			new Uint8ClampedArray(buffer),
			width,
			height,
			options,
		);
		scope.postMessage({ id, ok: true, buffer: out.buffer as ArrayBuffer }, [
			out.buffer as ArrayBuffer,
		]);
	} catch (error) {
		scope.postMessage(
			{
				id,
				ok: false,
				error: error instanceof Error ? error.message : String(error),
			},
			[],
		);
	}
};
