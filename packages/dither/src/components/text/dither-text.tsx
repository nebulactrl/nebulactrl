import { cn } from "cn";
import {
	type CSSProperties,
	type ElementType,
	type HTMLAttributes,
	type ReactNode,
	useEffect,
	useMemo,
	useRef,
	useState,
} from "react";
import {
	type BayerSize,
	bayerMatrixName,
	type DitherMatrix,
} from "../../lib/bayer";
import { useDitherFrame } from "../../lib/motion";
import { ditherMaskStyle } from "../../lib/pattern";
import { useDitherPalette } from "../../lib/tokens";
import { gridFor } from "../images/grid";
import {
	useBoxSize,
	useDevicePixelRatio,
	useFontVersion,
} from "../images/hooks";
import { cssTextColor, resolveTextRgba, type TextColor } from "./colors";
import {
	defaultGhost,
	type FillDirection,
	type FillSpec,
	paintTextGrid,
	type TextFill,
	type TextRgba,
} from "./fills";
import { type GlyphRaster, rasterizeText } from "./glyph";

/** Strength of the glow round the letters. `true` is `"high"`. */
export type DitherTextBloom = "off" | "low" | "high";

export interface DitherTextProps
	extends Omit<HTMLAttributes<HTMLElement>, "children" | "color"> {
	/** The headline. `\n` starts a new line; there is no automatic wrapping. */
	children: string;
	/** Element to render. Default `span`. */
	as?: ElementType;
	/**
	 * What fills the letters: `gradient` (solid on one side, sparse on the
	 * other), `noise` (drifting blobs of density), `dotted`, `hatched`, `solid`.
	 */
	fill?: TextFill;
	/**
	 * Gradient: the way it thins out from its dense side (`"down"` is dense at
	 * the top). Noise, dotted and hatched: the way it drifts. A number is an
	 * angle in degrees as in CSS (0 up, 90 right, 180 down).
	 */
	direction?: FillDirection;
	/** Tone at the dense end, 0 (no ink) to 1 (solid). Default 1. */
	from?: number;
	/** Tone at the sparse end. Keep it above about 0.3 so letters stay readable. Default 0.4. */
	to?: number;
	/** Noise only: blobs across the height of the text. Higher is smaller blobs. Default 3. */
	scale?: number;
	/** Bayer matrix size. `matrix` wins when both are set. */
	n?: BayerSize;
	/** Threshold pattern for gradient and noise. Default `"bayer4"`. */
	matrix?: DitherMatrix;
	/** Cell edge in CSS px, snapped to whole device px. Default: about 1/24 of the font size, at least 2. */
	cell?: number;
	/** Ink colour: `ink`, `accent`, `muted`, `paper`, a palette name such as `green`, a `--token` or any CSS colour. Default `ink`. */
	color?: TextColor;
	/** Opacity of the ink between dots and strokes, 0..1, so letters keep their shape. Default 0.2 for dotted and hatched, otherwise 0. */
	ghost?: number;
	/** Colours from the lightest tone to the darkest, for a multi-tone fill. Overrides `color` and `ghost`. */
	palette?: readonly [TextColor, TextColor, ...TextColor[]] | undefined;
	/** A soft glow of the ink colour round the letters. Default `"off"`. */
	bloom?: boolean | DitherTextBloom;
	/** Drift the fill. One static frame under reduced motion. Default true. */
	animate?: boolean;
	/** How fast the fill drifts; 0 holds it still. Default 1. */
	speed?: number;
	/** Frame rate of the drift. Default 12. */
	fps?: number;
}

const BLOOM: Record<
	DitherTextBloom,
	{ divisor: number; alpha: number } | null
> = {
	off: null,
	low: { divisor: 10, alpha: 0.3 },
	high: { divisor: 6, alpha: 0.45 },
};

const bloomLevel = (bloom: boolean | DitherTextBloom): DitherTextBloom =>
	bloom === true ? "high" : bloom === false ? "off" : bloom;

/** The auto cell: about 1/24 of the font size, at least 2 CSS px. */
export function autoCell(fontSize: number): number {
	return Number.isFinite(fontSize) ? Math.max(2, Math.round(fontSize / 24)) : 3;
}

/**
 * A display headline whose letters are filled with a dither. The text stays a
 * real element (selectable, translatable, read by screen readers, sized and
 * wrapped by your classes); it is painted transparent and a canvas of dither
 * cells, drawn from the same font, sits behind it, so a selection shows solid
 * in the page's selection colours. Before the canvas is drawn the letters show
 * through a plain dither mask. Under forced colours the canvas is hidden and
 * the text shows in the system colour. Only for display type: body text and
 * labels should stay crisp.
 */
