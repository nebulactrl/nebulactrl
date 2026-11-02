import { afterEach, describe, expect, spyOn, test } from "bun:test";
import {
	bitmapToCanvas,
	canvasToBitmap,
	download,
	downloadBlob,
	downloadText,
	exportCSS,
	exportPNG,
	exportSVG,
	toCSS,
	toPNG,
} from "./export";
import { ditherFillId } from "./pattern";

describe("exportSVG backgrounds", () => {
	test("an explicit background fills behind an indexed bitmap", () => {
		const svg = exportSVG(
			{
				kind: "indexed",
				bitmap: { width: 2, height: 1, indices: [0, 1] },
				palette: ["#111111", "#eeeeee"],
			},
			{ background: "#123456" },
		);
		expect(svg).toContain('<rect width="2" height="1" fill="#123456"/>');
		// Both colours are drawn on top: none is folded into the background.
		expect(svg).toContain('<path fill="#111111"');
		expect(svg).toContain('<path fill="#eeeeee"');
	});

	test("an ink mask is one path, with its background only when asked", () => {
		const mask = { width: 3, height: 1, ink: [1, 0, 1] };
		const plain = exportSVG({ kind: "mask", mask, color: "#fff" });
		expect(plain).not.toContain("<rect");
		expect(plain.match(/<path/g)).toHaveLength(1);
		expect(plain).toContain('fill="#fff"');

		const withBackground = exportSVG(
			{ kind: "mask", mask, color: "#fff" },
			{ background: "#000", cell: 4, title: "mask" },
		);
		expect(withBackground).toContain(
			'<rect width="3" height="1" fill="#000"/>',
		);
		expect(withBackground).toContain('width="12" height="4"');
		expect(withBackground).toContain("<title>mask</title>");
		expect(
			exportSVG({ kind: "mask", mask, color: "#fff" }, { background: "auto" }),
		).not.toContain("<rect");
	});

	test("a pattern export defines the pattern and fills a rect with it", () => {
		const fill = { tone: 0.5, color: "#fff", cell: 2 };
		const id = ditherFillId(fill);
		const svg = exportSVG(
			{ kind: "pattern", fill, width: 40, height: 20 },
			{ background: "#222" },
		);
		expect(svg).toContain('width="40" height="20" viewBox="0 0 40 20"');
		expect(svg).toContain(`<pattern id="${id}"`);
		expect(svg).toContain('<rect width="40" height="20" fill="#222"/>');
		expect(svg).toContain(`<rect width="40" height="20" fill="url(#${id})"/>`);
		// The background comes first so the pattern paints over it.
		expect(svg.indexOf('fill="#222"')).toBeLessThan(svg.indexOf("url(#"));
	});
});

/** A 2d context that records what is drawn onto it. */
function recordingContext(size: { w: number; h: number }) {
	const calls: { op: string; args: unknown[] }[] = [];
	const ctx = {
		imageSmoothingEnabled: true,
		drawImage: (...args: unknown[]) => calls.push({ op: "drawImage", args }),
		putImageData: (...args: unknown[]) =>
			calls.push({ op: "putImageData", args }),
		getImageData: (...args: unknown[]) => {
			calls.push({ op: "getImageData", args });
			return {
				width: size.w,
				height: size.h,
				data: new Uint8ClampedArray(size.w * size.h * 4).fill(7),
			};
		},
	};
	return { ctx, calls };
}

const restores: (() => void)[] = [];
function track<T extends { mockRestore(): void }>(spy: T): T {
	restores.push(() => spy.mockRestore());
	return spy;
}
afterEach(() => {
	for (const restore of restores.splice(0)) restore();
});

