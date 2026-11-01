import { DITHER_COLOR_NAMES } from "../../lib/tokens";
import { INK, inkMix, PAPER } from "./common";
import { DitherDotMap, type DotMapArc, type DotMapMarker } from "./dot-map";
import { DitherEclipse } from "./eclipse";
import { DitherFade, type FadeSide } from "./fade";
import { DitherGlobe, type GlobeArc, type GlobeMarker } from "./globe";
import { DitherHalftone } from "./halftone";
import { DitherHorizon } from "./horizon";
import { DitherMetaballs } from "./metaballs";
import { DitherNebula } from "./nebula";
import { DitherOrb } from "./orb";
import {
	type BackgroundEntry,
	bool,
	type KnobDef,
	MATRIX_OPTIONS,
	matrixOf,
	num,
	str,
} from "./registry";
import { DitherRetroGrid } from "./retro-grid";
import { DitherRidgelines } from "./ridgelines";
import type { MaskMode } from "./shared";
import { DitherStarfield } from "./starfield";
import { DitherTunnel } from "./tunnel";

const FILL = "absolute inset-0";

const range = (
	key: string,
	label: string,
	min: number,
	max: number,
	step: number,
	value: number,
): KnobDef => ({ type: "range", key, label, min, max, step, value });

const select = (
	key: string,
	label: string,
	options: readonly string[],
	value: string,
): KnobDef => ({ type: "select", key, label, options, value });

const toggle = (key: string, label: string, value: boolean): KnobDef => ({
	type: "toggle",
	key,
	label,
	value,
});

const CELL = range("cell", "Cell", 2, 8, 1, 3);
const SPEED = range("speed", "Speed", 0, 3, 0.1, 1);
const MATRIX = select("matrix", "Matrix", MATRIX_OPTIONS, "default");

const LONDON = [51.51, -0.13] as const;
const NEW_YORK = [40.71, -74.01] as const;
const TOKYO = [35.68, 139.69] as const;
const SYDNEY = [-33.87, 151.21] as const;
const SAO_PAULO = [-23.55, -46.63] as const;
const SINGAPORE = [1.35, 103.82] as const;

const GLOBE_MARKERS: readonly GlobeMarker[] = [
	{ at: LONDON },
	{ at: NEW_YORK },
	{ at: TOKYO },
	{ at: SYDNEY },
	{ at: SAO_PAULO },
];
const GLOBE_MARKERS_COLOURED: readonly GlobeMarker[] = [
	{ at: LONDON, color: "blue" },
	{ at: NEW_YORK, color: "green" },
	{ at: TOKYO, color: "pink" },
	{ at: SYDNEY },
	{ at: SAO_PAULO, color: "purple" },
];
const GLOBE_ARCS_COLOURED: readonly GlobeArc[] = [
	{ from: LONDON, to: TOKYO, color: "blue" },
	{ from: NEW_YORK, to: LONDON, color: "green" },
];
const GLOBE_ARCS: readonly GlobeArc[] = [
	{ from: LONDON, to: TOKYO },
	{ from: NEW_YORK, to: LONDON },
	{ from: SINGAPORE, to: SYDNEY },
];
const MAP_MARKERS: readonly DotMapMarker[] = [
	{ at: LONDON, label: "London" },
	{ at: NEW_YORK, label: "New York" },
	{ at: TOKYO, label: "Tokyo" },
	{ at: SYDNEY, label: "Sydney" },
	{ at: SAO_PAULO, label: "São Paulo" },
];
/** The same cities with a weight each, as a traffic map would pass them. */
const MAP_MARKERS_WEIGHTED: readonly DotMapMarker[] = [
	{ at: LONDON, label: "London", weight: 1 },
	{ at: NEW_YORK, label: "New York", weight: 0.7 },
	{ at: TOKYO, label: "Tokyo", weight: 0.45 },
	{ at: SYDNEY, label: "Sydney", weight: 0.2 },
	{ at: SAO_PAULO, label: "São Paulo", weight: 0 },
];
const MAP_MARKERS_COLOURED: readonly DotMapMarker[] = [
	{ at: LONDON, label: "London", color: "blue" },
	{ at: NEW_YORK, label: "New York", color: "green" },
	{ at: TOKYO, label: "Tokyo", color: "pink" },
	{ at: SYDNEY, label: "Sydney" },
	{ at: SAO_PAULO, label: "São Paulo", color: "purple" },
];
const MAP_ARCS_COLOURED: readonly DotMapArc[] = [
	{ from: LONDON, to: TOKYO, color: "blue" },
	{ from: NEW_YORK, to: LONDON, color: "green" },
	{ from: SINGAPORE, to: SYDNEY },
];
const MAP_ARCS: readonly DotMapArc[] = [
	{ from: LONDON, to: TOKYO },
	{ from: NEW_YORK, to: LONDON },
	{ from: SINGAPORE, to: SYDNEY },
];

