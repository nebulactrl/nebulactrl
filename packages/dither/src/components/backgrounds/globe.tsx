import { cn } from "cn";
import {
	type KeyboardEvent,
	type PointerEvent,
	useMemo,
	useRef,
	useState,
} from "react";
import { DitherShader } from "../../lib/gl";
import { prefersReducedMotion, useDitherFrame } from "../../lib/motion";
import {
	type BackgroundBaseProps,
	clamp,
	INK,
	PAPER,
	shaderBase,
} from "./common";
import { worldLandTextures } from "./data/land-mask";
import type { LatLon } from "./geo";
import {
	GLOBE_HELPERS,
	globeArcsGlsl,
	globeMarkersGlsl,
	sceneKey,
} from "./geo-glsl";
import {
	clampTiltOffset,
	type GlobeView,
	REST_VIEW,
	smoothSpeed,
	stepGlobe,
	tiltOf,
} from "./globe-motion";
import { BG2_COMMON } from "./glsl";
import { planInks } from "./item-colors";

// Each cell is placed on the unit disc (84% of the shorter side), lifted to
// the sphere, rotated back into world space and looked up in the land mask.
// Palette ramp: paper, `color` (land, sea and atmosphere: tone 0..0.5), then
// `accent` (markers and arcs: tone up to 1). Light is fixed to the viewer, so
// the terminator stays put as the land turns. The component drives rotation
// (gbLon) and the marker clock (gbTime) itself, so a drag can turn and coast
// and the spin can ease back in; the shader's own clock is not used.
const FIELD_MAIN = /* glsl */ `
float field(vec2 uv, float t) {
  gbMk = 0.0;
  gbAk = 0.0;
  gbInk = 0.0;
  float lo = radians(gbLon);
  float ti = radians(gbTilt);
  gbCl = cos(lo);
  gbSl = sin(lo);
  gbCt = cos(ti);
  gbSt = sin(ti);

  float R = 0.42 * min(u_res.x, u_res.y);
  vec2 p = (uv * u_res - 0.5 * u_res) / R;
  float r2 = dot(p, p);
  vec3 L = normalize(vec3(bg2Dir(gbLight) * 0.85, 0.55));
  float tone = 0.0;

  if (r2 <= 1.0) {
    float z = sqrt(1.0 - r2);
    vec3 nv = vec3(p, z);
    vec3 w = gbWorld(nv);
    float lat = asin(clamp(w.y, -1.0, 1.0));
    float lon = atan(w.x, w.z);
    float land = smoothstep(0.35, 0.65, texture(gbLand, vec2(lon / 6.2831853 + 0.5, lat / 3.1415927 + 0.5)).r);
    float lit = dot(nv, L);
    float day = smoothstep(-0.12, 0.35, lit);
    float limb = 1.0 - z;
    float landTone = 0.5 * mix(0.14, 1.0, day) * (1.0 - 0.3 * limb * limb);
    float seaTone = gbOcean * 0.5 * day * (0.35 + 0.9 * limb * limb);
    tone = mix(seaTone, landTone, land);
    tone += gbAtmo * 0.2 * pow(limb, 3.0) * day;
  } else {
    float e = sqrt(r2) - 1.0;
    if (gbAtmo > 0.5 && e < 0.3) {
      float toward = 0.5 + 0.5 * dot(p / sqrt(r2), normalize(L.xy));
      tone = exp(-e * 22.0) * 0.4 * (0.15 + 0.85 * toward * toward);
    }
  }

  if (r2 < 1.7) {
    float m = gbMarkers(p, R, gbTime);
    float a = gbArcs(p, R, gbTime);
    float top = max(m, a);
    if (top > tone) gbInk = m >= a ? gbMk : gbAk;
    tone = max(tone, top);
  }
  // With per-item colours a cell is inked or not and colorField picks its
  // colour, so the half of the ramp that mixes land into accent is spent here.
  return gbColoured > 0.5 ? min(tone * 2.0, 1.0) : tone;
}
`;

// With per-item colours the cell colour comes from the ramp position of
// whatever drew the cell: the land colour, or the marker or arc on top of it.
const COLOR_FIELD = /* glsl */ `
vec3 colorField(vec2 uv, float t, float tone) {
  return paletteColor(gbInk).rgb;
}
`;

export interface GlobeMarker {
	/** `[latitude, longitude]` in degrees. */
	at: LatLon;
	/** Colour of this marker: a name such as "blue", a `--token` or any CSS colour. Default `accent`. */
	color?: string | undefined;
}