export function DitherText({
	children,
	as: Tag = "span",
	fill = "gradient",
	direction = "down",
	from = 1,
	to = 0.4,
	scale = 3,
	n = 4,
	matrix,
	cell,
	color = "ink",
	ghost,
	palette,
	bloom = "off",
	animate = true,
	speed = 1,
	fps = 12,
	className,
	style,
	...rest
}: DitherTextProps): ReactNode {
	const hostRef = useRef<HTMLElement | null>(null);
	const canvasRef = useRef<HTMLCanvasElement | null>(null);
	const bufferRef = useRef<Uint8ClampedArray<ArrayBuffer> | null>(null);
	const timeRef = useRef(0);
	const themePalette = useDitherPalette();
	const dpr = useDevicePixelRatio();
	const box = useBoxSize(hostRef);
	const fontVersion = useFontVersion();
	const [glyph, setGlyph] = useState<GlyphRaster | null>(null);

	const resolvedMatrix = matrix ?? bayerMatrixName(n);
	const spec: FillSpec = useMemo(
		() => ({ fill, direction, from, to, scale, n, matrix: resolvedMatrix }),
		[fill, direction, from, to, scale, n, resolvedMatrix],
	);
	const ink = useMemo(
		() => resolveTextRgba(color, themePalette),
		[color, themePalette],
	);
	const paletteKey = palette?.join("|");
	// biome-ignore lint/correctness/useExhaustiveDependencies: paletteKey stands for the palette's contents.
	const ramp = useMemo<TextRgba[] | undefined>(
		() => palette?.map((entry) => resolveTextRgba(entry, themePalette)),
		[paletteKey, themePalette],
	);
	const inkRgb = (ramp?.[ramp.length - 1] ?? ink).slice(0, 3) as [
		number,
		number,
		number,
	];
	const ghostAlpha = ghost ?? defaultGhost(fill);

	// Rasterise the glyphs whenever the text, its box, the font or the cell grid changes.
	// biome-ignore lint/correctness/useExhaustiveDependencies: fontVersion re-measures once web fonts load.
	useEffect(() => {
		const host = hostRef.current;
		if (!host || !box || box.width < 1 || box.height < 1) return;
		const fontSize = Number.parseFloat(getComputedStyle(host).fontSize);
		const grid = gridFor(
			box.width,
			box.height,
			cell ?? autoCell(fontSize),
			dpr,
		);
		setGlyph(rasterizeText(host, children, grid));
	}, [children, box, cell, dpr, fontVersion]);

	const draw = (t: number) => {
		const canvas = canvasRef.current;
		if (!canvas || !glyph) return;
		const { cols, rows } = glyph.grid;
		if (canvas.width !== cols) canvas.width = cols;
		if (canvas.height !== rows) canvas.height = rows;
		const ctx = canvas.getContext("2d");
		if (!ctx) return;
		let buffer = bufferRef.current;
		if (buffer?.length !== cols * rows * 4) {
			buffer = new Uint8ClampedArray(cols * rows * 4);
			bufferRef.current = buffer;
		}
		const pixels = paintTextGrid(
			glyph.coverage,
			cols,
			rows,
			spec,
			[ink[0], ink[1], ink[2]],
			ghostAlpha,
			t,
			buffer,
			ramp,
		);
		ctx.putImageData(new ImageData(pixels, cols, rows), 0, 0);
	};

	useDitherFrame(
		(time) => {
			timeRef.current = time * speed;
			draw(timeRef.current);
		},
		{
			fps,
			enabled: animate && fill !== "solid" && speed !== 0 && glyph !== null,
			target: hostRef,
			stillTime: 0,
		},
	);
	// Static frames (reduced motion, solid fills, new colours) repaint on any input change.
	// biome-ignore lint/correctness/useExhaustiveDependencies: draw closes over glyph, spec, ink, ramp and ghostAlpha.
	useEffect(() => {
		draw(timeRef.current);
	}, [glyph, spec, ink, ramp, ghostAlpha]);

	const grid = glyph?.grid;
	const level = BLOOM[bloomLevel(bloom)];
	const glow = level
		? `drop-shadow(0 0 ${Math.max(
				4,
				Math.round((box?.height ?? 40) / level.divisor),
			)}px rgb(${inkRgb.join(" ")} / ${level.alpha}))`
		: undefined;

	// Until the canvas is drawn, the letters show in the ink colour through a plain dither mask.
	const fallback: CSSProperties | undefined = grid
		? undefined
		: {
				color: cssTextColor(palette?.[palette.length - 1] ?? color),
				...(fill === "solid"
					? {}
					: ditherMaskStyle((from + to) / 2, {
							cell: cell ?? 3,
							matrix: resolvedMatrix,
						})),
			};

	return (
		<Tag
			{...rest}
			ref={hostRef}
			className={cn("relative isolate inline-block whitespace-pre", className)}
			style={{ fontFamily: "var(--font-display)", ...style }}
		>
			<span
				className={cn(
					grid ? "text-transparent" : "",
					"forced-colors:text-[CanvasText]",
				)}
				style={fallback}
			>
				{children}
			</span>
			{grid ? (
				<canvas
					ref={canvasRef}
					className="pointer-events-none absolute top-0 left-0 -z-10 forced-colors:hidden"
					style={{
						width: grid.cssWidth,
						height: grid.cssHeight,
						imageRendering: "pixelated",
						...(glow ? { filter: glow } : {}),
					}}
				/>
			) : null}
		</Tag>
	);
}