/** Four warm colours for the retro sunset. */
const SUNSET = [
	"oklch(0.18 0.05 300)",
	"oklch(0.4 0.17 330)",
	"oklch(0.64 0.2 10)",
	"oklch(0.86 0.15 75)",
];

const INKS = [
	"--dither-series-1",
	"--dither-series-2",
	"--dither-series-3",
] as const;

/** The part-2 backgrounds: ridgelines through fade. */
export const BACKGROUNDS_2: readonly BackgroundEntry[] = [
	{
		id: "ridgelines",
		name: "Ridgelines",
		knobs: [
			range("lines", "Lines", 6, 64, 1, 32),
			range("amplitude", "Amplitude", 0.2, 3, 0.1, 1),
			range("spread", "Spread", 0.1, 1, 0.05, 0.5),
			range("shade", "Shade", 0, 0.5, 0.05, 0.25),
			range("cell", "Cell", 1, 6, 1, 2),
			SPEED,
			MATRIX,
		],
		render: (v) => (
			<DitherRidgelines
				matrix={matrixOf(v)}
				className={FILL}
				lines={num(v, "lines", 32)}
				amplitude={num(v, "amplitude", 1)}
				spread={num(v, "spread", 0.5)}
				shade={num(v, "shade", 0.25)}
				cell={num(v, "cell", 2)}
				speed={num(v, "speed", 1)}
			/>
		),
	},
	{
		id: "metaballs",
		name: "Metaballs",
		knobs: [
			select("variant", "Variant", ["solid", "rings"], "solid"),
			range("count", "Count", 2, 8, 1, 5),
			range("size", "Size", 0.4, 2, 0.05, 1),
			CELL,
			SPEED,
			MATRIX,
		],
		render: (v) => (
			<DitherMetaballs
				matrix={matrixOf(v)}
				className={FILL}
				variant={str<"solid" | "rings">(v, "variant", "solid")}
				count={num(v, "count", 5)}
				size={num(v, "size", 1)}
				cell={num(v, "cell", 3)}
				speed={num(v, "speed", 1)}
			/>
		),
	},
	{
		id: "tunnel",
		name: "Tunnel",
		knobs: [
			select("profile", "Profile", ["circle", "square"], "circle"),
			range("rings", "Rings", 0.3, 2.5, 0.1, 1),
			range("spokes", "Spokes", 0, 32, 1, 12),
			range("centerX", "Centre x", 0, 1, 0.05, 0.5),
			range("centerY", "Centre y", 0, 1, 0.05, 0.5),
			range("sway", "Sway", 0, 2, 0.1, 1),
			range("fog", "Fog", 0.5, 3, 0.1, 1),
			CELL,
			SPEED,
			MATRIX,
		],
		render: (v) => (
			<DitherTunnel
				matrix={matrixOf(v)}
				className={FILL}
				profile={str<"circle" | "square">(v, "profile", "circle")}
				rings={num(v, "rings", 1)}
				spokes={num(v, "spokes", 12)}
				center={[num(v, "centerX", 0.5), num(v, "centerY", 0.5)]}
				sway={num(v, "sway", 1)}
				fog={num(v, "fog", 1)}
				cell={num(v, "cell", 3)}
				speed={num(v, "speed", 1)}
			/>
		),
	},
	{
		id: "retro-grid",
		name: "Retro grid",
		knobs: [
			range("horizon", "Horizon", 0.2, 0.7, 0.02, 0.42),
			range("sun", "Sun", 0, 0.8, 0.05, 0.4),
			range("columns", "Columns", 4, 24, 1, 12),
			toggle("sunset", "Sunset palette", false),
			range("cell", "Cell", 2, 8, 1, 4),
			SPEED,
			MATRIX,
		],
		render: (v) => (
			<DitherRetroGrid
				matrix={matrixOf(v)}
				className={FILL}
				horizon={num(v, "horizon", 0.42)}
				sun={num(v, "sun", 0.4)}
				columns={num(v, "columns", 12)}
				palette={bool(v, "sunset", false) ? [...SUNSET] : undefined}
				cell={num(v, "cell", 4)}
				speed={num(v, "speed", 1)}
			/>
		),
	},
	{
		id: "eclipse",
		name: "Eclipse",
		knobs: [
			range("centerX", "Centre x", 0, 1, 0.05, 0.5),
			range("centerY", "Centre y", 0, 1, 0.05, 0.5),
			range("size", "Size", 0.08, 0.4, 0.01, 0.24),
			range("corona", "Corona", 0, 1, 0.05, 0.5),
			range("light", "Light", 0, 360, 5, 315),
			range("pulse", "Pulse", 0, 1, 0.05, 0.5),
			CELL,
			SPEED,
			MATRIX,
		],
		render: (v) => (
			<DitherEclipse
				matrix={matrixOf(v)}
				className={FILL}
				center={[num(v, "centerX", 0.5), num(v, "centerY", 0.5)]}
				size={num(v, "size", 0.24)}
				corona={num(v, "corona", 0.5)}
				light={num(v, "light", 315)}
				pulse={num(v, "pulse", 0.5)}
				cell={num(v, "cell", 3)}
				speed={num(v, "speed", 1)}
			/>
		),
	},
	{
		id: "horizon",
		name: "Horizon",
		knobs: [
			range("horizon", "Horizon", 0.1, 0.6, 0.02, 0.28),
			range("curve", "Curve", 0, 1, 0.05, 0.6),
			range("glow", "Glow", 0, 1, 0.05, 1),
			range("aurora", "Aurora", 0, 1, 0.05, 1),
			range("stars", "Stars", 0, 3, 0.1, 1),
			CELL,
			SPEED,
			MATRIX,
		],
		render: (v) => (
			<DitherHorizon
				matrix={matrixOf(v)}
				className={FILL}
				horizon={num(v, "horizon", 0.28)}
				curve={num(v, "curve", 0.6)}
				glow={num(v, "glow", 1)}
				aurora={num(v, "aurora", 1)}
				stars={num(v, "stars", 1)}
				cell={num(v, "cell", 3)}
				speed={num(v, "speed", 1)}
			/>
		),
	},
	{
		id: "starfield",
		name: "Starfield",
		knobs: [
			range("density", "Density", 0, 2, 0.1, 1),
			range("twinkle", "Twinkle", 0, 1, 0.05, 0.6),
			range("angle", "Angle", 0, 360, 5, 270),
			range("drift", "Drift", 0, 6, 0.2, 1.6),
			range("nebula", "Nebula", 0, 1, 0.05, 0.5),
			range("cell", "Cell", 1, 6, 1, 2),
			SPEED,
			MATRIX,
		],
		render: (v) => (
			<DitherStarfield
				matrix={matrixOf(v)}
				className={FILL}
				density={num(v, "density", 1)}
				twinkle={num(v, "twinkle", 0.6)}
				angle={num(v, "angle", 270)}
				drift={num(v, "drift", 1.6)}
				nebula={num(v, "nebula", 0.5)}
				cell={num(v, "cell", 2)}
				speed={num(v, "speed", 1)}
			/>
		),
	},
	{
		id: "nebula",
		name: "Nebula",
		knobs: [
			range("intensity", "Intensity", 0, 1.5, 0.05, 1),
			range("density", "Density", 0, 1, 0.05, 0.6),
			range("warp", "Warp", 0, 2, 0.05, 1),
			range("stars", "Stars", 0, 2, 0.1, 1),
			select(
				"mask",
				"Mask",
				["none", "center", "top", "bottom", "edges"],
				"none",
			),
			range("parallax", "Parallax", 0, 2, 0.1, 0),
			range("cell", "Cell", 1, 6, 1, 2),
			range("speed", "Speed", 0, 1, 0.01, 0.08),
			MATRIX,
		],
		render: (v) => (
			<DitherNebula
				matrix={matrixOf(v)}
				className={FILL}
				intensity={num(v, "intensity", 1)}
				density={num(v, "density", 0.6)}
				warp={num(v, "warp", 1)}
				stars={num(v, "stars", 1)}
				mask={str<MaskMode>(v, "mask", "none")}
				parallax={num(v, "parallax", 0)}
				cell={num(v, "cell", 2)}
				speed={num(v, "speed", 0.08)}
			/>
		),
	},
	{
		id: "halftone",
		name: "Halftone",
		knobs: [
			select("inks", "Inks", ["1", "2", "3"], "1"),
			select("source", "Source", ["noise", "radial", "linear"], "noise"),
			range("centerX", "Centre x", 0, 1, 0.05, 0.5),
			range("centerY", "Centre y", 0, 1, 0.05, 0.5),
			range("radius", "Radius", 0.1, 1.5, 0.05, 1),
			range("direction", "Direction", 0, 360, 5, 0),
			range("size", "Dot pitch", 4, 24, 1, 10),
			range("angle", "Angle", 0, 90, 5, 45),
			range("intensity", "Intensity", 0.2, 1, 0.05, 0.9),
			range("scale", "Scale", 0.3, 3, 0.1, 1),
			range("drift", "Drift", 0, 3, 0.1, 1),
			range("cell", "Cell", 1, 4, 1, 1),
			SPEED,
			MATRIX,
		],
		render: (v) => {
			const inks = Number(str(v, "inks", "1"));
			return (
				<DitherHalftone
					matrix={matrixOf(v)}
					className={FILL}
					color={INK}
					channels={
						inks === 2
							? [INKS[0], INKS[1]]
							: inks === 3
								? [INKS[0], INKS[1], INKS[2]]
								: undefined
					}
					source={str<"noise" | "radial" | "linear">(v, "source", "noise")}
					center={[num(v, "centerX", 0.5), num(v, "centerY", 0.5)]}
					radius={num(v, "radius", 1)}
					direction={num(v, "direction", 0)}
					size={num(v, "size", 10)}
					angle={num(v, "angle", 45)}
					intensity={num(v, "intensity", 0.9)}
					scale={num(v, "scale", 1)}
					drift={num(v, "drift", 1)}
					cell={num(v, "cell", 1)}
					speed={num(v, "speed", 1)}
				/>
			);
		},
	},
	{
		id: "globe",
		name: "Globe",
		knobs: [
			range("spin", "Spin (°/s)", -30, 30, 1, 6),
			range("start", "Start longitude", -180, 180, 5, 10),
			range("tilt", "Tilt", -30, 70, 1, 18),
			range("light", "Light", 0, 360, 5, 300),
			range("ocean", "Ocean", 0, 1, 0.02, 0.18),
			toggle("atmosphere", "Atmosphere", true),
			toggle("markers", "Markers", true),
			toggle("arcs", "Arcs", true),
			toggle("colours", "Per-item colours", false),
			select("accent", "Accent", [...DITHER_COLOR_NAMES, "ink"], "orange"),
			toggle("draggable", "Draggable", true),
			range("cell", "Cell", 1, 5, 1, 2),
			MATRIX,
		],
		render: (v) => (
			<DitherGlobe
				matrix={matrixOf(v)}
				className={FILL}
				markers={
					bool(v, "markers", true)
						? bool(v, "colours", false)
							? GLOBE_MARKERS_COLOURED
							: GLOBE_MARKERS
						: undefined
				}
				arcs={
					bool(v, "arcs", true)
						? bool(v, "colours", false)
							? GLOBE_ARCS_COLOURED
							: GLOBE_ARCS
						: undefined
				}
				accent={str(v, "accent", "orange")}
				speed={num(v, "spin", 6)}
				start={num(v, "start", 10)}
				tilt={num(v, "tilt", 18)}
				light={num(v, "light", 300)}
				ocean={num(v, "ocean", 0.18)}
				atmosphere={bool(v, "atmosphere", true)}
				draggable={bool(v, "draggable", true)}
				cell={num(v, "cell", 2)}
				aria-label="World globe with offices in five cities. Drag it or use the arrow keys to turn it."
			/>
		),
	},
	{
		id: "dot-map",
		name: "Dot map",
		knobs: [
			range("columns", "Columns", 60, 260, 5, 160),
			select("dots", "Dots", ["square", "round"], "square"),
			range("halo", "Halo (°)", 0, 30, 1, 14),
			range("ocean", "Ocean", 0, 1, 0.05, 0),
			toggle("markers", "Markers", true),
			toggle("weights", "Marker weights", false),
			toggle("colours", "Per-item colours", false),
			toggle("arcs", "Arcs", true),
			select("accent", "Accent", ["ink", ...DITHER_COLOR_NAMES], "ink"),
			range("cell", "Cell", 1, 4, 1, 1),
			SPEED,
			MATRIX,
		],
		render: (v) => (
			<div className="absolute inset-0 flex items-center justify-center p-4">
				<DitherDotMap
					matrix={matrixOf(v)}
					className="w-full max-h-full"
					markers={
						bool(v, "markers", true)
							? bool(v, "colours", false)
								? MAP_MARKERS_COLOURED
								: bool(v, "weights", false)
									? MAP_MARKERS_WEIGHTED
									: MAP_MARKERS
							: undefined
					}
					arcs={
						bool(v, "arcs", true)
							? bool(v, "colours", false)
								? MAP_ARCS_COLOURED
								: MAP_ARCS
							: undefined
					}
					accent={str(v, "accent", "ink")}
					columns={num(v, "columns", 160)}
					dots={str<"square" | "round">(v, "dots", "square")}
					halo={num(v, "halo", 14)}
					ocean={num(v, "ocean", 0)}
					cell={num(v, "cell", 1)}
					speed={num(v, "speed", 1)}
					title="World map with offices in five cities"
				/>
			</div>
		),
	},
	{
		id: "orb",
		name: "Orb",
		knobs: [
			range("size", "Size", 0.1, 0.45, 0.01, 0.26),
			range("morph", "Morph", 0, 2, 0.1, 1),
			range("lean", "Lean", 0, 2, 0.1, 1),
			range("glow", "Glow", 0, 1, 0.05, 0.5),
			toggle("highlight", "Highlight", true),
			CELL,
			SPEED,
			MATRIX,
		],
		render: (v) => (
			<DitherOrb
				matrix={matrixOf(v)}
				className={FILL}
				highlight={
					bool(v, "highlight", true)
						? `color-mix(in oklch, var(${INK}) 30%, white)`
						: undefined
				}
				size={num(v, "size", 0.26)}
				morph={num(v, "morph", 1)}
				lean={num(v, "lean", 1)}
				glow={num(v, "glow", 0.5)}
				cell={num(v, "cell", 3)}
				speed={num(v, "speed", 1)}
			/>
		),
	},
	{
		id: "fade",
		name: "Fade",
		knobs: [
			select("side", "Side", ["top", "bottom", "left", "right"], "bottom"),
			range("size", "Size", 40, 400, 10, 200),
			CELL,
			toggle("animate", "Drift (WebGL)", false),
			MATRIX,
		],
		render: (v) => (
			<div className={`${FILL} overflow-hidden`}>
				<div
					className={FILL}
					style={{
						background: `repeating-linear-gradient(135deg, ${inkMix(70)} 0 14px, transparent 14px 28px)`,
					}}
				/>
				<div className="absolute inset-0 flex items-center justify-center px-6 text-center">
					<p className="type-section text-ink-1">
						Content that fades into the page
					</p>
				</div>
				<DitherFade
					matrix={matrixOf(v)}
					side={str<FadeSide>(v, "side", "bottom")}
					size={num(v, "size", 200)}
					cell={num(v, "cell", 3)}
					animate={bool(v, "animate", false)}
					color={PAPER}
				/>
			</div>
		),
	},
];
