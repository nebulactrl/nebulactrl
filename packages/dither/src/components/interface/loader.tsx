import { cn } from "cn";
import * as React from "react";
import { type DitherMatrix, ditherOn } from "../../lib/bayer";
import { useDitherFrame } from "../../lib/motion";
import {
	bloomFilter,
	cellInk,
	type DitherBloom,
	type DitherColor,
	inkCss,
} from "./fill";
import { useInkRgb } from "./ink";
import {
	LOADER_STILL,
	type LoaderShape,
	type LoaderSize,
	loaderCell,
	loaderGrid,
	loaderHeight,
	loaderTone,
} from "./loader-math";

const GRID = 4;
const CELL_MS = 1200;

/** A grid of cells lit in a diagonal wave, or at rest as a dithered gradient. */
function CellGrid({
	size,
	cells,
	matrix,
	speed,
}: {
	size: number;
	cells: number;
	matrix: DitherMatrix | undefined;
	speed: number;
}) {
	const cell = size / cells;
	const grid: React.ReactNode[] = [];
	for (let y = 0; y < cells; y++) {
		for (let x = 0; x < cells; x++) {
			const diagonal = x + y;
			// At rest (reduced motion) the grid is a dithered diagonal gradient.
			const restTone = 1 - diagonal / (cells * 2 - 2);
			const tone = restTone * 0.9 + 0.05;
			const lit = matrix
				? cellInk("gradient", tone, x, y, matrix) > 0
				: ditherOn(tone, x, y, 4);
			grid.push(
				<rect
					key={`${x}-${y}`}
					x={x * cell}
					y={y * cell}
					width={cell - 0.75}
					height={cell - 0.75}
					rx={0.5}
					fill="currentColor"
					className="animate-dither-cell motion-reduce:animate-none"
					style={{
						animationDuration: `${CELL_MS / speed}ms`,
						animationDelay: `${(diagonal * 110 * GRID) / cells / speed}ms`,
						opacity: lit ? 1 : 0.14,
					}}
				/>,
			);
		}
	}
	return (
		<svg
			aria-hidden
			width={size}
			height={size}
			viewBox={`0 0 ${size} ${size}`}
			className="shrink-0"
		>
			{grid}
		</svg>
	);
}

export interface DitherLoaderProps {
	/**
	 * `inline` sits in a line of text or a button; `page` fills its parent.
	 * `ring`, `dots`, `pulse` and `bar` are drawn loaders: a comet on a track,
	 * three discs that dissolve in turn, a beating dot with ripples, a sweeping bar.
	 */
	variant?: "inline" | "page" | LoaderShape;
	/** Spoken label, and the visible caption of the page loader. */
	label?: string;
	/**
	 * Inline edge in px (default 16), or the height of a drawn loader: `sm`,
	 * `md` (default) and `lg` are 24, 40 and 64 px.
	 */
	size?: number | LoaderSize;
	/** A palette name, a custom property or any CSS colour. Default: the accent ink. */
	color?: DitherColor | undefined;
	/** A soft glow in the loader's colour. */
	bloom?: DitherBloom | undefined;
	/** The threshold pattern. Default `bayer4`. */
	matrix?: DitherMatrix | undefined;
	/** Size of one dither cell in CSS px. Drawn loaders: 2 (1 at `sm`). */
	cell?: number | undefined;
	/** Speed multiplier. Default 1. */
	speed?: number | undefined;
	className?: string | undefined;
}

/**
 * Loading, in dither. Inline it is a 4×4 block whose cells light in a
 * diagonal wave (a still dithered gradient under reduced motion). The page
 * variant fills its parent with the same block and a caption beneath it: no
 * shader and no WebGL context, so a route that is still loading costs
 * nothing. The drawn variants are small 2D canvases on the shared clock: they
 * never take a WebGL context, pause off-screen and hold one still frame
 * under reduced motion.
 */
function DitherLoader({
	variant = "inline",
	label = "Loading",
	size,
	color,
	bloom,
	matrix,
	cell,
	speed = 1,
	className,
}: DitherLoaderProps) {
	if (variant === "page") {
		return (
			<PageLoader
				label={label}
				color={color}
				bloom={bloom}
				matrix={matrix}
				speed={speed}
				className={className}
			/>
		);
	}
	if (variant !== "inline") {
		return (
			<ShapeLoader
				shape={variant}
				size={size ?? "md"}
				label={label}
				color={color}
				bloom={bloom}
				matrix={matrix}
				cell={cell}
				speed={speed}
				className={className}
			/>
		);
	}
	const edge = typeof size === "string" ? loaderHeight(size) : (size ?? 16);
	const hue = color === undefined ? undefined : inkCss(color);
	const glow = hue
		? bloomFilter(hue, bloom)
		: bloomFilter("var(--dither-ink)", bloom);
	return (
		<span
			role="status"
			data-slot="dither-loader"
			className={cn(
				"inline-flex shrink-0",
				hue ? "" : "text-accent-ink",
				className,
			)}
			style={{
				...(hue ? { color: hue } : null),
				...(glow ? { filter: glow } : null),
			}}
		>
			<CellGrid
				size={edge}
				cells={cell ? Math.max(2, Math.round(edge / cell)) : GRID}
				matrix={matrix}
				speed={speed}
			/>
			<span className="sr-only">{label}</span>
		</span>
	);
}

