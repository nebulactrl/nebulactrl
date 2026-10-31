import { DitherShader } from "../../lib/gl";
import { resolveMatrix } from "./matrix";
import {
	type BackgroundProps,
	DEG,
	type DitherColor,
	GLSL_MASK,
	INK,
	MASK_INDEX,
	PAPER,
} from "./shared";

const FIELD = /* glsl */ `
${GLSL_MASK}
float field(vec2 uv, float t) {
  vec2 c = vec2(0.5 * u_aspect, 0.5);
  vec2 p = vec2(uv.x * u_aspect, uv.y);
  p = c + rot2(auTilt) * (p - c);
  float y = auDown > 0.5 ? 1.0 - p.y : p.y;
  // Horizontal coordinate in box widths so the ray count does not depend on aspect.
  float x = (p.x / u_aspect + auDrift * t * 0.04) * auScale;
  float tone = 0.0;
  for (int k = 0; k < 6; k++) {
    if (float(k) >= auCurtains) break;
    float fk = float(k);
    float edge = auHeight + fk * auSpread
      + auSway * 0.09 * snoise(vec2(x * 1.6 + fk * 3.1, t * 0.07 + fk))
      + auTurb * 0.02 * snoise(vec2(x * 9.0 - t * 0.2, fk * 5.0 + t * 0.13));
    float dy = y - edge;
    // Bright lower edge, then rays that fade as they rise.
    float line = auGlow * exp(-max(dy, 0.0) * 55.0) * smoothstep(-0.012, 0.0, dy);
    float stripe = smoothstep(0.25, 0.85, vnoise(vec2(x * auRays * 0.9 + fk * 7.0, t * 0.1 + fk)));
    float rise = clamp(dy / max(auLength, 0.02), 0.0, 1.0);
    float body = stripe * pow(1.0 - rise, 1.6) * step(0.0, dy) * 0.8;
    float shimmer = 1.0 - auShimmer * 0.5 * (0.5 + 0.5 * sin(t * 1.3 + x * 18.0 + fk * 2.0));
    float v = (line + body) * shimmer * (1.0 - fk * 0.22);
    tone = 1.0 - (1.0 - tone) * (1.0 - clamp(v, 0.0, 1.0));
  }
  return tone * auIntensity * dMask(uv, auMask, 0.5);
}
`;

export type AuroraMask = "none" | "top" | "bottom" | "edges";

export type DitherAuroraProps = BackgroundProps<{
	/** The bright lower edge of each curtain. */
	color?: DitherColor;
	/** The faint tops of the rays. Set a second colour for a two-tone sky. */
	tipColor?: DitherColor;
	/** The sky behind. */
	background?: DitherColor;
	/** How many curtains, 1 to 6. Each hangs higher and dimmer. */
	curtains?: number;
	/** Where the lowest bright edge hangs, 0 (bottom) to 1 (top). */
	height?: number;
	/** Vertical gap between curtains, 0 to 0.3. */
	spread?: number;
	/** How far the edges fold. 0 is straight. */
	sway?: number;
	/** Fine ripples along each edge, 0 to 2. */
	turbulence?: number;
	/** How many rays fit across. */
	rays?: number;
	/** How far the rays reach up, 0 to 1. */
	length?: number;
	/** Brightness of the line along each edge, 0 to 2. */
	glow?: number;
	/** Brightness of the curtains, 0 to 1. */
	intensity?: number;
	/** A slow brightness ripple, 0 to 1. */
	shimmer?: number;
	/** Horizontal zoom. */
	scale?: number;
	/** Tilt in degrees, -45 to 45. */
	tilt?: number;
	/** Sideways travel, -2 to 2. */
	drift?: number;
	/** `down` flips the curtains so the rays hang from the top. */
	direction?: "up" | "down";
	/** Fades the aurora to paper where text sits. */
	mask?: AuroraMask;
}>;

/** Curtains of light that fold, sway and shimmer. */
export function DitherAurora({
	color = INK,
	tipColor,
	background = PAPER,
	curtains = 3,
	height = 0.42,
	spread = 0.11,
	sway = 1,
	turbulence = 0.6,
	rays = 8,
	length = 0.45,
	glow = 0.9,
	intensity = 0.95,
	shimmer = 0.2,
	scale = 1,
	tilt = 0,
	drift = 0,
	direction = "up",
	mask = "none",
	palette,
	matrix,
	n,
	levels,
	stillTime,
	staticTime,
	...rest
}: DitherAuroraProps) {
	const ramp =
		palette ?? (tipColor ? [background, tipColor, color] : [background, color]);
	const tune = resolveMatrix(
		{ matrix, n, levels, stillTime, staticTime },
		{ matrix: "bayer8", staticTime: 40, levels: "full" },
	);
	return (
		<DitherShader
			{...rest}
			field={FIELD}
			palette={ramp}
			{...tune}
			uniforms={{
				auCurtains: curtains,
				auHeight: height,
				auSpread: spread,
				auSway: sway,
				auTurb: turbulence,
				auRays: rays,
				auLength: length,
				auGlow: glow,
				auIntensity: intensity,
				auShimmer: shimmer,
				auScale: scale,
				auTilt: tilt * DEG,
				auDrift: drift,
				auDown: direction === "down" ? 1 : 0,
				auMask: MASK_INDEX[mask],
			}}
		/>
	);
}
