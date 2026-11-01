import { memo, useId, useMemo } from "react";
import { areaBands, type Pt, regionPath } from "../../lib/chart-math";
import {
	type DitherFill,
	ditherGradientStops,
	useDitherFillUrls,
} from "../../lib/pattern";
import { Bloom } from "./chart-kit";
import {
	type ChartBloom,
	type DitherMatrix,
	type FillVariant,
	flatFill,
	inkFill,
} from "./shared";

export interface AreaFillProps {
	/** The sampled line (the dense edge of the fill). */
	top: readonly Pt[];
	/** What the fill reaches down to: a baseline y, or the lower edge of a stack, sampled like `top`. */
	bottom: readonly Pt[] | number;
	/** CSS colour of the ink. */
	color: string;
	variant?: FillVariant;
	/** Dither cell in px. Default 3. */
	cell?: number;
	/** Distance in px over which a gradient thins out. Default 96. */
	depth?: number;
	/** y of the plot's bottom edge: the last band runs down to it. */
	floor: number;
	/** Tone just under the line. Default 0.72. */
	level?: number;
	/** Tone at the far end of the fade. Default 0.08. */
	floorTone?: number;
	/** Number of tone bands. Default 7. */
	bands?: number;
	/** Plain translucent fill (forced colours, tiny cells). */
	solid?: boolean;
	/** Threshold matrix of the dither (Bayer 2/4/8 or blue noise). Default Bayer 4. */
	matrix?: DitherMatrix | undefined;
	/** Soft glow in the colour behind the fill. Default off. */
	bloom?: ChartBloom | undefined;
}

/**
 * The dithered body of an area: horizontal tone bands that follow the line
 * (densest right under it), clipped to the region between `top` and
 * `bottom`. The bands are strips of the same curve shifted down, so the
 * dither is exactly as dense at a steep rise as on a flat run. Pure SVG:
 * the patterns live in the page-wide registry, so a chart redraw only
 * changes path data.
 */
export const AreaFill = memo(function AreaFill({
	top,
	bottom,
	color,
	variant = "gradient",
	cell = 3,
	depth = 96,
	floor,
	level = 0.72,
	floorTone = 0.08,
	bands = 7,
	solid = false,
	matrix,
	bloom = "off",
}: AreaFillProps) {
	const clipId = `nc-clip-${useId().replace(/[^a-zA-Z0-9_-]/g, "")}`;
	const gradient = variant === "gradient" && !solid;
	const tones = useMemo(
		() => (gradient ? ditherGradientStops(level, floorTone, bands) : []),
		[gradient, level, floorTone, bands],
	);
	const fills = useMemo<DitherFill[]>(() => {
		if (solid || variant === "solid") return [];
		if (variant === "gradient") {
			return tones.map((tone) => inkFill(tone, color, cell, matrix));
		}
		return [flatFill(variant, color, cell, matrix)];
	}, [solid, variant, tones, color, cell, matrix]);
	const urls = useDitherFillUrls(fills);
	const geometry = useMemo(
		() => ({
			region: regionPath(top, bottom),
			bands: gradient ? areaBands(top, { depth, tones, floor }) : [],
		}),
		[top, bottom, gradient, depth, tones, floor],
	);
	if (top.length < 2) return null;
	return (
		<g aria-hidden>
			<Bloom bloom={bloom} box={{ width: top.at(-1)?.x ?? 0, height: floor }}>
				<path d={geometry.region} fill={color} />
			</Bloom>
			<clipPath id={clipId}>
				<path d={geometry.region} />
			</clipPath>
			<g clipPath={`url(#${clipId})`}>
				{geometry.bands.length > 0 ? (
					geometry.bands.map((band, i) => (
						<path
							// biome-ignore lint/suspicious/noArrayIndexKey: bands are an ordered, fixed-length stack
							key={i}
							d={band.path}
							fill={urls[i]}
						/>
					))
				) : urls[0] ? (
					<path d={geometry.region} fill={urls[0]} />
				) : (
					<path
						d={geometry.region}
						fill={`color-mix(in oklab, ${color} 26%, transparent)`}
					/>
				)}
			</g>
		</g>
	);
});
