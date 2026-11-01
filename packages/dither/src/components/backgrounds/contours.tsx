import { DitherShader } from "../../lib/gl";
import { resolveMatrix } from "./matrix";
import { type BackgroundProps, INK, PAPER, tint } from "./shared";

const FIELD = /* glsl */ `
float field(vec2 uv, float t) {
  // Hills keep their size in CSS pixels: a narrow box shows a smaller piece of the same map.
  vec2 p = uv * u_res * u_cell / 420.0 * ctScale * 1.3 + vec2(t * 0.012, -t * 0.008);
  p += 0.4 * vec2(snoise(p * 0.5 + t * 0.02), snoise(p * 0.5 + 7.0 - t * 0.017));
  float h = smoothstep(0.2, 0.8, fbmN(p, 3));
  float lv = h * ctLines;
  float f = abs(fract(lv + 0.5) - 0.5);
  float w = clamp(fwidth(lv) * 0.9, 0.04, 0.4);
  float k = floor(lv + 0.5);
  bool isIndex = ctIndex > 0.5 && mod(k, ctIndex) < 0.5;
  float line = 1.0 - smoothstep(0.0, isIndex ? w * 2.0 : w, f);
  float tone = line * (isIndex ? 1.0 : 0.62);
  // Hillshade: slopes facing away from a light in the top left pick up a stipple.
  vec2 g = vec2(dFdx(h), dFdy(h));
  float away = clamp(dot(g, normalize(vec2(-1.0, 1.0))) * 70.0, 0.0, 1.0);
  return max(tone, ctRelief * 0.28 * away);
}
`;

export type DitherContoursProps = BackgroundProps<{
	/** Contour lines from lowest ground to highest, 4 to 32. */
	lines?: number;
	/** Size of the hills. Higher packs more in. */
	scale?: number;
	/** Every how many lines one is drawn heavier. 0 draws them all alike. */
	index?: number;
	/** How strongly slopes facing away from the light are shaded, 0 to 1. */
	relief?: number;
}>;

/** A topographic map: contour lines over slowly drifting hills. */
export function DitherContours({
	lines = 14,
	scale = 1,
	index = 5,
	relief = 0.5,
	palette = [PAPER, tint(45), INK],
	matrix,
	n,
	levels,
	stillTime,
	staticTime,
	...rest
}: DitherContoursProps) {
	const tune = resolveMatrix(
		{ matrix, n, levels, stillTime, staticTime },
		{ matrix: "bayer4", staticTime: 4, levels: 12 },
	);
	return (
		<DitherShader
			{...rest}
			field={FIELD}
			palette={palette}
			{...tune}
			uniforms={{
				ctLines: lines,
				ctScale: scale,
				ctIndex: index,
				ctRelief: relief,
			}}
		/>
	);
}
