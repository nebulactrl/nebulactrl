import { cn } from "cn";
import { useMemo } from "react";
import { DitherShader } from "../../lib/gl";
import { type BackgroundBaseProps, clamp, INK, shaderBase } from "./common";
import { worldLandTextures } from "./data/land-mask";
import {
	boundsAspect,
	DEFAULT_BOUNDS,
	type LatLon,
	latLonToUV,
	type MapBounds,
} from "./geo";
import {
	DOTMAP_HELPERS,
	dotMapArcsGlsl,
	dotMapMarkersGlsl,
	sceneKey,
} from "./geo-glsl";
import { BG2_COMMON } from "./glsl";
import { planInks } from "./item-colors";

// The map is a grid of square (or round) dots. Every dot is evaluated once at
// its centre: land comes from the shared mask, markers add a core, an
// expanding ring and a dithered halo on the land around them, and arcs are
// dotted curves with a stretch of light running along them. Palette ramp:
// paper, `color` (land, tone 0.5), then `accent` (markers, halos, arcs).
const FIELD_MAIN = /* glsl */ `
float field(vec2 uv, float t) {
  float pitchU = 1.0 / dmCols;
  float pitchV = pitchU * dmAspect;
  vec2 pitch = vec2(pitchU, pitchV);
  vec2 id = floor(uv / pitch);
  vec2 ctr = (id + 0.5) * pitch;
  vec2 lc = (uv - ctr) / pitch;

  float lon = dmBounds.x + ctr.x * (dmBounds.y - dmBounds.x);
  float lat = dmBounds.z + ctr.y * (dmBounds.w - dmBounds.z);
  float land = step(0.5, texture(dmLand, vec2(fract((lon + 180.0) / 360.0), (lat + 90.0) / 180.0)).r);

  dmMk = 0.0;
  dmAk = 0.0;
  dmInk = 0.0;
  vec3 mk = dmMarkers(ctr, t);
  float arc = dmArcs(ctr, t);

  float boost = land * max(mk.y, mk.z);
  float tone = land * (0.5 + 0.5 * max(mk.y, mk.z));
  tone = max(tone, (1.0 - land) * step(hash21(id), dmOcean) * 0.17);
  float top = max(mk.x, arc);
  // Where a marker's ring or halo lifts a land dot, the marker's colour takes
  // some of the dots at random, so the halo reads as a dither of its colour.
  if (boost > 0.0 && hash21(id + 3.7) < boost) dmInk = dmMk;
  if (top > land * 0.5) dmInk = mk.x >= arc ? dmMk : dmAk;
  tone = max(tone, top);

  float shape = dmRound > 0.5
    ? 1.0 - smoothstep(0.30, 0.42, length(lc))
    : 1.0 - smoothstep(0.34, 0.40, max(abs(lc.x), abs(lc.y)));
  // With per-item colours a dot is inked or not and colorField picks its
  // colour, so the half of the ramp that mixes land into accent is spent here.
  return dmColoured > 0.5 ? min(tone * shape * 2.0, 1.0) : tone * shape;
}
`;

// With per-item colours a dot's colour is the ramp position of whatever drew
// it: the land colour, or the marker or arc on top of it.
const COLOR_FIELD = /* glsl */ `
vec3 colorField(vec2 uv, float t, float tone) {
  return paletteColor(dmInk).rgb;
}
`;

export interface DotMapMarker {
	/** `[latitude, longitude]` in degrees. */
	at: LatLon;
	/** Shown beside the marker and read by screen readers. */
	label?: string | undefined;
	/** Colour of this marker and its halo: a name such as "blue", a `--token` or any CSS colour. Default `accent`. */
	color?: string | undefined;
	/**
	 * 0 to 1: scales the marker's size and brightness, so a busier place reads
	 * bigger and brighter. Left out, the marker keeps its full look.
	 */
	weight?: number | undefined;
}

export interface DotMapArc {
	from: LatLon;
	to: LatLon;
	/** Colour of this arc. Default `accent`. */
	color?: string | undefined;
}

export interface DitherDotMapProps
	extends Omit<BackgroundBaseProps, "palette" | "cell" | "shape"> {
	/** Locations to mark (up to 24). Labels show beside the marker. */
	markers?: readonly DotMapMarker[] | undefined;
	/** Dotted curves between places (up to 8). */
	arcs?: readonly DotMapArc[] | undefined;
	/** Colour of the land dots. Default "grey". */
	color?: string | undefined;
	/** Colour of markers, halos and arcs that do not set their own. Default `--dither-ink`. */
	accent?: string | undefined;
	/** Colour behind the dots. Default transparent. */
	background?: string | undefined;
	/** Dots across the full width. Default 160. */
	columns?: number | undefined;
	/** Dot shape. Default "square". */
	dots?: "square" | "round" | undefined;
	/** dithercn's name for `dots`. Wins over it when both are set. */
	shape?: "square" | "round" | undefined;
	/** Radius in degrees of each marker's dithered glow on the land. 0 turns it off. Default 14. */
	halo?: number | undefined;
	/** Share of sea cells with a faint dot, 0 to 1. Default 0. */
	ocean?: number | undefined;
	/** The region to draw, in degrees. Default 78°N to 56°S, all longitudes. */
	bounds?: MapBounds | undefined;
	/** Size of one dither cell in CSS pixels. Default 1: the dots are the pattern. */
	cell?: number | undefined;
	/** Accessible name. Marker labels are appended to it. Default "World map". */
	title?: string | undefined;
	/**
	 * Draw marker labels beside their dots. Default true. Turn it off when the
	 * caller lays its own labels or hotspots over the map (the labels are
	 * still read out through the accessible name).
	 */
	labels?: boolean | undefined;
}

