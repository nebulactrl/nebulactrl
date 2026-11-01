import * as React from "react";
import type { DitherMatrix } from "../../lib/bayer";
import { useDitherFrame } from "../../lib/motion";
import { useElementWidth } from "../../lib/prefs";
import {
	type BlockLetters,
	extrudeMask,
	faceTone,
	type GlyphMask,
	maskFromRgba,
	PRINT_SECONDS,
	printGain,
	sideTone,
	sweepBoost,
} from "./block-digits";
import {
	applyGlitch,
	type GlitchClock,
	type GlitchFrame,
	glitchClock,
	glitchFrameAt,
	planBurst,
	separatedSideIndex,
} from "./block-glitch";
import {
	bloomFilter,
	cellInk,
	type DitherBloom,
	type DitherColor,
	type DitherFillVariant,
	inkCss,
} from "./fill";
import { useInkRgb } from "./ink";

/** The block letters stop growing at this width, px. */
const MAX_WIDTH = 672;
/** The tallest a glyph may be, as a share of the grid's width (a single "4" would otherwise tower). */
const MAX_GLYPH_SHARE = 0.36;

/**
 * `code` set in `fontFamily`, bold, fitted to `cols` cells wide (less the
 * extrusion) and rasterised to a coverage mask at one pixel per cell. Needs a
 * canvas, so browser only; returns null without one.
 */
export function rasterise(
	code: string,
	cols: number,
	depth: number,
	fontFamily: string,
): GlyphMask | null {
	if (typeof document === "undefined" || code === "") return null;
	const probe = document.createElement("canvas").getContext("2d", {
		willReadFrequently: true,
	});
	if (!probe) return null;
	const reference = 100;
	probe.font = `700 ${reference}px ${fontFamily}`;
	const measured = probe.measureText(code);
	const inkWidth =
		measured.actualBoundingBoxLeft + measured.actualBoundingBoxRight;
	if (inkWidth <= 0) return null;
	const inkHeight =
		measured.actualBoundingBoxAscent + measured.actualBoundingBoxDescent;
	const room = Math.max(8, cols - depth);
	let scale = room / inkWidth;
	// Cap the height too, so short codes do not tower over the message.
	if (inkHeight > 0)
		scale = Math.min(scale, (cols * MAX_GLYPH_SHARE) / inkHeight);
	const fontPx = reference * scale;

	const canvas = document.createElement("canvas");
	const ctx = canvas.getContext("2d", { willReadFrequently: true });
	if (!ctx) return null;
	ctx.font = `700 ${fontPx}px ${fontFamily}`;
	const metrics = ctx.measureText(code);
	const left = Math.ceil(metrics.actualBoundingBoxLeft);
	const ascent = Math.ceil(metrics.actualBoundingBoxAscent);
	const width = Math.max(1, left + Math.ceil(metrics.actualBoundingBoxRight));
	const height = Math.max(
		1,
		ascent + Math.ceil(metrics.actualBoundingBoxDescent),
	);
	canvas.width = width;
	canvas.height = height;
	// Resizing resets the context: set the font again.
	ctx.font = `700 ${fontPx}px ${fontFamily}`;
	ctx.textBaseline = "alphabetic";
	ctx.fillStyle = "#000";
	ctx.fillText(code, left, ascent);
	return maskFromRgba(
		ctx.getImageData(0, 0, width, height).data,
		width,
		height,
	);
}

export interface BlockCodeProps {
	/** The text set as block letters, usually a status code such as "404". */
	code: string;
	/** Colour of the letters: a palette name, a custom property or any CSS colour. Default `ink`. */
	color?: DitherColor | undefined;
	/** Texture of the letter faces. Default `gradient`. */
	fill?: DitherFillVariant | undefined;
	/** A soft glow in the code's colour. */
	bloom?: DitherBloom | undefined;
	/** Size of one dither cell in CSS px. Default 4. */
	cell?: number | undefined;
	/** Depth of the extruded side in cells. 0 gives flat letters. Default 4. */
	depth?: number | undefined;
	/** The threshold pattern. Default `bayer4`. */
	matrix?: DitherMatrix | undefined;
	/** Print the letters in on mount and sweep light across them. Default true. */
	animate?: boolean | undefined;
	/**
	 * What moves after the print-in. `shine`: a band of light crosses the faces
	 * every few seconds. `glitch`: the letters stay still and, every 3 to 6
	 * seconds, break up for 150 to 300 ms in stepped frames (slices shift, a few
	 * cells drop or repeat, the side separates by a cell) and snap back. Default
	 * `shine`.
	 */
	motion?: "shine" | "glitch" | undefined;
}

/**
 * The status code as block letters on a canvas, one pixel per cell: a face
 * densest at the top and thinning towards the baseline, a sparse extruded side
 * down and to the right. On mount it prints in level by level; after that a
 * band of light crosses the faces every few seconds (`motion="glitch"`: short
 * bursts of faults instead, see `block-glitch.ts`). Under reduced motion, with
 * `animate={false}`, or when the block is off-screen or the tab hidden, it is
 * one finished still frame.
 *
 * The block fills its container (up to 672 px) and is set in the container's
 * own `font-family`. It is `aria-hidden`: render the code as text elsewhere.
 */
