import { DitherShader } from "../../lib/gl";
import { resolveMatrix } from "./matrix";
import { type BackgroundProps, INK, PAPER } from "./shared";

const FIELD = /* glsl */ `
float field(vec2 uv, float t) {
  vec2 p = vec2(uv.x * u_aspect, uv.y) * smScale * 2.4;
  p.y -= t * 0.22;
  p.x -= smWind * (t * 0.15 + uv.y * 1.2);
  // Domain-warped fbm: plumes that billow upward.
  vec2 q = vec2(fbm(p * 0.8 + vec2(0.0, t * 0.04)), fbm(p * 0.8 + vec2(5.2, 1.3 - t * 0.05)));
  vec2 w = p + 1.8 * (q - 0.5);
  float n = fbm(w);
  // A second sample towards the light (top left) gives each plume a lit side.
  float nl = fbm(w + vec2(-0.18, 0.18));
  float profile = pow(clamp(1.0 - uv.y / max(smHeight, 0.05), 0.0, 1.0), 1.25);
  float density = smoothstep(0.32, 0.82, n) * profile * smDensity;
  float lit = clamp(0.5 + (nl - n) * 3.2, 0.0, 1.0);
  // Stop short of solid ink so the dither always shows.
  return min(density * (0.4 + 0.5 * lit), 0.9);
}
`;

export type DitherSmokeProps = BackgroundProps<{
	/** Size of the plumes. Higher makes more, smaller ones. */
	scale?: number;
	/** How high the smoke climbs before it thins out, 0 to 1. */
	height?: number;
	/** Sideways drift. Negative blows left, positive right. */
	wind?: number;
	/** How thick the smoke is, 0 to 1.5. */
	density?: number;
}>;

/** Soft smoke that billows upward and thins out, lit from above. */
export function DitherSmoke({
	scale = 1,
	height = 0.8,
	wind = 0.2,
	density = 1,
	palette = [PAPER, INK],
	matrix,
	n,
	levels,
	stillTime,
	staticTime,
	...rest
}: DitherSmokeProps) {
	const tune = resolveMatrix(
		{ matrix, n, levels, stillTime, staticTime },
		{ matrix: "bayer4", staticTime: 8, levels: "full" },
	);
	return (
		<DitherShader
			{...rest}
			field={FIELD}
			palette={palette}
			{...tune}
			uniforms={{
				smScale: scale,
				smHeight: height,
				smWind: wind,
				smDensity: density,
			}}
		/>
	);
}