const NO_MARKERS: readonly DotMapMarker[] = [];
const NO_ARCS: readonly DotMapArc[] = [];

/** A flat world map in dithered dots with pulsing markers and dotted arcs. */
export function DitherDotMap({
	markers = NO_MARKERS,
	arcs = NO_ARCS,
	color = "grey",
	accent = INK,
	background = "transparent",
	columns = 160,
	dots = "square",
	shape,
	halo = 14,
	ocean = 0,
	bounds = DEFAULT_BOUNDS,
	cell = 1,
	title = "World map",
	labels = true,
	levels,
	animate = true,
	fps,
	speed,
	className,
	style,
	fallback,
	matrix,
	n,
	stillTime,
}: DitherDotMapProps) {
	const plan = planInks(background, color, accent, [
		...markers.map((marker) => marker.color),
		...arcs.map((arc) => arc.color),
	]);
	const inkMarkers = markers.map((marker, i) => ({
		at: marker.at,
		ink: plan.positions[i],
		weight: marker.weight,
	}));
	const inkArcs = arcs.map((arc, i) => ({
		from: arc.from,
		to: arc.to,
		ink: plan.positions[markers.length + i],
	}));
	const aspect = boundsAspect(bounds);
	const cols = Math.max(20, Math.round(columns));
	const pitchV = (1 / cols) * aspect;
	const key = `${sceneKey(inkMarkers, inkArcs)}|${cols}|${bounds.north},${bounds.south},${bounds.west},${bounds.east}`;
	// biome-ignore lint/correctness/useExhaustiveDependencies: `key` is the content of markers, arcs, columns and bounds, so inline arrays do not recompile.
	const field = useMemo(
		() =>
			[
				BG2_COMMON,
				DOTMAP_HELPERS,
				dotMapMarkersGlsl(inkMarkers, bounds),
				dotMapArcsGlsl(inkArcs, bounds, aspect, pitchV),
				FIELD_MAIN,
			].join("\n"),
		[key],
	);

	const labelled = markers
		.map((marker) => ({
			marker,
			uv: latLonToUV(marker.at[0], marker.at[1], bounds),
		}))
		.filter(
			({ marker, uv }) =>
				marker.label && uv[0] >= 0 && uv[0] <= 1 && uv[1] >= 0 && uv[1] <= 1,
		);
	const summary = [title, ...labelled.map(({ marker }) => marker.label)].join(
		", ",
	);

	return (
		<div
			role="img"
			aria-label={summary}
			className={cn("relative w-full forced-colors:hidden", className)}
			style={{ aspectRatio: aspect, ...style }}
		>
			<DitherShader
				{...shaderBase(
					{
						matrix,
						n,
						levels,
						cell,
						fps,
						animate,
						stillTime,
						speed,
						fallback,
					},
					// Per-item colours spend the whole tone range on coverage, so they need half the steps.
					{ staticTime: 1, levels: plan.coloured ? 5 : 9 },
				)}
				className="absolute inset-0"
				field={field}
				colorField={plan.coloured ? COLOR_FIELD : undefined}
				textures={worldLandTextures("dmLand")}
				palette={plan.palette}
				uniforms={{
					dmCols: cols,
					dmAspect: aspect,
					dmBounds: [bounds.west, bounds.east, bounds.south, bounds.north],
					dmSpan: [bounds.east - bounds.west, bounds.north - bounds.south],
					dmPitchDeg: (bounds.east - bounds.west) / cols,
					dmHalo: Math.max(0, halo),
					dmOcean: clamp(ocean, 0, 1),
					dmRound: (shape ?? dots) === "round" ? 1 : 0,
					dmColoured: plan.coloured ? 1 : 0,
				}}
			/>
			{(labels ? labelled : []).map(({ marker, uv }) => (
				<span
					key={`${marker.at[0]},${marker.at[1]}`}
					aria-hidden
					className="pointer-events-none absolute -translate-y-1/2 pl-2 font-mono text-[10px] leading-none whitespace-nowrap text-ink-2"
					style={{ left: `${uv[0] * 100}%`, top: `${(1 - uv[1]) * 100}%` }}
				>
					{marker.label}
				</span>
			))}
		</div>
	);
}
