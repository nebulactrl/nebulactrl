import { DitherShader } from "../../lib/gl";
import { resolveMatrix } from "./matrix";
import { type BackgroundProps, INK, PAPER } from "./shared";

const FIELD = /* glsl */ `
float field(vec2 uv, float t) {
  vec2 p = vec2(uv.x * u_aspect, uv.y) * 3.2 * plScale;
  // A slow noise warp so it does not look mechanical.
  p += plWarp * 1.5 * vec2(snoise(p * 0.6 + vec2(t * 0.08, 0.0)), snoise(p * 0.6 + vec2(0.0, 5.0 - t * 0.07)));
  vec2 c = vec2(0.5 * u_aspect, 0.5) * 3.2 * plScale + vec2(sin(t * 0.23), cos(t * 0.19)) * 2.0;
  float v = sin(p.x * 1.7 + t * 0.8)
          + sin(p.y * 2.1 - t * 0.6)
          + sin((p.x + p.y) * 1.3 + t * 0.5)
          + sin(length(p - c) * 2.2 - t * 0.7);
  float v01 = clamp(v * 0.2 + 0.5, 0.0, 1.0);
  return (0.5 - 0.5 * cos(6.28318530718 * v01 * plBands)) * plIntensity;
}
`;

export type DitherPlasmaProps = BackgroundProps<{
	/** Size of the pattern. Higher packs more waves in. */
	scale?: number;
	/** How far slow noise bends the waves, 0 to 1. */
	warp?: number;
	/** How many times the colours cycle across one wave. */
	bands?: number;
	/** The brightest tone, 0 to 1. Lower it to put text on top. */
	intensity?: number;
}>;

/** Classic plasma: overlapping sine waves, dithered through the palette. */
export function DitherPlasma({
	scale = 1,
	warp = 0.3,
	bands = 1.5,
	intensity = 1,
	palette = [PAPER, "--chart-5", INK],
	matrix,
	n,
	levels,
	stillTime,
	staticTime,
	...rest
}: DitherPlasmaProps) {
	const tune = resolveMatrix(
		{ matrix, n, levels, stillTime, staticTime },
		{ matrix: "bayer4", staticTime: 5, levels: "full" },
	);
	return (
		<DitherShader
			{...rest}
			field={FIELD}
			palette={palette}
			{...tune}
			uniforms={{
				plScale: scale,
				plWarp: warp,
				plBands: bands,
				plIntensity: intensity,
			}}
		/>
	);
}
