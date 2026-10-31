import { cn } from "cn";
import * as React from "react";
import type { DitherMatrix } from "../../lib/bayer";
import { type BandMask, bandMaskClass, rampStops } from "./bands";
import {
	type DitherColor,
	type DitherFillVariant,
	fillTile,
	inkCss,
} from "./fill";
import {
	type DitherCardGlow,
	EDGE_STRETCH,
	type GlowRing,
	glowOrigin,
	glowRings,
	hoverRings,
	rimRings,
} from "./glow";
import { Card } from "./primitives";

export type { DitherCardGlow } from "./glow";

// The lit edge: dither density falls from the top edge over a third of the card.
const EDGE: BandMask = {
	axis: "y",
	stops: [...rampStops(0.5, 0.06, 6), ...Array.from({ length: 12 }, () => 0)],
	cell: 2,
};
// The material: a faint flat grain over the whole surface.
const GRAIN: BandMask = { axis: "y", stops: [0.1], cell: 3 };
// A corner bloom: dither density rises toward the right edge.
const BLOOM: BandMask = {
	axis: "x",
	stops: rampStops(0, 0.45, 8),
	cell: 2,
};

/** Punch the content box out of the border box: what is left is a band `padding` wide. */
const RIM_MASK: React.CSSProperties = {
	WebkitMaskImage: "linear-gradient(#000 0 0), linear-gradient(#000 0 0)",
	WebkitMaskClip: "content-box, border-box",
	WebkitMaskComposite: "xor",
	maskImage: "linear-gradient(#000 0 0), linear-gradient(#000 0 0)",
	maskClip: "content-box, border-box",
	maskComposite: "exclude",
};

export interface DitherCardProps extends React.ComponentProps<typeof Card> {
	/**
	 * `edge`: the top edge is lit with fading dither. `grain`: a flat grain over
	 * the surface. `bloom`: a corner of accent dither. `all` combines edge and
	 * grain. `none` leaves only the glow.
	 */
	finish?: "edge" | "grain" | "bloom" | "all" | "none";
	/** A dithered glow in a corner or along an edge, with a lit rim on the border. Off unless set. */
	glow?: DitherCardGlow | undefined;
	/** How far the glow spreads from its origin, px. Default 240. */
	reach?: number | undefined;
	/** Densest dither level, at the origin, 0..1. Default 0.5. */
	strength?: number | undefined;
	/** Light the border with dither, densest near the glow. Default true. */
	rim?: boolean | undefined;
	/** Corner radius in px; the rim follows it. Default: the card's own. */
	radius?: number | undefined;
	/** Grow the glow ring by ring on hover and when something inside has focus. */
	interactive?: boolean | undefined;
	/** Glow colour: a palette name, a custom property or any CSS colour. Default `ink`. */
	color?: DitherColor | undefined;
	/** Texture of the glow. Gradient steps through dither levels; the rest stack one texture and fade it by overlap. */
	fill?: DitherFillVariant | undefined;
	/** The threshold pattern. Default `bayer4`. */
	matrix?: DitherMatrix | undefined;
	/** Size of one glow cell in CSS px. Default 3. */
	cell?: number | undefined;
}

interface GlowStackProps {
	glow: DitherCardGlow;
	rings: readonly GlowRing[];
	fill: DitherFillVariant;
	matrix: DitherMatrix;
	cell: number;
	hue: string;
	/** Fade each ring in on hover and focus, this many ms after the previous one. */
	stepMs?: number;
}

/**
 * One stack of rings as an SVG with no viewBox, so a cell is always whole CSS
 * pixels. Each ring is filled with its own dither tile; origins sit on the
 * card's edge so the stack reads as a light bleeding in.
 */
function GlowStack({
	glow,
	rings,
	fill,
	matrix,
	cell,
	hue,
	stepMs,
}: GlowStackProps) {
	// React 19 ids contain « » or _ characters: keep only what `url(#…)` accepts.
	const uid = `nc-glow-${React.useId().replace(/[^a-zA-Z0-9_-]/g, "")}`;
	const origin = glowOrigin(glow);
	return (
		<svg
			aria-hidden
			width="100%"
			height="100%"
			className="absolute inset-0 block size-full"
		>
			<defs>
				{rings.map((ring, index) => {
					const tile = fillTile(fill, matrix, ring.tone, cell);
					return (
						<pattern
							key={`${ring.r}-${ring.tone}`}
							id={`${uid}-${index}`}
							width={tile.size}
							height={tile.size}
							patternUnits="userSpaceOnUse"
						>
							<path
								d={tile.d}
								style={{ fill: hue, fillOpacity: tile.opacity }}
								shapeRendering="crispEdges"
							/>
						</pattern>
					);
				})}
			</defs>
			{rings.map((ring, index) => {
				const shape = origin.wide ? (
					<ellipse
						cx={origin.cx}
						cy={origin.cy}
						rx={ring.r * EDGE_STRETCH}
						ry={ring.r}
						fill={`url(#${uid}-${index})`}
						opacity={ring.opacity}
					/>
				) : (
					<circle
						cx={origin.cx}
						cy={origin.cy}
						r={ring.r}
						fill={`url(#${uid}-${index})`}
						opacity={ring.opacity}
					/>
				);
				if (stepMs === undefined)
					return <g key={`${ring.r}-${ring.tone}`}>{shape}</g>;
				return (
					<g
						key={`${ring.r}-${ring.tone}`}
						className="opacity-0 transition-opacity duration-200 ease-out group-focus-within/card:opacity-100 group-hover/card:opacity-100 motion-reduce:transition-none"
						style={{
							transitionDelay: `${(rings.length - 1 - index) * stepMs}ms`,
						}}
					>
						{shape}
					</g>
				);
			})}
		</svg>
	);
}

