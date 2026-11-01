import { DitherShader } from "../../lib/gl";
import {
	type BackgroundBaseProps,
	clamp,
	INK,
	PAPER,
	shaderBase,
} from "./common";

// The planet is a circle whose top touches `horizon`. e is the height above
// its surface. Above the rim, domain-warped noise forms aurora curtains that
// slide slowly along the horizon; fixed stars thin out towards the rim so the
// glow keeps a clean edge.
const FIELD = /* glsl */ `
float field(vec2 uv, float t) {
  float a = u_aspect;
  float cellH = 1.0 / u_res.y;
  vec2 p = vec2(uv.x * a, uv.y);
  // curve 0 is nearly flat (R ~ 12), curve 1 a small globe (R ~ 0.45).
  float R = exp(mix(2.48, -0.8, hzCurve));
  vec2 c = vec2(0.5 * a, hzHorizon - R);
  float e = length(p - c) - R;
  if (e < 0.0) return 0.0;

  float s = p.x - c.x;
  float warp = snoise(vec2(s * 1.3 + t * 0.05, e * 2.0 - t * 0.02)) * 0.35;
  float curtain = smoothstep(0.25, 0.85, fbm(vec2((s + warp) * 2.6 + t * 0.06, e * 1.1)));
  float streak = 0.7 + 0.6 * pow(vnoise(vec2((s + warp) * 22.0, t * 0.2 + e * 3.0)), 2.0);
  float curtains = mix(1.0, curtain * 1.5 * mix(1.0, streak, 0.6), hzAurora);
  float glow = hzGlow * exp(-e * 5.5) * curtains * 0.85;

  float rim = exp(-e / (1.3 * cellH)) * 0.8 * hzGlow;

  // Stars sit on a fixed grid of 5-cell tiles, one cell each, never moving.
  vec2 g = uv * u_res / 5.0;
  vec2 id = floor(g);
  vec2 sp = floor(hash22(id) * 5.0);
  vec2 lc = floor((g - id) * 5.0);
  float present = step(hash21(id + 7.3), 0.2 * hzStars);
  float star = present * step(abs(lc.x - sp.x) + abs(lc.y - sp.y), 0.5) * smoothstep(0.02, 0.3, e);

  return clamp(max(max(glow, rim), star), 0.0, 1.0);
}
`;

export interface DitherHorizonProps extends BackgroundBaseProps {
	/** Height of the horizon's highest point from the bottom, 0 to 1. Default 0.28. */
	horizon?: number | undefined;
	/** How round the planet is, 0 (nearly flat) to 1 (a small globe). Default 0.6. */
	curve?: number | undefined;
	/** Strength of the glow above the horizon, 0 to 1. Default 1. */
	glow?: number | undefined;
	/** How much the glow breaks into curtains, 0 (a smooth band) to 1. Default 1. */
	aurora?: number | undefined;
	/** How many stars, 0 to 3. They never move. Default 1. */
	stars?: number | undefined;
	/** Colour of the glow, rim and stars. Default `--dither-ink`. */
	glowColor?: string | undefined;
	/** Colour of the sky and the planet. Default `--dither-paper`. */
	skyColor?: string | undefined;
}

/** A planet's horizon seen from space: a lit rim, a drifting aurora glow and fixed stars. */
export function DitherHorizon({
	horizon = 0.28,
	curve = 0.6,
	glow = 1,
	aurora = 1,
	stars = 1,
	glowColor = INK,
	skyColor = PAPER,
	palette,
	...rest
}: DitherHorizonProps) {
	return (
		<DitherShader
			{...shaderBase(rest, { staticTime: 5 })}
			field={FIELD}
			palette={palette ?? [skyColor, glowColor]}
			uniforms={{
				hzHorizon: clamp(horizon, 0, 1),
				hzCurve: clamp(curve, 0, 1),
				hzGlow: clamp(glow, 0, 1),
				hzAurora: clamp(aurora, 0, 1),
				hzStars: clamp(stars, 0, 3),
			}}
		/>
	);
}
