import { DitherShader } from "../../lib/gl";
import { resolveMatrix } from "./matrix";
import { type BackgroundProps, DEG, INK, PAPER } from "./shared";

const FIELD = /* glsl */ `
float field(vec2 uv, float t) {
  vec2 p = vec2(uv.x * u_aspect, uv.y);
  vec2 o = vec2(bmOrigin.x * u_aspect, 1.0 - bmOrigin.y);
  vec2 d = p - o;
  float dist = length(d);
  float hw = max(bmSpread * 0.5, 0.01);
  // Angle from straight up, clockwise: the CSS convention (180 is down).
  float rel = atan(d.x, d.y) - (bmAngle + bmSweep * sin(t * 0.1));
  rel = atan(sin(rel), cos(rel));
  float cone = 1.0 - smoothstep(hw * 0.55, hw, abs(rel));
  // Rays are noise across the angle, so they widen with distance.
  float ray = smoothstep(0.3, 0.85, vnoise(vec2(rel / hw * bmRays * 0.5 + 11.0, t * 0.12)));
  ray = 0.25 + 0.75 * ray;
  float dust = 0.72 + 0.28 * vnoise(vec2(rel * 60.0, dist * 7.0 - t * 0.25));
  // Fade from where the light enters the box, not from a far-off origin.
  float enter = max(sdBox(o - vec2(0.5 * u_aspect, 0.5), vec2(0.5 * u_aspect, 0.5)), 0.0);
  float diag = length(vec2(u_aspect, 1.0));
  float fade = 1.0 - smoothstep(0.0, max(bmReach * diag, 0.05), max(dist - enter, 0.0));
  return cone * ray * dust * fade;
}
`;

export type DitherBeamsProps = BackgroundProps<{
	/** Where the light comes from, fractions of the box from the top left. May lie outside 0..1. */
	origin?: readonly [number, number];
	/** Direction the light points, degrees as in CSS: 180 is straight down. */
	angle?: number;
	/** Width of the cone in degrees. */
	spread?: number;
	/** Roughly how many separate rays. */
	rays?: number;
	/** How far the cone sways each way, in degrees. 0 holds it still. */
	sweep?: number;
	/** How far the light reaches from where it enters the box, as a share of the diagonal. */
	reach?: number;
}>;

/** God rays: shafts of light from a point or an edge that sweep slowly and fade. */
export function DitherBeams({
	origin = [0.18, -0.12],
	angle = 160,
	spread = 80,
	rays = 9,
	sweep = 6,
	reach = 1,
	palette = [PAPER, INK],
	matrix,
	n,
	levels,
	stillTime,
	staticTime,
	...rest
}: DitherBeamsProps) {
	const tune = resolveMatrix(
		{ matrix, n, levels, stillTime, staticTime },
		{ matrix: "bayer4", staticTime: 3, levels: "full" },
	);
	return (
		<DitherShader
			{...rest}
			field={FIELD}
			palette={palette}
			{...tune}
			uniforms={{
				bmOrigin: [origin[0], origin[1]],
				bmAngle: angle * DEG,
				bmSpread: spread * DEG,
				bmRays: rays,
				bmSweep: sweep * DEG,
				bmReach: reach,
			}}
		/>
	);
}