describe("bitmapToCanvas / canvasToBitmap", () => {
	test("bitmapToCanvas sizes a canvas to the buffer and puts the pixels on it", () => {
		const { ctx, calls } = recordingContext({ w: 2, h: 1 });
		track(spyOn(HTMLCanvasElement.prototype, "getContext")).mockImplementation(
			(() => ctx) as never,
		);

		const canvas = bitmapToCanvas({
			width: 2,
			height: 1,
			data: [1, 2, 3, 255, 4, 5, 6, 255],
		});
		expect(canvas.width).toBe(2);
		expect(canvas.height).toBe(1);
		const put = calls.find((c) => c.op === "putImageData");
		const image = put?.args[0] as ImageData;
		expect(Array.from(image.data)).toEqual([1, 2, 3, 255, 4, 5, 6, 255]);
		expect(put?.args.slice(1)).toEqual([0, 0]);
	});

	test("bitmapToCanvas throws without a 2d context", () => {
		track(spyOn(HTMLCanvasElement.prototype, "getContext")).mockImplementation(
			(() => null) as never,
		);
		expect(() =>
			bitmapToCanvas({ width: 1, height: 1, data: [0, 0, 0, 0] }),
		).toThrow("2D canvas is unavailable");
	});

	test("canvasToBitmap reads the whole canvas back", () => {
		const { ctx, calls } = recordingContext({ w: 3, h: 2 });
		const canvas = {
			width: 3,
			height: 2,
			getContext: () => ctx,
		} as unknown as OffscreenCanvas;
		const bitmap = canvasToBitmap(canvas);
		expect(calls[0]).toEqual({ op: "getImageData", args: [0, 0, 3, 2] });
		expect(bitmap.width).toBe(3);
		expect(bitmap.height).toBe(2);
		expect(bitmap.data).toHaveLength(24);
		expect(bitmap.data[0]).toBe(7);
	});

	test("canvasToBitmap throws without a 2d context", () => {
		const canvas = {
			width: 1,
			height: 1,
			getContext: () => null,
		} as unknown as OffscreenCanvas;
		expect(() => canvasToBitmap(canvas)).toThrow("2D canvas is unavailable");
	});
});

describe("downloads", () => {
	function stubDownloadEnv() {
		const created: Blob[] = [];
		const revoked: string[] = [];
		track(spyOn(URL, "createObjectURL")).mockImplementation(((blob: Blob) => {
			created.push(blob);
			return `blob:test/${created.length}`;
		}) as never);
		track(spyOn(URL, "revokeObjectURL")).mockImplementation(((url: string) => {
			revoked.push(url);
		}) as never);
		const clicks: { href: string; download: string; inDom: boolean }[] = [];
		track(spyOn(HTMLAnchorElement.prototype, "click")).mockImplementation(
			function (this: HTMLAnchorElement) {
				clicks.push({
					href: this.href,
					download: this.download,
					inDom: document.body.contains(this),
				});
			} as never,
		);
		const timers: { fn: () => void; ms: number }[] = [];
		track(spyOn(globalThis, "setTimeout")).mockImplementation(((
			fn: () => void,
			ms: number,
		) => {
			timers.push({ fn, ms });
			return 0;
		}) as never);
		return { created, revoked, clicks, timers };
	}

	test("downloadBlob clicks a temporary link, removes it and revokes the URL later", () => {
		const env = stubDownloadEnv();
		const blob = new Blob(["x"]);
		downloadBlob(blob, "a.png");

		expect(env.created).toEqual([blob]);
		expect(env.clicks).toEqual([
			{ href: "blob:test/1", download: "a.png", inDom: true },
		]);
		expect(document.body.querySelector("a")).toBeNull();
		// Revocation waits, so the browser can start the download first.
		expect(env.revoked).toEqual([]);
		expect(env.timers).toHaveLength(1);
		expect(env.timers[0]?.ms).toBe(1000);
		env.timers[0]?.fn();
		expect(env.revoked).toEqual(["blob:test/1"]);
	});

	test("downloadText wraps the text in a blob of the given type", async () => {
		const env = stubDownloadEnv();
		downloadText("<svg/>", "x.svg", "image/svg+xml");
		expect(env.clicks[0]?.download).toBe("x.svg");
		const blob = env.created[0];
		expect(blob?.type).toBe("image/svg+xml");
		expect(await blob?.text()).toBe("<svg/>");
	});

	test("downloadText defaults to text/plain", () => {
		const env = stubDownloadEnv();
		downloadText("hi", "x.txt");
		expect(env.created[0]?.type).toBe("text/plain");
	});

	test("download sends a string as text and a Blob as is", async () => {
		const env = stubDownloadEnv();
		download({ data: "body { }", filename: "a.css", type: "text/css" });
		const blob = new Blob(["png"], { type: "image/png" });
		download({ data: blob, filename: "a.png" });

		expect(env.clicks.map((c) => c.download)).toEqual(["a.css", "a.png"]);
		expect(env.created[0]?.type).toBe("text/css");
		expect(await env.created[0]?.text()).toBe("body { }");
		expect(env.created[1]).toBe(blob);
	});
});