interface ShapeLoaderProps {
	shape: LoaderShape;
	size: number | LoaderSize;
	label: string;
	color: DitherColor | undefined;
	bloom: DitherBloom | undefined;
	matrix: DitherMatrix | undefined;
	cell: number | undefined;
	speed: number;
	className: string | undefined;
}

/** A drawn loader: one pixel per cell on a canvas, scaled up crisp, redrawn on the shared clock. */
function ShapeLoader({
	shape,
	size,
	label,
	color,
	bloom,
	matrix,
	cell,
	speed,
	className,
}: ShapeLoaderProps) {
	const rootRef = React.useRef<HTMLSpanElement>(null);
	const canvasRef = React.useRef<HTMLCanvasElement>(null);
	const timeRef = React.useRef(LOADER_STILL[shape]);
	const [r, g, b] = useInkRgb(color);
	const cellPx = loaderCell(size, cell);
	const { cols, rows } = loaderGrid(shape, loaderHeight(size), cellPx);
	const mode = matrix ?? "bayer4";

	const draw = React.useCallback(
		(time: number) => {
			const ctx = canvasRef.current?.getContext("2d");
			if (!ctx) return;
			const image = ctx.createImageData(cols, rows);
			const t = time * speed;
			for (let y = 0; y < rows; y++) {
				for (let x = 0; x < cols; x++) {
					const tone = loaderTone(shape, (x + 0.5) / rows, (y + 0.5) / rows, t);
					if (tone < 0 || cellInk("gradient", tone, x, y, mode) === 0) continue;
					const at = (y * cols + x) * 4;
					image.data[at] = r;
					image.data[at + 1] = g;
					image.data[at + 2] = b;
					image.data[at + 3] = 255;
				}
			}
			ctx.putImageData(image, 0, 0);
		},
		[shape, cols, rows, speed, mode, r, g, b],
	);

	useDitherFrame(
		(time) => {
			timeRef.current = time;
			draw(time);
		},
		{ fps: 20, target: rootRef, stillTime: LOADER_STILL[shape] },
	);
	// A prop change (colour, matrix, size) repaints the frame already on screen.
	React.useLayoutEffect(() => {
		draw(timeRef.current);
	}, [draw]);

	const hue = inkCss(color);
	const glow = bloomFilter(hue, bloom);
	return (
		<span
			ref={rootRef}
			role="status"
			data-slot="dither-loader"
			data-variant={shape}
			className={cn("inline-flex shrink-0", className)}
		>
			<canvas
				ref={canvasRef}
				aria-hidden
				width={cols}
				height={rows}
				style={{
					width: cols * cellPx,
					height: rows * cellPx,
					imageRendering: "pixelated",
					...(glow ? { filter: glow } : null),
				}}
			/>
			<span className="sr-only">{label}</span>
		</span>
	);
}

/** The page loader: the cell wave and a caption, centred. No shader, no GL context. */
function PageLoader({
	label,
	color,
	bloom,
	matrix,
	speed,
	className,
}: {
	label: string;
	color: DitherColor | undefined;
	bloom: DitherBloom | undefined;
	matrix: DitherMatrix | undefined;
	speed: number;
	className?: string | undefined;
}) {
	const hue = color === undefined ? undefined : inkCss(color);
	const glow = hue
		? bloomFilter(hue, bloom)
		: bloomFilter("var(--dither-ink)", bloom);
	return (
		<div
			role="status"
			data-slot="dither-loader"
			data-variant="page"
			className={cn(
				"flex min-h-full w-full flex-1 flex-col items-center justify-center gap-3",
				hue ? "" : "text-accent-ink",
				className,
			)}
			style={hue ? { color: hue } : undefined}
		>
			<span style={glow ? { filter: glow } : undefined}>
				<CellGrid size={28} cells={GRID} matrix={matrix} speed={speed} />
			</span>
			<span className="font-mono text-2xs text-ink-3 uppercase tracking-[0.12em]">
				{label}
			</span>
		</div>
	);
}

export { DitherLoader };
