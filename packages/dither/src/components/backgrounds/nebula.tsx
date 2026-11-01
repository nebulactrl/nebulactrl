import type { CSSProperties, ReactNode } from "react";
import { DitherShader } from "../../lib/gl";
import {
	type ClearBox,
	clearBoxUniforms,
	MAX_CLEAR_BOXES,
} from "../../lib/gl/clear-box";
import {
	type BackgroundBaseProps,
	clamp,
	cssColor,
	shaderBase,
} from "./common";
import { resolveNebulaPalette } from "./nebula-palette";
import { GLSL_CLEAR, GLSL_MASK, MASK_INDEX, type MaskMode } from "./shared";

// Gas: three layers of fbm. Two fbm calls warp the coordinates; the first
// layer reads the warped plane, a second at a finer scale adds grain and a
// third, large and soft, shapes the lobes. A threshold on the sum leaves empty
// space, and a ridged fbm of the same plane draws the thin filaments. All of
// it comes down to one density, nbDens, which colorField turns into colour:
// thin gas, mid gas, bright knots, then a cyan rim (nbRim) on patches of the
// filaments and the cloud edge.
//
// Stars: two layers of one-cell points on jittered grids sized in CSS pixels,
// so the count does not change with `cell`. One twinkles; the other holds
// still and sits dimmer. A few stars carry a small cross. A star cell is
// always tone 1, so it is never dithered away; twinkle moves its colour
// towards the gas behind it instead of its tone.
//
// Speed: time runs at `speed` (default 0.08), so the rates below are per unit
// of that time, about twelve times the pace you see by default.
const FIELD = /* glsl */ `${GLSL_MASK}${GLSL_CLEAR}
float nbDens;
float nbRim;
float nbStar;

// One star layer on a grid of gs-cell tiles with one jittered star per tile.
// x is the body, y the cross arms; both scaled by the twinkle.
vec2 nbStarLayer(vec2 cell, float gs, float seed, float dens, float crossProb,
                 float twinkle, float t) {
  vec2 id = floor(cell / gs);
  vec2 d = cell - id * gs - (2.0 + floor(hash22(id + seed) * (gs - 4.0)));
  float present = step(hash21(id + seed + 11.7), dens);
  float man = abs(d.x) + abs(d.y);
  float big = step(1.0 - crossProb, hash21(id + seed + 3.3));
  float phase = hash21(id + seed + 5.1) * 6.2831853;
  float rate = mix(7.0, 15.0, hash21(id + seed + 8.8));
  float wave = 0.5 + 0.5 * sin(t * rate + phase);
  float b = 1.0 - twinkle * 0.75 * pow(1.0 - wave, 2.0);
  float body = step(man, 0.5) * mix(0.75, 1.0, big);
  float onAxis = step(abs(d.x * d.y), 0.5);
  float arm1 = step(0.5, man) * step(man, 1.5);
  float arm2 = step(1.5, man) * step(man, 2.5) * onAxis;
  return present * b * vec2(body, big * (arm1 * 0.8 + arm2 * 0.4));
}

float field(vec2 uv, float t) {
  float m = dMask(uv, nbMask, nbMaskSize);
  vec2 par = (u_pointer - 0.5) * nbParallax;
  vec2 p = vec2(uv.x * u_aspect, uv.y);
  // Camera in box heights; the stars are nearer, so they move further.
  vec2 camC = vec2(0.0, nbScroll * 0.14) + par * 0.035;
  vec2 camS = vec2(0.0, nbScroll * 0.34) + par * 0.08;

  // Gas plane: slow drift, and a warp field that wanders on a small circle.
  vec2 q = (p + camC) * 2.2 + vec2(0.2, 0.05) * t;
  vec2 ph = 0.8 * vec2(cos(t * 0.45), sin(t * 0.37));
  vec2 w = vec2(fbmN(q * 0.7 + vec2(1.7, 9.2) + ph, 3),
                fbmN(q * 0.7 + vec2(8.3, 2.8) - ph.yx, 3)) - 0.5;
  vec2 qw = q + w * nbWarp * 4.0;

  float n1 = fbmN(qw, 5);
  float n2 = fbmN(qw * 1.9 + vec2(5.2, 1.3) - w * nbWarp * 2.0, 4);
  float n3 = fbmN(q * 0.42 + vec2(3.1, 6.7) + w, 2);
  float dd = 0.5 + (n1 - 0.5) * 1.5 + (n3 - 0.5) * 0.9 + (n2 - 0.5) * 0.5;

  // Empty space below the threshold; soft lobes above it.
  float thr = mix(0.72, 0.42, clamp(nbDensity, 0.0, 1.0));
  float c = smoothstep(thr, thr + 0.34, dd);

  // Filaments: thin ridges where a third fbm crosses its midline, riding the gas.
  float rd = fbmN(qw * 1.35 + vec2(3.3, 6.1), 4);
  float ridge = pow(max(1.0 - abs(rd - 0.5) * 11.0, 0.0), 6.0);
  float fil = ridge * smoothstep(thr - 0.1, thr + 0.15, dd);

  // Cyan light on a few filaments and along the cloud edge; thin, so it reads as a rim.
  float rimPatch = smoothstep(0.62, 0.85, vnoise(qw * 0.6 + 13.7));
  float edge = smoothstep(0.0, 0.12, c) * (1.0 - smoothstep(0.14, 0.4, c));
  float rim = rimPatch * clamp(fil * 1.1 + edge * 0.35, 0.0, 1.0);

  // Focus: the gas lives in an ellipse (radii as fractions of the box), right
  // of a soft vertical gate and above a soft floor. Stars ignore it, so they
  // cross the whole box.
  float g = 1.0;
  if (nbFocus.z > 0.0) {
    vec2 fd = (uv - nbFocus.xy) / nbFocus.zw;
    g = (1.0 - smoothstep(0.3, 1.0, length(fd)))
      * smoothstep(nbFocusGate.x, nbFocusGate.y, uv.x)
      * smoothstep(nbFocusFloor.x, nbFocusFloor.y, uv.y);
  }

  // Clear boxes keep the gas off text; the stars ignore them, like the focus.
  float clearK = bg2Clear(vec2(uv.x * u_res.x, (1.0 - uv.y) * u_res.y) * u_cell);
  nbDens = clamp(c + fil * 0.4 + rim * 0.3, 0.0, 1.0) * g * clearK;
  nbRim = rim * g * clearK;

  // Stars: tiles of about 44 and 36 CSS px; gas dims them, the mask thins them.
  vec2 cell = floor(uv * u_res) + floor(camS * u_res.y + 0.5);
  float gsA = max(6.0, floor(44.0 / u_cell));
  float gsB = max(6.0, floor(36.0 / u_cell));
  vec2 sa = nbStarLayer(cell, gsA, 1.0, min(0.14 * nbStars * m, 0.9), 0.035, 1.0, t);
  vec2 sb = nbStarLayer(cell + 37.0, gsB, 2.0, min(0.10 * nbStars * m, 0.9), 0.0125, 0.0, t);
  // Glints (the cross arms) only where there is gas; in empty sky a star stays a point.
  float glint = smoothstep(0.08, 0.35, nbDens);
  float star = max(max(sa.x, sa.y * glint), max(sb.x, sb.y * glint) * 0.8) * (1.0 - 0.65 * nbDens);
  nbStar = clamp(star, 0.0, 1.0);

  float cloud = min(nbDens * nbIntensity, 0.92) * m;
  return max(cloud, step(0.12, nbStar));
}
`;