describe("exportPNG", () => {
	function realCanvas(width: number, height: number) {
		const canvas = document.createElement("canvas");
		canvas.width = width;
		canvas.height = height;
		return canvas;
	}

	test("encodes a canvas as image/png without scaling", async () => {
		const canvas = realCanvas(4, 3);
		const requested: (string | undefined)[] = [];
		const png = new Blob(["png"], { type: "image/png" });
		track(spyOn(canvas, "toBlob")).mockImplementation(((
			cb: BlobCallback,
			type?: string,
		) => {
			requested.push(type);
			cb(png);
		}) as never);
		expect(await exportPNG(canvas)).toBe(png);
		expect(requested).toEqual(["image/png"]);
	});

	test("scale draws a nearest-neighbour copy at whole multiples", async () => {
		const canvas = realCanvas(4, 3);
		const { ctx, calls } = recordingContext({ w: 1, h: 1 });
		let scaled: HTMLCanvasElement | null = null;
		track(spyOn(HTMLCanvasElement.prototype, "getContext")).mockImplementation(
			function (this: HTMLCanvasElement) {
				scaled = this;
				return ctx;
			} as never,
		);
		const png = new Blob(["png"]);
		track(spyOn(HTMLCanvasElement.prototype, "toBlob")).mockImplementation(
			function (this: HTMLCanvasElement, cb: BlobCallback) {
				// The blob must come from the upscaled canvas, not the original.
				expect(this).toBe(scaled as unknown as HTMLCanvasElement);
				cb(png);
			} as never,
		);

		expect(await exportPNG(canvas, { scale: 2.6 })).toBe(png);
		expect((scaled as unknown as HTMLCanvasElement).width).toBe(12);
		expect((scaled as unknown as HTMLCanvasElement).height).toBe(9);
		expect(ctx.imageSmoothingEnabled).toBe(false);
		expect(calls[0]).toEqual({
			op: "drawImage",
			args: [canvas, 0, 0, 12, 9],
		});
	});

	test("a scale below 1 is treated as 1", async () => {
		const canvas = realCanvas(2, 2);
		const getContext = track(spyOn(HTMLCanvasElement.prototype, "getContext"));
		const png = new Blob(["png"]);
		track(spyOn(canvas, "toBlob")).mockImplementation(((cb: BlobCallback) =>
			cb(png)) as never);
		expect(await exportPNG(canvas, { scale: 0.2 })).toBe(png);
		expect(getContext).not.toHaveBeenCalled();
	});

	test("rejects when the upscale canvas has no 2d context", async () => {
		track(spyOn(HTMLCanvasElement.prototype, "getContext")).mockImplementation(
			(() => null) as never,
		);
		await expect(exportPNG(realCanvas(2, 2), { scale: 2 })).rejects.toThrow(
			"2D canvas is unavailable",
		);
	});

	test("rejects when the canvas cannot be encoded", async () => {
		const canvas = realCanvas(2, 2);
		track(spyOn(canvas, "toBlob")).mockImplementation(((cb: BlobCallback) =>
			cb(null)) as never);
		await expect(exportPNG(canvas)).rejects.toThrow(
			"Canvas could not be encoded as PNG",
		);
	});

	test("an OffscreenCanvas is encoded through convertToBlob", async () => {
		const png = new Blob(["png"]);
		const types: unknown[] = [];
		const offscreen = {
			width: 2,
			height: 2,
			convertToBlob: async (opts: { type: string }) => {
				types.push(opts.type);
				return png;
			},
		} as unknown as OffscreenCanvas;
		expect(await exportPNG(offscreen)).toBe(png);
		expect(types).toEqual(["image/png"]);
	});

	test("a filename also downloads the PNG", async () => {
		const png = new Blob(["png"]);
		const offscreen = {
			width: 1,
			height: 1,
			convertToBlob: async () => png,
		} as unknown as OffscreenCanvas;
		const created: Blob[] = [];
		track(spyOn(URL, "createObjectURL")).mockImplementation(((b: Blob) => {
			created.push(b);
			return "blob:x";
		}) as never);
		const names: string[] = [];
		track(spyOn(HTMLAnchorElement.prototype, "click")).mockImplementation(
			function (this: HTMLAnchorElement) {
				names.push(this.download);
			} as never,
		);
		track(spyOn(globalThis, "setTimeout")).mockImplementation(
			(() => 0) as never,
		);

		expect(await exportPNG(offscreen, { filename: "dither.png" })).toBe(png);
		expect(names).toEqual(["dither.png"]);
		expect(created).toEqual([png]);
	});
});

