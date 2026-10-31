import { cn } from "cn";
import {
	type CSSProperties,
	type ReactNode,
	useEffect,
	useMemo,
	useRef,
	useState,
} from "react";
import type { BayerSize, DitherMatrix } from "../../lib/bayer";
import { useDitherFrame } from "../../lib/motion";
import { type RGB, useDitherPalette } from "../../lib/tokens";
import { fitRect } from "./algorithms";
import { gridFor } from "./grid";
import { useBoxSize, useDevicePixelRatio, useInView } from "./hooks";
import {
	insideMask,
	type LogoEffect,
	type LogoFill,
	type LogoSource,
	logoInk,
	rimMask,
	rowSpan,
	scanLit,
	sweepAt,
} from "./logo-cells";
import type { LogoSource as LogoSourceSpec } from "./logo-source";
import { loadLogo, logoSourceKey } from "./logo-source";
import { resolveColor } from "./look";
import type { CrossOrigin } from "./use-dithered";

export interface DitheredLogoProps {
	/** An SVG or PNG URL. */
	src?: string;
	/** A brand from the host's catalog (`setBrandCatalog`). With neither `src` nor `brand`, our own mark is drawn. */
	brand?: string;
	/** Names the logo, e.g. "PostgreSQL". `""` hides it from assistive technology. */
	alt: string;
	/** Width ÷ height. Default: the file's own ratio, or 1 for sprite brands and our mark. Set it to reserve space before the logo loads. */
	ratio?: number;
	crossOrigin?: CrossOrigin;
	/** Cell edge in CSS px. Larger cells give a chunkier, pixel-art mark. Default 1. */
	cell?: number;
	/** One ink for the whole logo: a palette name (`green`, `ink`…), `accent`, `muted`, a `--token` or any CSS colour. Default `ink`. */
	color?: string;
	/** `mono` (or `ink`) paints everything in `color`; `original` keeps the file's own colours. Default `mono`. */
	tone?: "mono" | "ink" | "original";
	/** Which pixels are the logo: `alpha` for transparent files, `dark` for a dark logo on an opaque light background. Default `alpha`. */
	source?: LogoSource;
	/** What fills the inside: a gradient that thins towards the bottom, solid, dots or hatching. */
	fill?: LogoFill;
	/** Gradient tone at the bottom, 0..1. Default 0.3. */
	floor?: number;
	/** Keep a solid one-cell outline so the shape stays exact. Default true. */
	outline?: boolean;
	/** Threshold pattern of the gradient and the effects: `bayer2`, `bayer4`, `bayer8`, `blue-noise` (or a Bayer size 2, 4, 8). Default `bayer4`. */
	matrix?: DitherMatrix | BayerSize;
	/** `shimmer` sweeps a diagonal band of solid ink across, `scan` runs a bright line down. Runs at 30 fps only on screen; static under reduced motion. */
	effect?: LogoEffect;
	/** Seconds per sweep. Default 6. */
	period?: number;
	/** Where in the sweep this logo starts, 0..1. Offsets logos in a row. Default 0. */
	phase?: number;
	/** Load only when the logo nears the viewport. Turn it off inside a clipped, moving row. Default true. */
	lazy?: boolean;
	className?: string;
	style?: CSSProperties;
}

interface Raster {
	cols: number;
	rows: number;
	inside: Uint8Array;
	/** Outline cells: the silhouette's edge, plus seams between colours. */
	rim: Uint8Array;
	/** Source RGBA, for `tone="original"`. */
	rgba: Uint8ClampedArray;
}

const lighten = (c: number) => Math.round(c + (255 - c) * 0.55);

/**
 * An SVG or PNG logo redrawn on a pixel grid: exact edges (cut at half
 * coverage), a solid outline, and a dithered fill inside, with an optional
 * shimmer or scan line. Set the height with a class; the width follows the
 * logo's ratio. The logo is named by `alt` and the canvas is decoration.
 */
