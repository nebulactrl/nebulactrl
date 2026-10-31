import { DitherShader } from "../../lib/gl";
import { resolveMatrix } from "./matrix";
import { type BackgroundProps, GLSL_FAR, INK, PAPER } from "./shared";

const FIELD = /* glsl */ `
${GLSL_FAR}
float field(vec2 uv, float t) {
  vec2 p = vec2(uv.x * u_aspect, uv.y);
  vec2 c = vec2(glPos.x * u_aspect, 1.0 - glPos.y);
  float breath = 1.0 + 0.04 * sin(t * 0.5);
  float d = length(p - c) / max(dFar(c) * glRadius * breath, 1e-4);
  // A little noise at the rim so it never reads as a flat circle.
  d += (fbm(p * 3.0 + vec2(t * 0.04, -t * 0.03)) - 0.5) * 0.18 * smoothstep(0.0, 0.8, d);
  return glIntensity * pow(clamp(1.0 - d, 0.0, 1.0), 1.7);
}
`;

const POSITIONS = {
	top: [0.5, -0.08],
	bottom: [0.5, 1.08],
	left: [-0.08, 0.5],
	right: [1.08, 0.5],
	center: [0.5, 0.5],
	"top-left": [-0.05, -0.05],
	"top-right": [1.05, -0.05],
	"bottom-left": [-0.05, 1.05],
	"bottom-right": [1.05, 1.05],
} as const;

export type GlowPosition = keyof typeof POSITIONS;
export const GLOW_POSITIONS = Object.keys(POSITIONS) as GlowPosition[];

export type DitherGlowProps = BackgroundProps<{
	/** Where the light comes from: an edge, corner, or `[x, y]` fractions from the top left. */
	position?: GlowPosition | readonly [number, number];
	/** How far the light reaches. 1 reaches the farthest corner. */
	radius?: number;
	/** Brightness at the centre, 0 to 1. */
	intensity?: number;
}>;

/** A soft dithered spotlight from an edge, a corner or any point. */
export function DitherGlow({
	position = "top",
	radius = 0.7,
	intensity = 0.75,
	palette = [PAPER, INK],
	matrix,
	n,
	levels,
	stillTime,
	staticTime,
	...rest
}: DitherGlowProps) {
	const at = typeof position === "string" ? POSITIONS[position] : position;
	const tune = resolveMatrix(
		{ matrix, n, levels, stillTime, staticTime },
		{ matrix: "bayer8", staticTime: 0, levels: "full" },
	);
	return (
		<DitherShader
			{...rest}
			field={FIELD}
			palette={palette}
			{...tune}
			uniforms={{
				glPos: [at[0], at[1]],
				glRadius: radius,
				glIntensity: intensity,
			}}
		/>
	);
}
