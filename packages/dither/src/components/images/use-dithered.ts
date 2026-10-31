import { type RefObject, useEffect, useMemo, useState } from "react";
import { useDitherPalette } from "../../lib/tokens";
import { fitRect, rasterCacheKey } from "./algorithms";
import { ditherCached, type RasterFrame } from "./dither-runner";
import { effectiveCell, gridFor, halvingSizes, readCellVar } from "./grid";
import { useBoxSize, useDevicePixelRatio } from "./hooks";
import { type DitherLook, resolveLook } from "./look";

export type CrossOrigin = "anonymous" | "use-credentials";

const images = new Map<string, Promise<HTMLImageElement>>();

/** Load and decode an image once per src + crossOrigin. Rejects on failure. */
export function loadImage(
	src: string,
	crossOrigin?: CrossOrigin,
): Promise<HTMLImageElement> {
	const key = `${crossOrigin ?? ""}|${src}`;
	const hit = images.get(key);
	if (hit) return hit;
	const promise = new Promise<HTMLImageElement>((resolve, reject) => {
		const image = new Image();
		if (crossOrigin) image.crossOrigin = crossOrigin;
		image.decoding = "async";
		image.onload = () => resolve(image);
		image.onerror = () => reject(new Error(`Could not load ${src}`));
		image.src = src;
	});
	promise.catch(() => images.delete(key));
	images.set(key, promise);
	return promise;
}

/**
 * The natural size of `src`, once it has loaded (null before, and while
 * `enabled` is false). Shares the image cache of `loadImage`.
 */
export function useNaturalSize(
	src: string,
	enabled: boolean,
	crossOrigin?: CrossOrigin,
): { width: number; height: number } | null {
	const [size, setSize] = useState<{
		src: string;
		width: number;
		height: number;
	} | null>(null);
	useEffect(() => {
		if (!enabled) return;
		let cancelled = false;
		loadImage(src, crossOrigin)
			.then((image) => {
				if (cancelled) return;
				setSize({
					src,
					width: image.naturalWidth,
					height: image.naturalHeight,
				});
			})
			.catch(() => {});
		return () => {
			cancelled = true;
		};
	}, [src, enabled, crossOrigin]);
	return size?.src === src ? size : null;
}

/** Paint a dithered frame onto a canvas, resizing the canvas to the frame's grid. */
export function paintFrame(
	canvas: HTMLCanvasElement,
	frame: RasterFrame,
): void {
	if (canvas.width !== frame.width) canvas.width = frame.width;
	if (canvas.height !== frame.height) canvas.height = frame.height;
	const ctx = canvas.getContext("2d");
	if (!ctx) return;
	ctx.putImageData(new ImageData(frame.data, frame.width, frame.height), 0, 0);
}

export interface UseDitheredOptions {
	src: string;
	crossOrigin?: CrossOrigin | undefined;
	look: DitherLook;
	/** The element whose box the image fills. */
	boxRef: RefObject<Element | null>;
	/**
	 * Cell multipliers to compute, e.g. `[16, 8, 4, 2, 1]` for a coarse-to-fine
	 * reveal. The result `frames[i]` matches `multipliers[i]`. Default `[1]`.
	 */
	multipliers?: readonly number[];
	/** Skip all work while false (not yet on screen, say). Default true. */
	enabled?: boolean;
}

export interface Dithered {
	status: "idle" | "loading" | "ready" | "error";
	/** One frame per multiplier, finest last when `multipliers` is descending. */
	frames: readonly RasterFrame[];
	/** CSS px size of the canvas for the base (×1) grid. */
	cssSize: { width: number; height: number } | null;
	/** Natural size of the source image. */
	natural: { width: number; height: number } | null;
}

const ONE: readonly number[] = [1];
const IDLE: Dithered = {
	status: "idle",
	frames: [],
	cssSize: null,
	natural: null,
};

function makeCanvas(width: number, height: number): HTMLCanvasElement {
	const canvas = document.createElement("canvas");
	canvas.width = width;
	canvas.height = height;
	return canvas;
}

/**
 * Draw `image` into a `cols × rows` buffer, one pixel per cell, fitted like
 * `object-fit`. Large downscales halve in steps first, which keeps fine
 * detail from aliasing into noise.
 */