export interface GlobeArc {
	from: LatLon;
	to: LatLon;
	/** Colour of this arc. Default `accent`. */
	color?: string | undefined;
}

export interface DitherGlobeProps
	extends Omit<BackgroundBaseProps, "palette" | "speed" | "cell"> {
	/** Colour of the land, sea and atmosphere. Default `--dither-ink`. */
	color?: string | undefined;
	/** Colour of markers and arcs that do not set their own. Default "orange". */
	accent?: string | undefined;
	/** Colour behind the globe. Default `--dither-paper`. */
	background?: string | undefined;
	/** Pulsing locations (up to 24). Markers on the far side are hidden. */
	markers?: readonly GlobeMarker[] | undefined;
	/** Great-circle arcs (up to 8) lifted off the surface, with a stretch of light running along them. */
	arcs?: readonly GlobeArc[] | undefined;
	/** Degrees of rotation per second. Negative spins the other way. Default 6. Holds at 0 when `animate` is off. */
	speed?: number | undefined;
	/** Longitude facing the viewer at the start, in degrees. Default 10. */
	start?: number | undefined;
	/** How far the north pole leans towards the viewer, in degrees. Default 18. */
	tilt?: number | undefined;
	/** Where the light comes from, in degrees clockwise from the top. Default 300. */
	light?: number | undefined;
	/** Brightness of the sea on the lit side, 0 to 1. Default 0.18. */
	ocean?: number | undefined;
	/** A thin dithered glow around the lit rim. Default true. */
	atmosphere?: boolean | undefined;
	/** Drag or use the arrow keys to turn and tilt the globe. It coasts after a drag, then eases back into its idle spin. Default true. */
	draggable?: boolean | undefined;
	/** Size of one dither cell in CSS pixels. Default 2. */
	cell?: number | undefined;
	/** Accessible name when draggable. Say what the globe shows. */
	"aria-label"?: string | undefined;
}

const NO_MARKERS: readonly GlobeMarker[] = [];
const NO_ARCS: readonly GlobeArc[] = [];
const KEY_STEP = { lon: 10, tilt: 8 };
const KEY_TURN: Readonly<Record<string, readonly [number, number]>> = {
	ArrowLeft: [-1, 0],
	ArrowRight: [1, 0],
	ArrowUp: [0, -1],
	ArrowDown: [0, 1],
};