// Palette slots: 0 sky, 1 thin gas, 2 mid gas, 3 knots, 4 rim, 5 stars.
const COLOR_FIELD = /* glsl */ `
vec3 colorField(vec2 uv, float t, float tone) {
  vec3 c = mix(u_pal[1].rgb, u_pal[2].rgb, smoothstep(0.15, 0.7, nbDens));
  c = mix(c, u_pal[3].rgb, smoothstep(0.6, 0.98, nbDens));
  c = mix(c, u_pal[4].rgb, nbRim);
  return mix(c, u_pal[5].rgb, nbStar);
}
`;

export interface DitherNebulaProps extends BackgroundBaseProps {
	/**
	 * Six colours: sky, thin gas, mid gas, the brightest knots, the cyan rim and
	 * the stars. Names, `--token`s or any CSS colour; a short list keeps its
	 * colours and takes the rest from the defaults. Default: paper, deep indigo,
	 * `#6d6bff`, magenta, cyan and ink.
	 */
	palette?: string[] | undefined;
	/** Brightness of the gas, 0 to 1.5. Default 1. */
	intensity?: number | undefined;
	/** How much of the sky the gas fills, 0 (a few wisps) to 1. Default 0.6. */
	density?: number | undefined;
	/** How hard the gas is bent into filaments, 0 (soft blobs) to 2. Default 1. */
	warp?: number | undefined;
	/** How many stars, 0 to 2. Default 1. */
	stars?: number | undefined;
	/** Clears the field where text sits: `edges` keeps the sides, `bottom` clears the lower part. Default `none`. */
	mask?: MaskMode | undefined;
	/** How much the mask clears, 0 to 1. Default 0.5. */
	maskSize?: number | undefined;
	/** Page scroll as 0..1, fed by the caller. Shifts the camera a little, the stars further than the gas. Default 0. */
	scroll?: number | undefined;
	/** Pointer parallax, 0 (off) to 2. The stars follow the pointer more than the gas, both eased. Default 0. */
	parallax?: number | undefined;
	/** Confines the gas to a region and leaves the stars everywhere; see `NebulaFocus`. Default: the gas fills the box. */
	focus?: NebulaFocus | undefined;
}

