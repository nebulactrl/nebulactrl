import { DitherShader } from "../../lib/gl";
import { resolveMatrix } from "./matrix";
import { type BackgroundProps, INK, PAPER, tint } from "./shared";

const FIELD = /* glsl */ `
float field(vec2 uv, float t) {
  float x = uv.x * u_aspect;
  float layers = wvLayers;
  float tone = 0.0;
  if (wvVariant < 0.5) {
    // Glow above the horizon.
    float above = uv.y - wvHorizon;
    if (above >= 0.0) tone = 0.55 * exp(-above * 14.0) * (0.85 + 0.15 * sin(x * 3.0 + t * 0.2));
    float crestW = 1.4 / u_res.y;
    // Far rows first; nearer rows paint over them.
    for (int i = 0; i < 8; i++) {
      if (float(i) >= layers) break;
      float z = float(i) / max(layers - 1.0, 1.0);
      float base = wvHorizon - wvHorizon * (0.04 + 0.92 * pow(z, 1.5));
      float freq = mix(18.0, 4.5, z);
      float amp = wvAmp * mix(0.006, 0.04, z);
      float ph = float(i) * 1.7;
      float wave = sin(x * freq + t * (0.5 + 0.3 * z) + ph)
                 + 0.45 * sin(x * freq * 2.3 - t * 0.4 * (1.0 + z) + ph * 2.0);
      float line = base + amp * wave;
      if (uv.y < line) {
        tone = mix(0.8, 0.18, z);
        // One bright cell on every crest.
        if (uv.y > line - crestW * (0.6 + z)) tone = 0.97;
      }
    }
  } else {
    // Thin lines like a sound wave: swell in the middle, taper at both ends.
    float env = pow(max(sin(3.14159265 * uv.x), 0.0), 1.4);
    for (int i = 0; i < 8; i++) {
      if (float(i) >= layers) break;
      float k = float(i);
      float rel = k - (layers - 1.0) * 0.5;
      float wave = sin(uv.x * (9.0 + 3.5 * k) + t * (0.8 + 0.25 * k) + k * 1.9)
                 * (0.6 + 0.4 * sin(uv.x * 4.0 - t * 0.5 + k));
      float y = 0.5 + rel * 0.02 * (1.0 - env * 0.5) + env * wvAmp * 0.2 * wave * (1.0 - 0.3 * abs(rel) / max(layers, 1.0));
      float d = abs(uv.y - y) * u_res.y;
      float a = (1.0 - smoothstep(0.4, 1.5, d)) * (0.5 + 0.5 * env);
      tone = max(tone, a);
    }
  }
  return tone;
}
`;

export type DitherWavesProps = BackgroundProps<{
	/** Swells under a horizon, or thin lines like a sound wave. */
	variant?: "ocean" | "lines";
	/** Ocean only. Height of the horizon, 0 (bottom) to 1 (top). */
	horizon?: number;
	/** How many waves, 2 to 8. */
	layers?: number;
	/** Height of the waves. 1 is calm, 2 is rough. */
	amplitude?: number;
}>;

/** Layered swells under a glowing horizon, or sound-wave lines. */
export function DitherWaves({
	variant = "ocean",
	horizon = 0.62,
	layers = 6,
	amplitude = 1,
	palette,
	matrix,
	n,
	levels,
	stillTime,
	staticTime,
	...rest
}: DitherWavesProps) {
	const ramp =
		palette ??
		(variant === "lines" ? [PAPER, INK] : [PAPER, tint(35), tint(65), INK]);
	const tune = resolveMatrix(
		{ matrix, n, levels, stillTime, staticTime },
		{ matrix: "bayer4", staticTime: 6, levels: "full" },
	);
	return (
		<DitherShader
			{...rest}
			field={FIELD}
			palette={ramp}
			{...tune}
			uniforms={{
				wvVariant: variant === "lines" ? 1 : 0,
				wvHorizon: horizon,
				wvLayers: Math.min(8, Math.max(2, Math.round(layers))),
				wvAmp: amplitude,
			}}
		/>
	);
}