export function rasterize(
	image: HTMLImageElement,
	cols: number,
	rows: number,
	fit: "cover" | "contain",
): Uint8ClampedArray {
	const r = fitRect(image.naturalWidth, image.naturalHeight, cols, rows, fit);
	let source: CanvasImageSource = image;
	let { sx, sy, sw, sh } = r;
	// The cropped source area is what has to shrink to the destination.
	for (const step of halvingSizes(sw, sh, r.dw, r.dh)) {
		const half = makeCanvas(step.width, step.height);
		const ctx = half.getContext("2d");
		if (!ctx) break;
		ctx.imageSmoothingQuality = "high";
		ctx.drawImage(source, sx, sy, sw, sh, 0, 0, step.width, step.height);
		source = half;
		sx = 0;
		sy = 0;
		sw = step.width;
		sh = step.height;
	}
	const ctx = makeCanvas(cols, rows).getContext("2d", {
		willReadFrequently: true,
	});
	if (!ctx) throw new Error("2D canvas is unavailable");
	ctx.imageSmoothingEnabled = true;
	ctx.imageSmoothingQuality = "high";
	ctx.drawImage(source, sx, sy, sw, sh, r.dx, r.dy, r.dw, r.dh);
	return ctx.getImageData(0, 0, cols, rows).data;
}

/**
 * Load `src`, fit it to the box (cover or contain), and dither it onto the
 * cell grid with the look, once per multiplier. Re-runs when the box, device
 * pixel ratio, theme palette or look changes; results are cached per
 * src + grid + options, and big grids run in a worker.
 */
export function useDithered(options: UseDitheredOptions): Dithered {
	const { src, crossOrigin, look, boxRef, enabled = true } = options;

	const multipliers = options.multipliers ?? ONE;
	const palette = useDitherPalette();
	const dpr = useDevicePixelRatio();
	const box = useBoxSize(boxRef);
	// The element is null on the first render; once the box is measured it is
	// there, and wrapper overrides of `--dither-*` retint this image only.
	const candidate = resolveLook(look, palette, boxRef.current ?? undefined);
	// `look` is usually an inline object: key on its contents, not its identity.
	const lookKey = `${rasterCacheKey("", 0, 0, candidate.fit, candidate.raster)}|${candidate.cell ?? ""}|${candidate.minColumns}`;
	// biome-ignore lint/correctness/useExhaustiveDependencies: lookKey is the content of candidate.
	const resolved = useMemo(() => candidate, [lookKey]);
	const [state, setState] = useState<Dithered>(IDLE);
	const multiplierKey = multipliers.join(",");

	useEffect(() => {
		if (!enabled || !box || box.width < 1 || box.height < 1) return;
		let cancelled = false;
		setState((prev) =>
			prev.status === "ready" ? prev : { ...IDLE, status: "loading" },
		);

		(async () => {
			const image = await loadImage(src, crossOrigin);
			const cell = effectiveCell(
				resolved.cell ?? readCellVar(),
				box.width,
				resolved.minColumns,
			);
			const frames: RasterFrame[] = [];
			let cssSize: Dithered["cssSize"] = null;
			for (const multiplier of multiplierKey.split(",").map(Number)) {
				const grid = gridFor(box.width, box.height, cell * multiplier, dpr);
				if (multiplier === 1 || cssSize === null) {
					cssSize = { width: grid.cssWidth, height: grid.cssHeight };
				}
				const key = rasterCacheKey(
					src,
					grid.cols,
					grid.rows,
					resolved.fit,
					resolved.raster,
				);
				const frame = await ditherCached(
					key,
					grid.cols,
					grid.rows,
					resolved.raster,
					() => rasterize(image, grid.cols, grid.rows, resolved.fit),
				);
				if (cancelled) return;
				frames.push(frame);
			}
			setState({
				status: "ready",
				frames,
				cssSize,
				natural: { width: image.naturalWidth, height: image.naturalHeight },
			});
		})().catch(() => {
			if (!cancelled) setState({ ...IDLE, status: "error" });
		});

		return () => {
			cancelled = true;
		};
	}, [src, crossOrigin, resolved, box, dpr, multiplierKey, enabled]);

	return state;
}