/** A rotating dithered globe with a lit terminator, pulsing markers and arcs. */
export function DitherGlobe({
	color = INK,
	accent = "orange",
	background = PAPER,
	markers = NO_MARKERS,
	arcs = NO_ARCS,
	speed = 6,
	start = 10,
	tilt = 18,
	light = 300,
	ocean = 0.18,
	atmosphere = true,
	draggable = true,
	cell = 2,
	levels,
	animate = true,
	fps,
	className,
	style,
	"aria-label": ariaLabel,
	...rest
}: DitherGlobeProps) {
	const plan = planInks(background, color, accent, [
		...markers.map((marker) => marker.color),
		...arcs.map((arc) => arc.color),
	]);
	const inkMarkers = markers.map((marker, i) => ({
		at: marker.at,
		ink: plan.positions[i],
	}));
	const inkArcs = arcs.map((arc, i) => ({
		from: arc.from,
		to: arc.to,
		ink: plan.positions[markers.length + i],
	}));
	const key = sceneKey(inkMarkers, inkArcs);
	// biome-ignore lint/correctness/useExhaustiveDependencies: `key` is the content of markers and arcs, so inline arrays do not recompile.
	const field = useMemo(
		() =>
			[
				BG2_COMMON,
				GLOBE_HELPERS,
				globeMarkersGlsl(inkMarkers),
				globeArcsGlsl(inkArcs),
				FIELD_MAIN,
			].join("\n"),
		[key],
	);

	const base = shaderBase(
		{ ...rest, levels, cell, fps, animate: false },
		// Per-item colours spend the whole tone range on coverage, so they need half the steps.
		{ staticTime: 0, levels: plan.coloured ? 5 : 9 },
	);

	// The component owns rotation and the marker clock, on the shared frame
	// loop: it pauses off-screen and in a hidden tab, and draws one still frame
	// under reduced motion. The shader itself is held still and only redrawn
	// when these uniforms change.
	const hostRef = useRef<HTMLDivElement>(null);
	const [view, setView] = useState<GlobeView>(REST_VIEW);
	const [time, setTime] = useState(base.staticTime);
	const held = useRef(false);
	const drag = useRef<{
		x: number;
		y: number;
		at: number;
		degPerPx: number;
	} | null>(null);
	useDitherFrame(
		(now, delta, still) => {
			setTime(now);
			if (still) return;
			setView((prev) =>
				stepGlobe(prev, delta, {
					spin: speed,
					held: held.current,
					coast: true,
					tilt,
				}),
			);
		},
		{
			fps,
			enabled: animate,
			target: hostRef,
			stillTime: base.staticTime,
		},
	);

	const turnBy = (dLon: number, dTilt: number) =>
		setView((prev) => ({
			...prev,
			lon: prev.lon + dLon,
			tilt: clampTiltOffset(tilt, prev.tilt + dTilt),
		}));
	const onPointerDown = (event: PointerEvent<HTMLDivElement>) => {
		const rect = event.currentTarget.getBoundingClientRect();
		const radiusPx = 0.42 * Math.min(rect.width, rect.height);
		held.current = true;
		drag.current = {
			x: event.clientX,
			y: event.clientY,
			at: event.timeStamp,
			degPerPx: radiusPx > 0 ? 180 / Math.PI / radiusPx : 0.3,
		};
		// Catching the globe stops a coast.
		setView((prev) => ({ ...prev, vLon: 0, vTilt: 0, gain: 0 }));
		event.currentTarget.setPointerCapture(event.pointerId);
	};
	const onPointerMove = (event: PointerEvent<HTMLDivElement>) => {
		const d = drag.current;
		if (!d) return;
		// Dragging right brings the western land into view: the facing longitude decreases.
		const dLon = -(event.clientX - d.x) * d.degPerPx;
		const dTilt = (event.clientY - d.y) * d.degPerPx;
		const seconds = (event.timeStamp - d.at) / 1000;
		d.x = event.clientX;
		d.y = event.clientY;
		d.at = event.timeStamp;
		setView((prev) => ({
			...prev,
			lon: prev.lon + dLon,
			tilt: clampTiltOffset(tilt, prev.tilt + dTilt),
			vLon: smoothSpeed(prev.vLon, dLon, seconds),
			vTilt: smoothSpeed(prev.vTilt, dTilt, seconds),
		}));
	};
	const onPointerUp = (event: PointerEvent<HTMLDivElement>) => {
		const d = drag.current;
		drag.current = null;
		held.current = false;
		if (event.currentTarget.hasPointerCapture(event.pointerId)) {
			event.currentTarget.releasePointerCapture(event.pointerId);
		}
		// Let go mid-drag and it coasts; holding still before letting go does not,
		// and neither does a globe that is not animating (reduced motion included).
		const rested = d !== null && event.timeStamp - d.at > 90;
		if (rested || !animate || prefersReducedMotion()) {
			setView((prev) => ({ ...prev, vLon: 0, vTilt: 0 }));
		}
	};
	const onKeyDown = (event: KeyboardEvent<HTMLDivElement>) => {
		const delta = KEY_TURN[event.key];
		if (!delta) return;
		event.preventDefault();
		turnBy(delta[0] * KEY_STEP.lon, delta[1] * KEY_STEP.tilt);
	};

	const interactive = draggable
		? {
				role: "img" as const,
				tabIndex: 0,
				"aria-label":
					ariaLabel ?? "Globe. Drag it or use the arrow keys to turn it.",
				onPointerDown,
				onPointerMove,
				onPointerUp,
				onPointerCancel: onPointerUp,
				onKeyDown,
			}
		: { "aria-hidden": true as const };

	return (
		<div
			{...interactive}
			ref={hostRef}
			className={cn(
				"relative isolate forced-colors:hidden",
				draggable &&
					"cursor-grab touch-pan-y select-none outline-none focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-ring active:cursor-grabbing",
				className,
			)}
			style={style}
		>
			<DitherShader
				{...base}
				className="absolute inset-0"
				field={field}
				colorField={plan.coloured ? COLOR_FIELD : undefined}
				textures={worldLandTextures("gbLand")}
				palette={plan.palette}
				uniforms={{
					gbLon: start + view.lon,
					gbTilt: tiltOf(tilt, view.tilt),
					gbTime: time,
					gbLight: light,
					gbOcean: clamp(ocean, 0, 1),
					gbAtmo: atmosphere ? 1 : 0,
					gbColoured: plan.coloured ? 1 : 0,
				}}
			/>
		</div>
	);
}
