import { DitherShader } from "../../lib/gl";
import { resolveMatrix } from "./matrix";
import {
	type BackgroundProps,
	DEG,
	type DitherColor,
	GLSL_MASK,
	INK,
	MASK_INDEX,
	type MaskMode,
	PAPER,
} from "./shared";

const FIELD = /* glsl */ `
${GLSL_MASK}
float field(vec2 uv, float t) {
  vec2 p = vec2(uv.x * u_aspect, uv.y) * nfScale * 2.2;
  vec2 dir = vec2(cos(nfAngle), sin(nfAngle));
  p -= dir * t * 0.035 * nfFlow;
  int oct = int(nfDetail + 0.5);
  // Fold the noise back on itself so it flows instead of scrolling.
  vec2 q = vec2(fbmN(p + t * 0.01, oct), fbmN(p + vec2(5.2, 1.3) - t * 0.012, oct));
  vec2 w = p + nfWarp * (q - 0.5) * 1.6;
  float n = fbmN(w + vec2(1.7, 9.2), oct);
  float v;
  if (nfVariant < 0.5) {
    v = smoothstep(0.28, 0.78, n);
  } else if (nfVariant < 1.5) {
    v = pow(1.0 - abs(2.0 * n - 1.0), 3.0);
  } else if (nfVariant < 2.5) {
    float h = n * nfBands;
    float d = abs(fract(h + 0.5) - 0.5);
    float wd = clamp(fwidth(h) * 1.1, 0.03, 0.45);
    v = 1.0 - smoothstep(0.0, wd, d);
  } else {
    float s = (w.x * 0.5 + (n - 0.5) * 1.2 * nfWarp) * nfBands;
    v = pow(0.5 + 0.5 * sin(s), 2.2);
  }
  v = clamp((v - 0.5) * nfContrast + 0.5 + nfCoverage, 0.0, 1.0);
  if (nfInvert > 0.5) v = 1.0 - v;
  return v * nfIntensity * dMask(uv, nfMask, nfMaskSize);
}
`;

const VARIANT_INDEX = { smoke: 0, ridges: 1, contours: 2, marble: 3 } as const;

export type NoiseFieldVariant = keyof typeof VARIANT_INDEX;

export type DitherNoiseFieldProps = BackgroundProps<{
	/** The ink of the texture. */
	color?: DitherColor;
	/** A third colour for the brightest peaks. */
	highlight?: DitherColor;
	/** The paper behind. */
	background?: DitherColor;
	/** Soft folds, sharp creases, topographic lines or warped stripes. */
	variant?: NoiseFieldVariant;
	/** Size of the features. Higher is smaller and busier. */
	scale?: number;
	/** How far the noise folds back on itself, 0 to 4. */
	warp?: number;
	/** Octaves of noise, 1 to 6. */
	detail?: number;
	/** 1 is soft, 2 and up give hard-edged islands. */
	contrast?: number;
	/** How much of the box is inked, -0.5 to 0.5. */
	coverage?: number;
	/** Brightest tone the field reaches, 0 to 1. */
	intensity?: number;
	/** Lines for `contours`, stripes for `marble`. */
	bands?: number;
	/** Drift direction in degrees. 0 is right, 90 is up. */
	angle?: number;
	/** How fast it drifts along `angle`, 0 to 4. */
	flow?: number;
	/** Swap paper and ink. */
	invert?: boolean;
	/** Clear space for the text on top. */
	mask?: MaskMode;
	/** How much of the box the mask clears, 0 to 1. */
	maskSize?: number;
}>;

/** A slow, folding noise texture in four styles, with a mask that clears space for text. */
export function DitherNoiseField({
	color = INK,
	highlight,
	background = PAPER,
	variant = "smoke",
	scale = 1.3,
	warp = 1.7,
	detail = 4,
	contrast = 1.2,
	coverage = 0,
	intensity = 0.9,
	bands = 8,
	angle = 90,
	flow = 1,
	invert = false,
	mask = "none",
	maskSize = 0.5,
	palette,
	matrix,
	n,
	levels,
	stillTime,
	staticTime,
	...rest
}: DitherNoiseFieldProps) {
	const ramp =
		palette ??
		(highlight ? [background, color, highlight] : [background, color]);
	const tune = resolveMatrix(
		{ matrix, n, levels, stillTime, staticTime },
		{ matrix: "bayer8", staticTime: 0, levels: "full" },
	);
	return (
		<DitherShader
			{...rest}
			field={FIELD}
			palette={ramp}
			{...tune}
			uniforms={{
				nfVariant: VARIANT_INDEX[variant],
				nfScale: scale,
				nfWarp: warp,
				nfDetail: detail,
				nfContrast: contrast,
				nfCoverage: coverage,
				nfIntensity: intensity,
				nfBands: bands,
				nfAngle: angle * DEG,
				nfFlow: flow,
				nfInvert: invert ? 1 : 0,
				nfMask: MASK_INDEX[mask],
				nfMaskSize: maskSize,
			}}
		/>
	);
}