/**
 * A `Card` with a dithered surface finish. The material (variant, border,
 * radius, shadow) is the card's own; the finish is decoration behind the
 * content, so text on it stays crisp and nothing here is read by assistive tech.
 *
 * `glow` adds a stack of dither rings from a corner or an edge (default
 * `top-left`) and a lit rim on the border; `interactive` grows it on hover and
 * focus. Setting `glow`, `interactive`, `reach`, `strength`, `color`, `fill` or
 * `matrix` turns the glow on; a card with none of them keeps only its finish.
 * The glow is static SVG: it costs nothing while idle.
 */
function DitherCard({
	finish = "all",
	glow: glowProp,
	reach: reachProp,
	strength: strengthProp,
	rim = true,
	radius,
	interactive,
	color,
	fill: fillProp,
	matrix: matrixProp,
	cell = 3,
	className,
	style,
	children,
	...props
}: DitherCardProps) {
	const edge = finish === "edge" || finish === "all";
	const grain = finish === "grain" || finish === "all";
	const bloom = finish === "bloom";
	const glowOn =
		glowProp !== undefined ||
		interactive !== undefined ||
		reachProp !== undefined ||
		strengthProp !== undefined ||
		color !== undefined ||
		fillProp !== undefined ||
		matrixProp !== undefined;
	const glow = glowOn ? (glowProp ?? "top-left") : undefined;
	const reach = reachProp ?? 240;
	const strength = strengthProp ?? 0.5;
	const fill = fillProp ?? "gradient";
	const matrix = matrixProp ?? "bayer4";
	const hue = inkCss(color);
	const rings = React.useMemo(
		() => glowRings({ reach, strength, fill }),
		[reach, strength, fill],
	);
	const rimStack = React.useMemo(
		() => rimRings({ reach, strength, fill }),
		[reach, strength, fill],
	);
	const grown = React.useMemo(
		() => hoverRings({ reach, strength, fill }),
		[reach, strength, fill],
	);
	const cornerStyle =
		radius === undefined ? style : { borderRadius: radius, ...style };

	return (
		<Card
			className={cn("relative isolate", className)}
			{...(cornerStyle ? { style: cornerStyle } : {})}
			{...props}
		>
			<span
				aria-hidden
				data-slot="card-finish"
				className="pointer-events-none absolute inset-0 -z-10 forced-colors:hidden"
			>
				{grain ? (
					<span
						className={cn(
							"absolute inset-0 bg-ink-1/[0.07]",
							bandMaskClass(GRAIN),
						)}
					/>
				) : null}
				{edge ? (
					<span
						className={cn(
							"absolute inset-0 bg-white/[0.16]",
							bandMaskClass(EDGE),
						)}
					/>
				) : null}
				{bloom ? (
					<span
						className={cn(
							"absolute inset-y-0 right-0 w-1/2 bg-accent-ink/40",
							bandMaskClass(BLOOM),
						)}
					/>
				) : null}
			</span>
			{glow ? (
				<span
					aria-hidden
					data-slot="card-glow"
					className="pointer-events-none absolute inset-0 -z-10 overflow-hidden rounded-[inherit] forced-colors:hidden"
				>
					<GlowStack
						glow={glow}
						rings={rings}
						fill={fill}
						matrix={matrix}
						cell={cell}
						hue={hue}
					/>
					{interactive ? (
						<GlowStack
							glow={glow}
							rings={grown}
							fill={fill}
							matrix={matrix}
							cell={cell}
							hue={hue}
							stepMs={45}
						/>
					) : null}
					{rim ? (
						<span
							data-slot="card-rim"
							className="absolute inset-0 rounded-[inherit]"
							style={{ padding: cell, ...RIM_MASK }}
						>
							<GlowStack
								glow={glow}
								rings={rimStack}
								fill={fill}
								matrix={matrix}
								cell={cell}
								hue={hue}
							/>
						</span>
					) : null}
				</span>
			) : null}
			{children}
		</Card>
	);
}

export { DitherCard };
