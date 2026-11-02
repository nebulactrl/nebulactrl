import { spyOn } from "bun:test";

/**
 * Test helper: happy-dom has no 2D canvas, so `getContext("2d")` returns null.
 * `stubCanvas2d()` gives every canvas a recording context: `createImageData`
 * hands out real pixel buffers, `putImageData` keeps what was drawn, and the
 * text calls (`measureText`, `fillText`, `getImageData`) are scripted per test.
 */

export interface FakeImage {
	width: number;
	height: number;
	data: Uint8ClampedArray;
}

interface FakeContext2d {
	canvas: HTMLCanvasElement;
	font: string;
	fillStyle: string;
	textBaseline: string;
	/** Every `putImageData` image, in order. */
	puts: FakeImage[];
	/** Every `drawImage` call: the source and the numeric arguments after it. */
	draws: { source: unknown; args: number[] }[];
	imageSmoothingEnabled: boolean;
	imageSmoothingQuality: string;
	/** Every `fillText(text, x, y)` call. */
	texts: { text: string; x: number; y: number; font: string }[];
	/** The font strings `measureText` was asked about. */
	measured: string[];
	createImageData(width: number, height: number): FakeImage;
	putImageData(image: FakeImage): void;
	getImageData(x: number, y: number, width: number, height: number): FakeImage;
	measureText(text: string): TextMetrics;
	fillText(text: string, x: number, y: number): void;
	clearRect(): void;
	fillRect(): void;
	drawImage(source: unknown, ...args: number[]): void;
}

export interface CanvasStubOptions {
	/** What `measureText` reports; default a 60×80 box at any font size. */
	metrics?: (text: string, font: string) => Partial<TextMetrics>;
	/** What `getImageData` returns; default fully transparent. */
	imageData?: (width: number, height: number) => Uint8ClampedArray;
	/** `getContext("2d")` returns null: a canvas the browser could not give. */
	unavailable?: boolean;
}

export interface CanvasStub {
	/** The context of one canvas (created on first `getContext`). */
	contextOf(canvas: HTMLCanvasElement): FakeContext2d | undefined;
	/** Every context created so far. */
	all: FakeContext2d[];
	restore(): void;
}

function parseColor(css: string): [number, number, number] {
	const hex = /^#([0-9a-f]{6})$/i.exec(css.trim());
	if (hex?.[1]) {
		const n = Number.parseInt(hex[1], 16);
		return [(n >> 16) & 255, (n >> 8) & 255, n & 255];
	}
	const rgb = /^rgba?\(\s*(\d+)[,\s]+(\d+)[,\s]+(\d+)/i.exec(css.trim());
	if (rgb) return [Number(rgb[1]), Number(rgb[2]), Number(rgb[3])];
	return [128, 128, 128];
}

export function stubCanvas2d(options: CanvasStubOptions = {}): CanvasStub {
	const contexts = new Map<HTMLCanvasElement, FakeContext2d>();
	const all: FakeContext2d[] = [];
	const spy = spyOn(
		HTMLCanvasElement.prototype,
		"getContext",
	).mockImplementation(function (this: HTMLCanvasElement, type: string) {
		if (type !== "2d" || options.unavailable) return null;
		let ctx = contexts.get(this);
		if (ctx) return ctx as unknown as CanvasRenderingContext2D;
		const made: FakeContext2d = {
			canvas: this,
			font: "",
			fillStyle: "",
			textBaseline: "",
			puts: [],
			draws: [],
			imageSmoothingEnabled: false,
			imageSmoothingQuality: "",
			texts: [],
			measured: [],
			createImageData: (width, height) => ({
				width,
				height,
				data: new Uint8ClampedArray(width * height * 4),
			}),
			putImageData: (image) => {
				made.puts.push({
					width: image.width,
					height: image.height,
					data: image.data.slice(),
				});
			},
			getImageData: (_x, _y, width, height) => {
				// A 1×1 read is the colour probe of `toRGB` and friends (module-level
				// canvases that outlive a test): answer it with the last fill colour.
				if (width === 1 && height === 1) {
					const [r, g, b] = parseColor(made.fillStyle);
					return { width, height, data: new Uint8ClampedArray([r, g, b, 255]) };
				}
				return {
					width,
					height,
					data:
						options.imageData?.(width, height) ??
						new Uint8ClampedArray(width * height * 4),
				};
			},
			measureText: (text) => {
				made.measured.push(made.font);
				return {
					width: 60,
					actualBoundingBoxLeft: 0,
					actualBoundingBoxRight: 60,
					actualBoundingBoxAscent: 80,
					actualBoundingBoxDescent: 0,
					...options.metrics?.(text, made.font),
				} as TextMetrics;
			},
			fillText: (text, x, y) => {
				made.texts.push({ text, x, y, font: made.font });
			},
			drawImage: (source, ...args) => {
				made.draws.push({ source, args });
			},
			clearRect: () => {},
			fillRect: () => {},
		};
		ctx = made;
		contexts.set(this, ctx);
		all.push(ctx);
		return ctx as unknown as CanvasRenderingContext2D;
	} as never);
	return {
		contextOf: (canvas) => contexts.get(canvas),
		all,
		restore: () => spy.mockRestore(),
	};
}

/** Count of pixels with any alpha in a drawn image. */
export function inkedPixels(image: FakeImage): number {
	let n = 0;
	for (let i = 3; i < image.data.length; i += 4)
		if ((image.data[i] ?? 0) > 0) n++;
	return n;
}
