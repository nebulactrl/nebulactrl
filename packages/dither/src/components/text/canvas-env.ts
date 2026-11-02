import { afterEach, beforeEach, spyOn } from "bun:test";

/**
 * Test-only canvas for the text components. happy-dom has no 2D context and no
 * `ImageData`, so `installCanvasEnv` registers hooks that give every canvas a
 * recording fake context, define a minimal `ImageData`, and hand back the
 * knobs a test needs: what the glyph raster reads back (`coverage`), the font
 * metrics, and whether a context exists at all.
 */

interface CanvasCall {
	name: string;
	args: unknown[];
}

interface FakeContext {
	canvas: HTMLCanvasElement;
	calls: CanvasCall[];
	font: string;
	letterSpacing: string;
	textAlign: string;
	textBaseline: string;
	fillStyle: string;
	callsOf: (name: string) => unknown[][];
}

interface FakeImageData {
	data: Uint8ClampedArray;
	width: number;
	height: number;
}

export interface CanvasEnv {
	/** Coverage (0-255) a rasterised glyph reads back at cell `index`. Default: everything covered. */
	coverage: (index: number, cols: number, rows: number) => number;
	/** `measureText` result. `undefined` fields exercise the fallback metrics. */
	metrics: { fontBoundingBoxAscent?: number; fontBoundingBoxDescent?: number };
	/** `false` makes `getContext` return null, like a browser without canvas. */
	available: boolean;
	/** Every context handed out, in creation order. */
	contexts: FakeContext[];
	/** The context of `canvas`, once it asked for one. */
	contextOf: (canvas: HTMLCanvasElement) => FakeContext | undefined;
	/** Every image written with `putImageData`, in order. */
	puts: FakeImageData[];
}

export function installCanvasEnv(): CanvasEnv {
	const env: CanvasEnv = {
		coverage: () => 255,
		metrics: { fontBoundingBoxAscent: 8, fontBoundingBoxDescent: 2 },
		available: true,
		contexts: [],
		contextOf: () => undefined,
		puts: [],
	};
	const realImageData = (globalThis as { ImageData?: unknown }).ImageData;
	let getContext: { mockRestore: () => void } | undefined;

	beforeEach(() => {
		env.coverage = () => 255;
		env.metrics = { fontBoundingBoxAscent: 8, fontBoundingBoxDescent: 2 };
		env.available = true;
		env.contexts = [];
		env.puts = [];
		const byCanvas = new Map<HTMLCanvasElement, FakeContext>();
		env.contextOf = (canvas) => byCanvas.get(canvas);

		class FakeImage implements FakeImageData {
			data: Uint8ClampedArray;
			width: number;
			height: number;
			constructor(data: Uint8ClampedArray, width: number, height: number) {
				this.data = data;
				this.width = width;
				this.height = height;
			}
		}
		(globalThis as { ImageData?: unknown }).ImageData = FakeImage;

		const make = (canvas: HTMLCanvasElement): FakeContext => {
			const calls: CanvasCall[] = [];
			const record =
				(name: string, result?: (...args: unknown[]) => unknown) =>
				(...args: unknown[]) => {
					calls.push({ name, args });
					return result?.(...args);
				};
			const ctx = {
				canvas,
				calls,
				font: "",
				letterSpacing: "",
				textAlign: "",
				textBaseline: "",
				fillStyle: "",
				callsOf: (name: string) =>
					calls.filter((c) => c.name === name).map((c) => c.args),
				clearRect: record("clearRect"),
				setTransform: record("setTransform"),
				fillText: record("fillText"),
				fillRect: record("fillRect"),
				measureText: record("measureText", () => ({ ...env.metrics })),
				getImageData: record("getImageData", (...args) => {
					const [, , w, h] = args as [number, number, number, number];
					const data = new Uint8ClampedArray(w * h * 4);
					for (let i = 0; i < w * h; i += 1) {
						data[i * 4 + 3] = env.coverage(i, w, h);
					}
					return new FakeImage(data, w, h);
				}),
				putImageData: record("putImageData", (...args) => {
					env.puts.push(args[0] as FakeImage);
				}),
			};
			return ctx as unknown as FakeContext;
		};

		getContext = spyOn(
			HTMLCanvasElement.prototype,
			"getContext",
		).mockImplementation(function (this: HTMLCanvasElement) {
			if (!env.available) return null;
			let ctx = byCanvas.get(this);
			if (!ctx) {
				ctx = make(this);
				byCanvas.set(this, ctx);
				env.contexts.push(ctx);
			}
			return ctx as unknown as CanvasRenderingContext2D;
		} as unknown as HTMLCanvasElement["getContext"]);
	});

	afterEach(() => {
		getContext?.mockRestore();
		(globalThis as { ImageData?: unknown }).ImageData = realImageData;
	});

	return env;
}