/**
 * Where the gas lives. `x`, `y` is the centre of an ellipse and `rx`, `ry` its
 * radii, all as 0..1 fractions of the box (`y` from the top). The gas fades
 * to nothing at the ellipse's edge. `from` and `to` (0..1 of the width)
 * add a left-to-right fade: no gas left of `from`, all of it right of `to`.
 * `floor` (0..1 of the height, from the top) and `floorFade` do the same from
 * above: all of the gas above `floor - floorFade`, none at or below `floor`.
 * Stars are never focused; they fill the box.
 */
export interface NebulaFocus {
	x: number;
	y: number;
	rx: number;
	ry: number;
	from?: number | undefined;
	to?: number | undefined;
	floor?: number | undefined;
	floorFade?: number | undefined;
}

/** The field is told clear boxes in CSS px, so one unit is one CSS px. */
const PX_UNITS = { cellCss: 1 } as const;

/** smoothstep(0, 1, t) at a quarter steps of the feather, as the shader eases it. */
const FEATHER_EASE = [0, 0.15625, 0.5, 0.84375, 1] as const;

/**
 * One axis of a box's clearing as a gradient: no gas inside `from`..`to`, the
 * gas easing back in over `feather` px outside it, as the shader's
 * smoothstep does.
 */
function clearAxis(
	direction: "to right" | "to bottom",
	from: number,
	to: number,
	feather: number,
): string {
	const stop = (k: number, at: number) =>
		`rgba(0,0,0,${FEATHER_EASE[k]}) ${at}px`;
	const steps = [4, 3, 2, 1, 0] as const;
	const before = steps.map((k) => stop(k, from - (feather * k) / 4));
	const after = [...steps]
		.reverse()
		.map((k) => stop(k, to + (feather * k) / 4));
	return `linear-gradient(${direction}, ${[...before, ...after].join(", ")})`;
}

/**
 * One clear box as a CSS mask for the fallback: the gas everywhere except the
 * box, with the shader's feathered edge. The clearing is the product of a
 * horizontal and a vertical ramp, so its mask (the gas that is left) is
 * their union (`add` = source-over: a + b - ab). The corners are a little
 * squarer than the shader's rounded ones, and nothing has a hard edge unless
 * the box asks for none (`feather` 0).
 */
function clearBoxMask(box: ClearBox): CSSProperties {
	const image = [
		clearAxis("to right", box.x, box.x + box.w, box.feather),
		clearAxis("to bottom", box.y, box.y + box.h, box.feather),
	].join(", ");
	return {
		WebkitMaskImage: image,
		WebkitMaskComposite: "source-over",
		maskImage: image,
		maskComposite: "add",
	};
}