export function BlockCode({
	code,
	color,
	fill = "gradient",
	bloom,
	cell = 4,
	depth = 4,
	animate = true,
	matrix = "bayer4",
	motion = "shine",
}: BlockCodeProps) {
	const hostRef = React.useRef<HTMLDivElement>(null);
	const canvasRef = React.useRef<HTMLCanvasElement>(null);
	const width = useElementWidth(hostRef);
	const [fontEpoch, setFontEpoch] = React.useState(0);
	const [r, g, b] = useInkRgb(color);

	// The faces are set in the page's own font: draw again once web fonts load.
	React.useEffect(() => {
		if (typeof document === "undefined" || !document.fonts) return;
		let alive = true;
		const bump = () => {
			if (alive) setFontEpoch((n) => n + 1);
		};
		document.fonts.ready.then(bump);
		document.fonts.addEventListener("loadingdone", bump);
		return () => {
			alive = false;
			document.fonts.removeEventListener("loadingdone", bump);
		};
	}, []);

	// biome-ignore lint/correctness/useExhaustiveDependencies: `fontEpoch` is a re-measure trigger: it re-sets the type once web fonts load
	const letters = React.useMemo<BlockLetters | null>(() => {
		if (width === null || width <= 0) return null;
		const cols = Math.floor(Math.min(width, MAX_WIDTH) / cell);
		if (cols < 8) return null;
		const family = hostRef.current
			? getComputedStyle(hostRef.current).fontFamily
			: "monospace";
		const mask = rasterise(code, cols, depth, family);
		return mask ? extrudeMask(mask, depth) : null;
	}, [width, cell, depth, code, fontEpoch]);

	const startRef = React.useRef<number | null>(null);
	const lastRef = React.useRef<{ time: number; still: boolean }>({
		time: 0,
		still: true,
	});

	const glitchRef = React.useRef<GlitchClock | null>(null);
	/** True while the canvas shows the clean letters at full gain, so an idle glitch block repaints nothing. */
	const cleanRef = React.useRef(false);

	const draw = React.useCallback(
		(time: number, still: boolean, force = false) => {
			const canvas = canvasRef.current;
			const ctx = canvas?.getContext("2d");
			if (!(letters && ctx)) return;
			if (startRef.current === null) startRef.current = time;
			const elapsed = time - startRef.current;
			const gain = still ? 1 : printGain(elapsed);
			const sweepAt =
				still || motion === "glitch" ? -1 : elapsed - PRINT_SECONDS - 0.4;
			// Reduced motion and a paused block are one clean frame, never a fault.
			let fault: GlitchFrame | null = null;
			if (motion === "glitch" && !still && gain === 1) {
				glitchRef.current ??= glitchClock(elapsed, Math.random);
				fault = glitchFrameAt(
					glitchRef.current,
					elapsed,
					() =>
						planBurst(
							Math.random,
							{
								width: letters.width,
								height: letters.height,
								faceTop: letters.faceTop,
								faceBottom: letters.faceBottom,
							},
							(x, y) => letters.face[y * letters.width + x] === 1,
						),
					Math.random,
				);
			}
			if (
				motion === "glitch" &&
				fault === null &&
				gain === 1 &&
				cleanRef.current &&
				!force
			) {
				return;
			}
			cleanRef.current = gain === 1 && fault === null;
			const separate = fault?.separate ?? false;
			const image = ctx.createImageData(letters.width, letters.height);
			for (let y = 0; y < letters.height; y++) {
				for (let x = 0; x < letters.width; x++) {
					const index = y * letters.width + x;
					let alpha = 0;
					if (letters.face[index] === 1) {
						const tone =
							faceTone(y, letters.faceTop, letters.faceBottom) * gain +
							sweepBoost(x, y, letters.width, letters.height, sweepAt);
						alpha = cellInk(fill, tone, x, y, matrix);
					} else {
						const from = separatedSideIndex(
							x,
							y,
							letters.width,
							letters.height,
							separate,
						);
						const k = from < 0 ? 0 : (letters.side[from] ?? 0);
						if (k > 0) {
							const tone = sideTone(k, letters.depth) * gain;
							alpha = cellInk("gradient", tone, x, y, matrix);
						}
					}
					if (alpha <= 0) continue;
					const at = index * 4;
					image.data[at] = r;
					image.data[at + 1] = g;
					image.data[at + 2] = b;
					image.data[at + 3] = Math.round(alpha * 255);
				}
			}
			if (fault) applyGlitch(image.data, letters.width, letters.height, fault);
			ctx.putImageData(image, 0, 0);
		},
		[letters, fill, matrix, motion, r, g, b],
	);

	useDitherFrame(
		(time, _delta, still) => {
			const isStill = still !== undefined;
			lastRef.current = { time, still: isStill };
			draw(time, isStill);
		},
		{ fps: 20, enabled: animate, target: hostRef, stillTime: 0 },
	);
	// A new grid starts a new glitch clock: bursts are planned for one grid's size.
	// biome-ignore lint/correctness/useExhaustiveDependencies: `letters` is the trigger, not a value read here
	React.useLayoutEffect(() => {
		glitchRef.current = null;
		cleanRef.current = false;
	}, [letters]);
	// A new grid, colour or fill repaints what is on screen.
	React.useLayoutEffect(() => {
		draw(lastRef.current.time, lastRef.current.still, true);
	}, [draw]);

	const glow = bloomFilter(inkCss(color), bloom);
	return (
		<div
			ref={hostRef}
			aria-hidden
			data-slot="dither-code"
			className="flex w-full max-w-[672px] justify-center font-mono"
			// Reserve the height before the first measurement so the block does not jump.
			style={{ minHeight: letters ? undefined : cell * 24 }}
		>
			{letters ? (
				<canvas
					ref={canvasRef}
					width={letters.width}
					height={letters.height}
					className="forced-colors:hidden"
					style={{
						width: letters.width * cell,
						height: letters.height * cell,
						imageRendering: "pixelated",
						...(glow ? { filter: glow } : null),
					}}
				/>
			) : null}
			<span className="hidden font-bold text-6xl text-ink-1 forced-colors:block">
				{code}
			</span>
		</div>
	);
}
