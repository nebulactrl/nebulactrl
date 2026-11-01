import type { AuroraMask } from "./aurora";
import { DitherAurora } from "./aurora";
import { DitherBeams } from "./beams";
import { DitherContours } from "./contours";
import { DitherGlow, GLOW_POSITIONS, type GlowPosition } from "./glow";
import { DitherGradient, type GradientMode } from "./gradient";
import { DitherMeshGradient } from "./mesh-gradient";
import { DitherNoiseField, type NoiseFieldVariant } from "./noise-field";
import { DitherPlasma } from "./plasma";
import {
	type BackgroundEntry,
	bool,
	type KnobDef,
	MATRIX_OPTIONS,
	matrixOf,
	num,
	str,
} from "./registry";
import { DitherRipples, type RippleSource } from "./ripples";
import type { MaskMode } from "./shared";
import { DitherSilk } from "./silk";
import { DitherSmoke } from "./smoke";
import { DitherWaves } from "./waves";

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

/** Up to four ripple sources, spread so adding one is visible. */
const RIPPLE_POINTS: readonly RippleSource[] = [
	[0.28, 0.38],
	[0.74, 0.66],
	[0.5, 0.15],
	[0.2, 0.85],
];

export const BACKGROUNDS_1: readonly BackgroundEntry[] = [
	{
		id: "gradient",
		name: "Gradient",
		knobs: [
			select("mode", "Mode", ["linear", "radial", "conic"], "linear"),
			range("angle", "Angle", 0, 360, 5, 160),
			range("centerX", "Centre x", 0, 1, 0.05, 0.5),
			range("centerY", "Centre y", 0, 1, 0.05, 0.5),
			range("radius", "Radius", 0.1, 1.5, 0.05, 1),
			range("from", "From", 0, 1, 0.05, 0),
			range("to", "To", 0, 1, 0.05, 1),
			range("noise", "Noise", 0, 0.6, 0.02, 0.12),
			CELL,
			SPEED,
			MATRIX,
		],
		render: (v) => (
			<DitherGradient
				matrix={matrixOf(v)}
				className={FILL}
				mode={str<GradientMode>(v, "mode", "linear")}
				angle={num(v, "angle", 160)}
				center={[num(v, "centerX", 0.5), num(v, "centerY", 0.5)]}
				radius={num(v, "radius", 1)}
				from={num(v, "from", 0)}
				to={num(v, "to", 1)}
				noise={num(v, "noise", 0.12)}
				cell={num(v, "cell", 3)}
				speed={num(v, "speed", 1)}
			/>
		),
	},
	{
		id: "glow",
		name: "Glow",
		knobs: [
			select("position", "Position", GLOW_POSITIONS, "top"),
			range("radius", "Radius", 0.1, 1.5, 0.05, 0.7),
			range("intensity", "Intensity", 0, 1, 0.05, 0.75),
			CELL,
			SPEED,
			MATRIX,
		],
		render: (v) => (
			<DitherGlow
				matrix={matrixOf(v)}
				className={FILL}
				position={str<GlowPosition>(v, "position", "top")}
				radius={num(v, "radius", 0.7)}
				intensity={num(v, "intensity", 0.75)}
				cell={num(v, "cell", 3)}
				speed={num(v, "speed", 1)}
			/>
		),
	},
	{
		id: "mesh-gradient",
		name: "Mesh gradient",
		knobs: [
			range("blobs", "Blobs", 3, 5, 1, 4),
			range("size", "Size", 0.2, 1.2, 0.05, 0.6),
			CELL,
			SPEED,
			MATRIX,
		],
		render: (v) => {
			const blobs = num(v, "blobs", 4);
			return (
				<DitherMeshGradient
					matrix={matrixOf(v)}
					className={FILL}
					blobs={blobs === 3 ? 3 : blobs === 5 ? 5 : 4}
					size={num(v, "size", 0.6)}
					cell={num(v, "cell", 3)}
					speed={num(v, "speed", 1)}
				/>
			);
		},
	},
	{
		id: "aurora",
		name: "Aurora",
		knobs: [
			range("curtains", "Curtains", 1, 6, 1, 3),
			range("height", "Height", 0, 1, 0.02, 0.42),
			range("spread", "Spread", 0, 0.3, 0.01, 0.11),
			range("sway", "Sway", 0, 3, 0.1, 1),
			range("turbulence", "Turbulence", 0, 2, 0.1, 0.6),
			range("rays", "Rays", 2, 20, 1, 8),
			range("length", "Length", 0, 1, 0.05, 0.45),
			range("glow", "Glow", 0, 2, 0.1, 0.9),
			range("intensity", "Intensity", 0, 1, 0.05, 0.95),
			range("shimmer", "Shimmer", 0, 1, 0.05, 0.2),
			range("scale", "Scale", 0.4, 3, 0.1, 1),
			range("tilt", "Tilt", -45, 45, 1, 0),
			range("drift", "Drift", -2, 2, 0.1, 0),
			toggle("twoTone", "Tip colour", true),
			select("direction", "Direction", ["up", "down"], "up"),
			select("mask", "Mask", ["none", "top", "bottom", "edges"], "none"),
			CELL,
			SPEED,
			MATRIX,
		],
		render: (v) => (
			<DitherAurora
				matrix={matrixOf(v)}
				className={FILL}
				curtains={num(v, "curtains", 3)}
				height={num(v, "height", 0.42)}
				spread={num(v, "spread", 0.11)}
				sway={num(v, "sway", 1)}
				turbulence={num(v, "turbulence", 0.6)}
				rays={num(v, "rays", 8)}
				length={num(v, "length", 0.45)}
				glow={num(v, "glow", 0.9)}
				intensity={num(v, "intensity", 0.95)}
				shimmer={num(v, "shimmer", 0.2)}
				scale={num(v, "scale", 1)}
				tilt={num(v, "tilt", 0)}
				drift={num(v, "drift", 0)}
				{...(bool(v, "twoTone", true) ? { tipColor: "--chart-2" } : {})}
				direction={str<"up" | "down">(v, "direction", "up")}
				mask={str<AuroraMask>(v, "mask", "none")}
				cell={num(v, "cell", 3)}
				speed={num(v, "speed", 1)}
			/>
		),
	},
	{
		id: "noise-field",
		name: "Noise field",
		knobs: [
			select(
				"variant",
				"Style",
				["smoke", "ridges", "contours", "marble"],
				"smoke",
			),
			range("scale", "Scale", 0.3, 5, 0.1, 1.3),
			range("warp", "Warp", 0, 4, 0.1, 1.7),
			range("detail", "Detail", 1, 6, 1, 4),
			range("contrast", "Contrast", 0.5, 3, 0.1, 1.2),
			range("coverage", "Coverage", -0.5, 0.5, 0.05, 0),
			range("intensity", "Intensity", 0, 1, 0.05, 0.9),
			range("bands", "Bands", 2, 24, 1, 8),
			range("angle", "Angle", 0, 360, 5, 90),
			range("flow", "Flow", 0, 4, 0.1, 1),
			toggle("invert", "Invert", false),
			toggle("highlight", "Highlight", false),
			select(
				"mask",
				"Mask",
				["none", "center", "top", "bottom", "edges"],
				"center",
			),
			range("maskSize", "Mask size", 0, 1, 0.05, 0.5),
			CELL,
			SPEED,
			MATRIX,
		],
		render: (v) => (
			<DitherNoiseField
				matrix={matrixOf(v)}
				className={FILL}
				variant={str<NoiseFieldVariant>(v, "variant", "smoke")}
				scale={num(v, "scale", 1.3)}
				warp={num(v, "warp", 1.7)}
				detail={num(v, "detail", 4)}
				contrast={num(v, "contrast", 1.2)}
				coverage={num(v, "coverage", 0)}
				intensity={num(v, "intensity", 0.9)}
				bands={num(v, "bands", 8)}
				angle={num(v, "angle", 90)}
				flow={num(v, "flow", 1)}
				invert={bool(v, "invert", false)}
				{...(bool(v, "highlight", false) ? { highlight: "--chart-2" } : {})}
				mask={str<MaskMode>(v, "mask", "center")}
				maskSize={num(v, "maskSize", 0.5)}
				cell={num(v, "cell", 3)}
				speed={num(v, "speed", 1)}
			/>
		),
	},
	{
		id: "waves",
		name: "Waves",
		knobs: [
			select("variant", "Style", ["ocean", "lines"], "ocean"),
			range("horizon", "Horizon", 0.2, 0.9, 0.02, 0.62),
			range("layers", "Layers", 2, 8, 1, 6),
			range("amplitude", "Amplitude", 0.2, 2.5, 0.1, 1),
			CELL,
			SPEED,
			MATRIX,
		],
		render: (v) => (
			<DitherWaves
				matrix={matrixOf(v)}
				className={FILL}
				variant={str<"ocean" | "lines">(v, "variant", "ocean")}
				horizon={num(v, "horizon", 0.62)}
				layers={num(v, "layers", 6)}
				amplitude={num(v, "amplitude", 1)}
				cell={num(v, "cell", 3)}
				speed={num(v, "speed", 1)}
			/>
		),
	},
	{
		id: "plasma",
		name: "Plasma",
		knobs: [
			range("scale", "Scale", 0.3, 3, 0.1, 1),
			range("warp", "Warp", 0, 1, 0.05, 0.3),
			range("bands", "Bands", 0.5, 4, 0.1, 1.5),
			range("intensity", "Intensity", 0, 1, 0.05, 1),
			CELL,
			SPEED,
			MATRIX,
		],
		render: (v) => (
			<DitherPlasma
				matrix={matrixOf(v)}
				className={FILL}
				scale={num(v, "scale", 1)}
				warp={num(v, "warp", 0.3)}
				bands={num(v, "bands", 1.5)}
				intensity={num(v, "intensity", 1)}
				cell={num(v, "cell", 3)}
				speed={num(v, "speed", 1)}
			/>
		),
	},
	{
		id: "beams",
		name: "Beams",
		knobs: [
			range("originX", "Origin x", -0.5, 1.5, 0.02, 0.18),
			range("originY", "Origin y", -1, 1.5, 0.02, -0.12),
			range("angle", "Angle", 0, 360, 5, 160),
			range("spread", "Spread", 10, 160, 2, 80),
			range("rays", "Rays", 2, 20, 1, 9),
			range("sweep", "Sweep", 0, 30, 1, 6),
			range("reach", "Reach", 0.1, 1.5, 0.05, 1),
			CELL,
			SPEED,
			MATRIX,
		],
		render: (v) => (
			<DitherBeams
				matrix={matrixOf(v)}
				className={FILL}
				origin={[num(v, "originX", 0.18), num(v, "originY", -0.12)]}
				angle={num(v, "angle", 160)}
				spread={num(v, "spread", 80)}
				rays={num(v, "rays", 9)}
				sweep={num(v, "sweep", 6)}
				reach={num(v, "reach", 1)}
				cell={num(v, "cell", 3)}
				speed={num(v, "speed", 1)}
			/>
		),
	},
	{
		id: "silk",
		name: "Silk",
		knobs: [
			range("folds", "Folds", 2, 14, 1, 6),
			range("angle", "Angle", 0, 360, 5, 110),
			range("sheen", "Sheen", 0, 1, 0.05, 0.6),
			range("intensity", "Intensity", 0, 1, 0.05, 1),
			CELL,
			SPEED,
			MATRIX,
		],
		render: (v) => (
			<DitherSilk
				matrix={matrixOf(v)}
				className={FILL}
				folds={num(v, "folds", 6)}
				angle={num(v, "angle", 110)}
				sheen={num(v, "sheen", 0.6)}
				intensity={num(v, "intensity", 1)}
				cell={num(v, "cell", 3)}
				speed={num(v, "speed", 1)}
			/>
		),
	},
	{
		id: "smoke",
		name: "Smoke",
		knobs: [
			range("scale", "Scale", 0.3, 3, 0.1, 1),
			range("height", "Height", 0.1, 1, 0.05, 0.8),
			range("wind", "Wind", -1, 1, 0.05, 0.2),
			range("density", "Density", 0, 1.5, 0.05, 1),
			CELL,
			SPEED,
			MATRIX,
		],
		render: (v) => (
			<DitherSmoke
				matrix={matrixOf(v)}
				className={FILL}
				scale={num(v, "scale", 1)}
				height={num(v, "height", 0.8)}
				wind={num(v, "wind", 0.2)}
				density={num(v, "density", 1)}
				cell={num(v, "cell", 3)}
				speed={num(v, "speed", 1)}
			/>
		),
	},
	{
		id: "contours",
		name: "Contours",
		knobs: [
			range("lines", "Lines", 4, 32, 1, 14),
			range("scale", "Scale", 0.3, 3, 0.1, 1),
			range("index", "Index every", 0, 10, 1, 5),
			range("relief", "Relief", 0, 1, 0.05, 0.5),
			range("cell", "Cell", 2, 8, 1, 2),
			SPEED,
			MATRIX,
		],
		render: (v) => (
			<DitherContours
				matrix={matrixOf(v)}
				className={FILL}
				lines={num(v, "lines", 14)}
				scale={num(v, "scale", 1)}
				index={num(v, "index", 5)}
				relief={num(v, "relief", 0.5)}
				cell={num(v, "cell", 2)}
				speed={num(v, "speed", 1)}
			/>
		),
	},
	{
		id: "ripples",
		name: "Ripples",
		knobs: [
			range("sources", "Sources", 1, 4, 1, 2),
			range("rings", "Rings", 3, 30, 1, 12),
			range("decay", "Decay", 0, 3, 0.1, 1),
			range("intensity", "Intensity", 0, 1, 0.05, 1),
			CELL,
			SPEED,
			MATRIX,
		],
		render: (v) => (
			<DitherRipples
				matrix={matrixOf(v)}
				className={FILL}
				sources={RIPPLE_POINTS.slice(0, num(v, "sources", 2))}
				rings={num(v, "rings", 12)}
				decay={num(v, "decay", 1)}
				intensity={num(v, "intensity", 1)}
				cell={num(v, "cell", 3)}
				speed={num(v, "speed", 1)}
			/>
		),
	},
];