/** Shown without WebGL2: a few soft washes of the gas colours. */
function NebulaFallback({
	palette,
	focus,
	clear,
}: {
	palette: readonly string[];
	focus?: NebulaFocus | undefined;
	clear?: readonly ClearBox[] | undefined;
}) {
	const [, deep, mid, bright] = palette.map(cssColor);
	const wash = (color: string | undefined, percent: number) =>
		`color-mix(in oklch, ${color} ${percent}%, transparent)`;
	const pct = (value: number) => `${Math.round(value * 100)}%`;
	const layers = focus
		? [
				`radial-gradient(${pct(focus.rx)} ${pct(focus.ry)} at ${pct(focus.x)} ${pct(focus.y)}, ${wash(mid, 26)}, transparent 70%)`,
				`radial-gradient(${pct(focus.rx * 0.5)} ${pct(focus.ry * 0.5)} at ${pct(focus.x)} ${pct(focus.y)}, ${wash(deep, 30)}, transparent 75%)`,
			]
		: [
				`radial-gradient(60% 55% at 28% 38%, ${wash(mid, 34)}, transparent 70%)`,
				`radial-gradient(50% 45% at 72% 62%, ${wash(bright, 24)}, transparent 70%)`,
				`radial-gradient(70% 60% at 55% 30%, ${wash(deep, 40)}, transparent 75%)`,
			];
	const masks: string[] = [];
	if (focus?.from !== undefined && focus.to !== undefined)
		masks.push(
			`linear-gradient(to right, transparent ${pct(focus.from)}, #000 ${pct(focus.to)})`,
		);
	if (focus?.floor !== undefined)
		masks.push(
			`linear-gradient(to bottom, #000 ${pct(focus.floor - (focus.floorFade ?? 0))}, transparent ${pct(focus.floor)})`,
		);
	const mask = masks.length > 0 ? masks.join(", ") : undefined;
	const gas = (
		<div
			className="absolute inset-0"
			style={{
				backgroundImage: layers.join(", "),
				WebkitMaskImage: mask,
				WebkitMaskComposite: "source-in",
				maskImage: mask,
				maskComposite: "intersect",
			}}
		/>
	);
	if (!clear || clear.length === 0) return gas;
	// One wrapper per box, each masking by its own feathered clearing; the
	// alphas multiply, as the shader multiplies its boxes.
	let wrapped: ReactNode = gas;
	for (const box of clear.slice(0, MAX_CLEAR_BOXES).reverse()) {
		wrapped = (
			<div className="absolute inset-0" style={clearBoxMask(box)}>
				{wrapped}
			</div>
		);
	}
	return wrapped;
}

/**
 * A slow nebula in deep space: warped gas with filaments, coloured by density,
 * dithered in fine dots, under two layers of sparse stars.
 */
export function DitherNebula({
	palette,
	intensity = 1,
	density = 0.6,
	warp = 1,
	stars = 1,
	mask = "none",
	maskSize = 0.5,
	scroll = 0,
	parallax = 0,
	cell = 2,
	speed = 0.08,
	focus,
	clear,
	fallback,
	...rest
}: DitherNebulaProps) {
	const colors = resolveNebulaPalette(palette);
	return (
		<DitherShader
			{...shaderBase(
				{
					...rest,
					cell,
					speed,
					fallback: fallback ?? (
						<NebulaFallback palette={colors} focus={focus} clear={clear} />
					),
				},
				// Blue noise leaves no grid in the dots; a full tone range keeps the
				// gas edges soft.
				{ staticTime: 12, matrix: "blue-noise", levels: "full" },
			)}
			field={FIELD}
			colorField={COLOR_FIELD}
			palette={colors}
			pointer={parallax > 0}
			uniforms={{
				nbIntensity: clamp(intensity, 0, 1.5),
				nbDensity: clamp(density, 0, 1),
				nbWarp: clamp(warp, 0, 2),
				nbStars: clamp(stars, 0, 2),
				nbMask: MASK_INDEX[mask],
				nbMaskSize: clamp(maskSize, 0, 1),
				nbScroll: clamp(scroll, 0, 1),
				nbParallax: clamp(parallax, 0, 2),
				nbFocus: focus
					? [
							clamp(focus.x, 0, 1),
							1 - clamp(focus.y, 0, 1),
							Math.max(clamp(focus.rx, 0, 4), 0.001),
							Math.max(clamp(focus.ry, 0, 4), 0.001),
						]
					: [0.5, 0.5, 0, 0],
				nbFocusGate: [focus?.from ?? -1, focus?.to ?? 0],
				nbFocusFloor:
					focus?.floor !== undefined
						? [
								1 - focus.floor,
								1 - (focus.floor - Math.max(focus.floorFade ?? 0, 0.001)),
							]
						: [-1, -0.5],
				...clearBoxUniforms(clear ?? [], PX_UNITS),
			}}
		/>
	);
}
