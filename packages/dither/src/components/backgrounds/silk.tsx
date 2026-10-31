import { DitherShader } from "../../lib/gl";
import { resolveMatrix } from "./matrix";
import { type BackgroundProps, DEG, INK, PAPER, tint } from "./shared";

const FIELD = /* glsl */ `
float field(vec2 uv, float t) {
  vec2 p = vec2(uv.x * u_aspect, uv.y);
  // Direction the folds run along (CSS angle) and its normal.
  vec2 run = vec2(sin(skAngle), cos(skAngle));
  vec2 nrm = vec2(run.y, -run.x);
  vec2 q = p - vec2(0.5 * u_aspect, 0.5);
  float along = dot(q, run);
  float across = dot(q, nrm);
  // Height field: folds across, bent by slow waves that slide and change width.
  float s = across * skFolds * 1.4;
  s += 0.9 * snoise(vec2(along * 1.1 + t * 0.05, across * 0.6 - t * 0.03));
  s += 0.35 * sin(along * 3.1 + t * 0.18 + across * 1.7);
  s += 0.12 * t;
  float ph = s * 3.14159265;
  // Lit from the top left: faces turned towards it are bright, the far sides fall into shadow.
  vec2 light = normalize(vec2(-1.0, 1.0));
  float facing = clamp(dot(nrm, light) * 1.6, -1.0, 1.0);
  float lit = clamp(0.5 + 0.5 * cos(ph) * facing, 0.0, 1.0);
  float crest = pow(lit, mix(5.0, 45.0, skSheen));
  float tone = 0.08 + 0.6 * lit + 0.55 * skSheen * crest;
  return clamp(tone, 0.0, 1.0) * skIntensity;
}
`;

export type DitherSilkProps = BackgroundProps<{
	/** How many folds cross the box. */
	folds?: number;
	/** Direction the folds run, degrees as in CSS: 90 runs them left to right. */
	angle?: number;
	/** How narrow and bright the highlight on each fold is, 0 to 1. */
	sheen?: number;
	/** The brightest tone, 0 to 1. Lower it to put text on top. */
	intensity?: number;
}>;

/** Folds of silk catching the light, with a bright highlight along each crest. */
export function DitherSilk({
	folds = 6,
	angle = 110,
	sheen = 0.6,
	intensity = 1,
	palette = [PAPER, tint(25), tint(55), INK],
	matrix,
	n,
	levels,
	stillTime,
	staticTime,
	...rest
}: DitherSilkProps) {
	const tune = resolveMatrix(
		{ matrix, n, levels, stillTime, staticTime },
		{ matrix: "bayer8", staticTime: 6, levels: "full" },
	);
	return (
		<DitherShader
			{...rest}
			field={FIELD}
			palette={palette}
			{...tune}
			uniforms={{
				skFolds: folds,
				skAngle: angle * DEG,
				skSheen: sheen,
				skIntensity: intensity,
			}}
		/>
	);
}