export function DitheredLogo({
	src,
	brand,
	alt,
	ratio,
	crossOrigin,
	cell = 1,
	color = "ink",
	tone = "mono",
	source: cut = "alpha",
	fill = "gradient",
	floor = 0.3,
	outline = true,
	matrix = "bayer4",
	effect = "none",
	period = 6,
	phase = 0,
	lazy = true,
	className,
	style,
}: DitheredLogoProps): ReactNode {
	const hostRef = useRef<HTMLSpanElement | null>(null);
	const canvasRef = useRef<HTMLCanvasElement | null>(null);
	const bufferRef = useRef<Uint8ClampedArray<ArrayBuffer> | null>(null);
	const palette = useDitherPalette();
	const dpr = useDevicePixelRatio();
	const box = useBoxSize(hostRef);
	const seen = useInView(hostRef, { rootMargin: "320px 0px", once: true });
	const load = seen || !lazy;
	const [image, setImage] = useState<HTMLImageElement | null>(null);
	const [failed, setFailed] = useState(false);
	const [raster, setRaster] = useState<Raster | null>(null);
	const [ink, setInk] = useState<RGB>(() => resolveColor(color, palette));

	const logo: LogoSourceSpec = useMemo(
		() =>
			src
				? { kind: "url", src }
				: brand
					? { kind: "brand", slug: brand }
					: { kind: "mark" },
		[src, brand],
	);
	const sourceKey = logoSourceKey(logo);

	// Resolve on the element, so a wrapper that overrides `--dither-*` retints its own logos.
	useEffect(() => {
		setInk(resolveColor(color, palette, hostRef.current ?? undefined));
	}, [color, palette]);

	// biome-ignore lint/correctness/useExhaustiveDependencies: sourceKey is the content of logo; the palette re-colours the mark.
	useEffect(() => {
		if (!load) return;
		let cancelled = false;
		setFailed(false);
		loadLogo(logo, crossOrigin)
			.then((loaded) => !cancelled && setImage(loaded))
			.catch(() => {
				if (cancelled) return;
				setImage(null);
				setFailed(true);
			});
		return () => {
			cancelled = true;
		};
	}, [sourceKey, crossOrigin, palette, load]);

	const naturalRatio =
		image && image.naturalHeight > 0
			? image.naturalWidth / image.naturalHeight
			: 1;
	const aspect = ratio ?? naturalRatio;

	// Draw the logo onto the cell grid and keep its silhouette.
	useEffect(() => {
		if (!image || !box || box.width < 1 || box.height < 1) return;
		const grid = gridFor(box.width, box.height, cell, dpr);
		const scratch = document.createElement("canvas");
		scratch.width = grid.cols;
		scratch.height = grid.rows;
		const ctx = scratch.getContext("2d", { willReadFrequently: true });
		if (!ctx || image.naturalWidth === 0) return;
		ctx.imageSmoothingQuality = "high";
		const r = fitRect(
			image.naturalWidth,
			image.naturalHeight,
			grid.cols,
			grid.rows,
			"contain",
		);
		ctx.drawImage(image, r.sx, r.sy, r.sw, r.sh, r.dx, r.dy, r.dw, r.dh);
		const { data } = ctx.getImageData(0, 0, grid.cols, grid.rows);
		const inside = insideMask(data, 128, cut);
		setRaster({
			cols: grid.cols,
			rows: grid.rows,
			inside,
			rim: rimMask(inside, data, grid.cols, grid.rows),
			rgba: data,
		});
	}, [image, box, cell, dpr, cut]);

	const draw = (sweep: number) => {
		const canvas = canvasRef.current;
		if (!canvas || !raster) return;
		const { cols, rows, inside, rim, rgba } = raster;
		if (canvas.width !== cols) canvas.width = cols;
		if (canvas.height !== rows) canvas.height = rows;
		const ctx = canvas.getContext("2d");
		if (!ctx) return;
		const marks = logoInk(
			inside,
			cols,
			rows,
			{ fill, floor, outline, n: matrix, effect, phase: sweep },
			rim,
		);
		let out = bufferRef.current;
		if (out?.length !== cols * rows * 4) {
			out = new Uint8ClampedArray(cols * rows * 4);
			bufferRef.current = out;
		}
		const span = rowSpan(inside, cols, rows);
		const top = span?.[0] ?? 0;
		const height = Math.max(1, (span?.[1] ?? 0) - top);
		for (let y = 0; y < rows; y += 1) {
			const lit = effect === "scan" && scanLit(y, top, height, sweep);
			for (let x = 0; x < cols; x += 1) {
				const p = y * cols + x;
				const o = p * 4;
				if (!marks[p]) {
					out[o + 3] = 0;
					continue;
				}
				const r = tone === "original" ? (rgba[o] ?? 0) : ink[0];
				const g = tone === "original" ? (rgba[o + 1] ?? 0) : ink[1];
				const b = tone === "original" ? (rgba[o + 2] ?? 0) : ink[2];
				out[o] = lit ? lighten(r) : r;
				out[o + 1] = lit ? lighten(g) : g;
				out[o + 2] = lit ? lighten(b) : b;
				out[o + 3] = 255;
			}
		}
		ctx.putImageData(new ImageData(out, cols, rows), 0, 0);
	};

	// The still frame under reduced motion (time -phase × period) has no band or line in view.
	useDitherFrame((time) => draw(sweepAt(time, period, phase)), {
		fps: 30,
		enabled: effect !== "none" && raster !== null,
		target: hostRef,
		stillTime: -phase * period,
	});
	// biome-ignore lint/correctness/useExhaustiveDependencies: draw closes over the same inputs listed here.
	useEffect(() => {
		draw(effect === "none" ? 0 : sweepAt(-phase * period, period, phase));
	}, [raster, ink, tone, fill, floor, outline, matrix, effect, phase, period]);

	const grid = raster
		? gridFor(box?.width ?? 1, box?.height ?? 1, cell, dpr)
		: null;
	const decorative = alt === "";

	return (
		<span
			ref={hostRef}
			{...(decorative
				? { "aria-hidden": true }
				: { role: "img", "aria-label": alt })}
			data-state={
				failed ? "error" : raster ? "ready" : load ? "loading" : "idle"
			}
			className={cn("relative inline-block shrink-0", className)}
			style={{ aspectRatio: String(aspect), ...style }}
		>
			{raster && grid ? (
				<canvas
					ref={canvasRef}
					className="pointer-events-none absolute top-0 left-0"
					style={{
						width: grid.cssWidth,
						height: grid.cssHeight,
						imageRendering: "pixelated",
					}}
				/>
			) : null}
		</span>
	);
}
