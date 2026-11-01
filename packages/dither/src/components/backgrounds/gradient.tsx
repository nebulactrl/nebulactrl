import { DitherShader } from "../../lib/gl";
import { resolveMatrix } from "./matrix";
import { type BackgroundProps, DEG, GLSL_FAR, INK, PAPER } from "./shared";

const FIELD = /* glsl */ `
${GLSL_FAR}
float field(vec2 uv, float t) {
  vec2 p = vec2(uv.x * u_aspect, uv.y);
  float g;
  if (grMode < 0.5) {
    // Linear, as CSS: 180deg runs top to bottom.
    vec2 dir = vec2(sin(grAngle), cos(grAngle));
    vec2 q = p - vec2(0.5 * u_aspect, 0.5);
    float len = abs(u_aspect * dir.x) + abs(dir.y);
    g = dot(q, dir) / len + 0.5;
  } else if (grMode < 1.5) {
    vec2 c = vec2(grCenter.x * u_aspect, 1.0 - grCenter.y);
    g = length(p - c) / max(dFar(c) * grRadius, 1e-4);
  } else {
    vec2 c = vec2(grCenter.x * u_aspect, 1.0 - grCenter.y);
    vec2 d = p - c;
    g = fract(atan(d.x, d.y) / 6.28318530718 - grAngle / 6.28318530718);
  }
  g += (fbm(p * 1.3 + vec2(t * 0.05, t * 0.03)) - 0.5) * grNoise * 1.5;
  return mix(grFrom, grTo, clamp(g, 0.0, 1.0));
}
`;

const MODE_INDEX = { linear: 0, radial: 1, conic: 2 } as const;

export type GradientMode = keyof typeof MODE_INDEX;

export type DitherGradientProps = BackgroundProps<{
	/** Shape of the gradient. */
	mode?: GradientMode;
	/** Linear: direction in degrees as in CSS. Conic: where the sweep starts. Default 180 (linear), 0 (conic). */
	angle?: number;
	/** Radial and conic centre, fractions of the box from the top left. */
	center?: readonly [number, number];
	/** Radial only: 1 reaches the farthest corner. */
	radius?: number;
	/** Tone at the start, 0 (paper) to 1 (ink). */
	from?: number;
	/** Tone at the end. */
	to?: number;
	/** How far a slow noise drift bends the bands. 0 gives straight bands. */
	noise?: number;
}>;

/** A linear, radial or conic gradient drawn as a dither. */
export function DitherGradient({
	mode = "linear",
	angle,
	center = [0.5, 0.5],
	radius = 1,
	from = 0,
	to = 1,
	noise = 0.12,
	palette = [PAPER, INK],
	matrix,
	n,
	levels,
	stillTime,
	staticTime,
	...rest
}: DitherGradientProps) {
	const degrees = angle ?? (mode === "conic" ? 0 : 180);
	const tune = resolveMatrix(
		{ matrix, n, levels, stillTime, staticTime },
		{ matrix: "bayer4", staticTime: 0, levels: "full" },
	);
	return (
		<DitherShader
			{...rest}
			field={FIELD}
			palette={palette}
			{...tune}
			uniforms={{
				grMode: MODE_INDEX[mode],
				grAngle: degrees * DEG,
				grCenter: [center[0], center[1]],
				grRadius: radius,
				grFrom: from,
				grTo: to,
				grNoise: noise,
			}}
		/>
	);
}
