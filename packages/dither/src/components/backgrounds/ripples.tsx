import { DitherShader } from "../../lib/gl";
import { resolveMatrix } from "./matrix";
import { type BackgroundProps, INK, PAPER, tint } from "./shared";

const FIELD = /* glsl */ `
float rpAdd(vec2 p, vec2 s, float t) {
  vec2 c = vec2(s.x * u_aspect, 1.0 - s.y);
  // A slow swell bends the rings so they do not look drawn with a compass.
  float d = length(p - c) + (fbm(p * 2.2 + vec2(t * 0.03, 0.0)) - 0.5) * 0.04;
  float wave = 0.5 + 0.5 * cos(d * rpRings * 6.28318530718 - t * 1.6);
  float fade = exp(-d * rpDecay * 2.2);
  // One ring tops out at the middle tone; two crossing add up to full ink.
  return pow(wave, 1.5) * fade * 0.5;
}
float field(vec2 uv, float t) {
  vec2 p = vec2(uv.x * u_aspect, uv.y);
  float a = rpAdd(p, rpS0, t);
  if (rpCount > 1.5) a += rpAdd(p, rpS1, t);
  if (rpCount > 2.5) a += rpAdd(p, rpS2, t);
  if (rpCount > 3.5) a += rpAdd(p, rpS3, t);
  return clamp(a, 0.0, 1.0) * rpIntensity;
}
`;

export type RippleSource = readonly [x: number, y: number];

export type DitherRipplesProps = BackgroundProps<{
	/** Where the rings start, fractions from the top left. One to four points. */
	sources?: readonly RippleSource[];
	/** How many rings fit in the height of the box. */
	rings?: number;
	/** How quickly the rings fade away from their source. 0 keeps them strong. */
	decay?: number;
	/** The brightest tone, 0 to 1. */
	intensity?: number;
}>;

const CENTER: RippleSource = [0.5, 0.5];

/** Concentric rings from one to four points, crossing like drops on still water. */
export function DitherRipples({
	sources = [
		[0.28, 0.38],
		[0.74, 0.66],
	],
	rings = 12,
	decay = 1,
	intensity = 1,
	palette = [PAPER, tint(45), INK],
	matrix,
	n,
	levels,
	stillTime,
	staticTime,
	...rest
}: DitherRipplesProps) {
	const list = sources.slice(0, 4);
	const at = (i: number): [number, number] => {
		const s = list[i] ?? CENTER;
		return [s[0], s[1]];
	};
	const tune = resolveMatrix(
		{ matrix, n, levels, stillTime, staticTime },
		{ matrix: "bayer4", staticTime: 4, levels: "full" },
	);
	return (
		<DitherShader
			{...rest}
			field={FIELD}
			palette={palette}
			{...tune}
			uniforms={{
				rpS0: at(0),
				rpS1: at(1),
				rpS2: at(2),
				rpS3: at(3),
				rpCount: Math.max(1, list.length),
				rpRings: rings,
				rpDecay: decay,
				rpIntensity: intensity,
			}}
		/>
	);
}