describe("toPNG", () => {
	test("a pixel buffer is drawn to a canvas and upscaled by the requested scale", async () => {
		const { ctx, calls } = recordingContext({ w: 1, h: 1 });
		const sizes: [number, number][] = [];
		track(spyOn(HTMLCanvasElement.prototype, "getContext")).mockImplementation(
			(() => ctx) as never,
		);
		const png = new Blob(["png"]);
		track(spyOn(HTMLCanvasElement.prototype, "toBlob")).mockImplementation(
			function (this: HTMLCanvasElement, cb: BlobCallback) {
				sizes.push([this.width, this.height]);
				cb(png);
			} as never,
		);

		const blob = await toPNG(
			{ width: 2, height: 1, data: [0, 0, 0, 255, 255, 255, 255, 255] },
			{ scale: 5 },
		);
		expect(blob).toBe(png);
		expect(calls.map((c) => c.op)).toEqual(["putImageData", "drawImage"]);
		expect(sizes).toEqual([[10, 5]]);
	});

	test("the scale is capped so the longest side stays within 16384 px", async () => {
		const canvas = {
			width: 20000,
			height: 10,
			getContext: () => null,
			convertToBlob: async () => new Blob(["png"]),
		} as unknown as OffscreenCanvas;
		// A scale of 4 would exceed the cap, so it drops to 1: no upscale canvas
		// (which would need a context) is created.
		const blob = await toPNG(canvas, { scale: 4 });
		expect(blob).toBeInstanceOf(Blob);
	});
});

describe("toCSS image", () => {
	const image = {
		width: 2,
		height: 1,
		data: [0, 0, 0, 255, 255, 255, 255, 255],
	};

	test("png format embeds the canvas data URL as a covering background", () => {
		const { ctx } = recordingContext({ w: 2, h: 1 });
		track(spyOn(HTMLCanvasElement.prototype, "getContext")).mockImplementation(
			(() => ctx) as never,
		);
		track(spyOn(HTMLCanvasElement.prototype, "toDataURL")).mockImplementation(
			(() => "data:image/png;base64,AAAA") as never,
		);

		const css = toCSS({ kind: "image", image, selector: ".hero" });
		expect(css).toBe(
			'.hero {\n\tbackground-image: url("data:image/png;base64,AAAA");\n\tbackground-size: cover;\n\tbackground-repeat: no-repeat;\n\timage-rendering: pixelated;\n}',
		);
	});

	test("svg format needs no canvas and sizes the rule to cover", () => {
		const css = toCSS({ kind: "image", image, format: "svg" });
		expect(css.startsWith(".dither {")).toBe(true);
		expect(css).toContain('background-image: url("data:image/svg+xml,');
		expect(css).toContain("background-size: cover;");
		expect(css).toContain("image-rendering: pixelated;");
	});
});

describe("exportSVG pattern without a background", () => {
	test("is just the pattern and one covering rect", () => {
		const fill = { tone: 0.25, color: "#fff" };
		const id = ditherFillId(fill);
		const svg = exportSVG({ kind: "pattern", fill, width: 8, height: 8 });
		expect(svg.match(/<rect/g)).toHaveLength(1);
		expect(svg).toContain(`fill="url(#${id})"`);
		expect(svg).not.toContain("<title>");
	});
});

describe("exportCSS tile size", () => {
	test("a fill with a Bayer size and no matrix sizes the tile n × cell", () => {
		const css = exportCSS({
			kind: "tile",
			fill: { tone: 0.5, color: "#fff", n: 8, cell: 2 },
		});
		expect(css).toContain("background-size: 16px 16px;");
	});

	test("without a size or matrix the tile is 4 cells across", () => {
		const css = exportCSS({
			kind: "tile",
			fill: { tone: 0.5, color: "#fff", cell: 5 },
		});
		expect(css).toContain("background-size: 20px 20px;");
	});

	test("a blue-noise matrix uses its own, larger tile", () => {
		const css = exportCSS({
			kind: "tile",
			fill: { tone: 0.5, color: "#fff", matrix: "blue-noise", cell: 1 },
		});
		expect(css).toContain("background-size: 16px 16px;");
	});
});
