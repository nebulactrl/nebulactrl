import { DitherShader } from "../../lib/gl";
import {
	type BackgroundBaseProps,
	clamp,
	INK,
	inkMix,
	PAPER,
	shaderBase,
} from "./common";
import { BG2_COMMON } from "./glsl";

// Stars sit on three grids (near, middle, far). Each star is one cell, snapped
// to a palette stop (tone exactly 0.5 or 1.0 on a three-colour ramp) so it
// never flickers between dither thresholds as it moves. Twinkle dips a star
// one stop for a few seconds. Near stars carry a small cross. Dust is a soft
// fbm haze below the first stop.
const FIELD = /* glsl */ `${BG2_COMMON}
float sfLayer(vec2 pix, vec2 dir, float drift, float gs, float seed,
              float dens, float hiProb, float crossOn, float t) {
  vec2 q = pix - dir * drift * t;
  vec2 id = floor(q / gs);
  vec2 lc = q - id * gs;
  vec2 sp = 1.0 + floor(hash22(id + seed) * (gs - 2.0));
  float present = step(hash21(id + seed + 11.7), dens);
  vec2 d = floor(lc) - sp;
  float manhattan = abs(d.x) + abs(d.y);
  float cls = step(1.0 - hiProb, hash21(id + seed + 3.3));
  float phase = hash21(id + seed + 5.1) * 6.2831853;
  float period = mix(3.0, 12.0, hash21(id + seed + 8.8));
  float wave = 0.5 + 0.5 * sin(t * 6.2831853 / period + phase);
  float dim = step(wave, 0.5 * sfTwinkle);
  float hiTone = mix(sfLo, 1.0, cls);
  float loTone = mix(0.0, sfLo, cls);
  float tone = (manhattan < 0.5) ? mix(hiTone, loTone, dim) : 0.0;
  float arm = crossOn * cls * (1.0 - dim) * step(manhattan, 1.5) * step(0.5, manhattan);
  return present * max(tone, arm * sfLo);
}
float field(vec2 uv, float t) {
  vec2 pix = uv * u_res;
  vec2 dir = bg2Dir(sfAngle);
  float dn = clamp(sfDensity, 0.0, 2.0);
  float stars = sfLayer(pix, dir, sfDrift, 12.0, 1.0, min(0.25 * dn, 0.9), 0.8, 1.0, t);
  stars = max(stars, sfLayer(pix, dir, sfDrift * 0.6, 8.0, 2.0, min(0.35 * dn, 0.9), 0.25, 0.0, t));
  stars = max(stars, sfLayer(pix, dir, sfDrift * 0.3, 6.0, 3.0, min(0.4 * dn, 0.9), 0.05, 0.0, t));
  float dust = fbm(pix / (u_res.y * 0.45) - dir * t * 0.01 * sfDrift + vec2(3.7, 1.3));
  float haze = sfNebula * 0.55 * sfLo * smoothstep(0.45, 0.85, dust);
  return max(stars, haze);
}
`;

export interface DitherStarfieldProps extends BackgroundBaseProps {
	/** How many stars, 0 to 2. Default 1. */
	density?: number | undefined;
	/** How much each star pulses, 0 (steady) to 1. Default 0.6. */
	twinkle?: number | undefined;
	/** Direction the stars drift, in degrees as in CSS: 0 is up, 90 right. Default 270. */
	angle?: number | undefined;
	/** Drift speed of the nearest layer in cells per second; the others move at 0.6 and 0.3 of it. Default 1.6. */
	drift?: number | undefined;
	/** Strength of the dust haze behind the stars, 0 to 1. Default 0.5. */
	nebula?: number | undefined;
}

const DEFAULT_PALETTE = [PAPER, inkMix(55), INK];

/** Dithered stars in three parallax layers that twinkle and drift slowly. */
export function DitherStarfield({
	density = 1,
	twinkle = 0.6,
	angle = 270,
	drift = 1.6,
	nebula = 0.5,
	palette = DEFAULT_PALETTE,
	// Sixteen steps per palette stop keep every stop exact, so stars stay solid.
	levels = 16 * Math.max(palette.length - 1, 1) + 1,
	...rest
}: DitherStarfieldProps) {
	return (
		<DitherShader
			{...shaderBase(
				{ ...rest, levels },
				{ staticTime: 4, matrix: "blue-noise" },
			)}
			field={FIELD}
			palette={palette}
			uniforms={{
				sfDensity: clamp(density, 0, 2),
				sfTwinkle: clamp(twinkle, 0, 1),
				sfAngle: angle,
				sfDrift: drift,
				sfNebula: clamp(nebula, 0, 1),
				// Tone of the second-to-last palette stop: the dimmer star colour.
				sfLo:
					palette.length >= 3
						? (palette.length - 2) / (palette.length - 1)
						: 0.5,
			}}
		/>
	);
}
