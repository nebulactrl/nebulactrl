import { DitherShader } from "../../lib/gl";
import {
	type BackgroundBaseProps,
	clamp,
	INK,
	PAPER,
	shaderBase,
} from "./common";

// Stacked ridge lines, a pulsar plot. Line 0 is the front (lowest) line. For
// each cell we find the frontmost line whose curve passes above it: that line
// owns the cell, so every line hides the ones behind it. The cell is either on
// the curve (a one-cell line) or in the faint body shaded under a raised peak.
const FIELD = /* glsl */ `
float rlEnvelope(float x) {
  float half_ = max(rlSpread, 0.05) * 0.5;
  float k = clamp(abs(x - 0.5) / half_, 0.0, 1.0);
  return 1.0 - k * k * (3.0 - 2.0 * k);
}
float rlPeak(float x, float j, float t) {
  float n1 = vnoise(vec2(x * 7.0 + j * 3.7, t * 0.32 + j * 1.3));
  float n2 = vnoise(vec2(x * 19.0 - j * 5.1, t * 0.55 + j * 0.7));
  float n = 0.62 * n1 + 0.38 * n2;
  return clamp(n * n * 2.1, 0.0, 1.0);
}
float field(vec2 uv, float t) {
  float cellH = 1.0 / u_res.y;
  float span = 0.78;
  // Lines stay at least four cells apart, so a short box draws fewer.
  float rows = max(min(rlLines, floor(u_res.y * span / 4.0) + 1.0), 2.0);
  float spacing = span / (rows - 1.0);
  float lift = rlAmp * 0.22;
  float env = rlEnvelope(uv.x);
  int owner = -1;
  float ownerY = 0.0;
  float ownerBase = 0.0;
  for (int i = 0; i < 64; i++) {
    float fi = float(i);
    if (fi >= rows) break;
    float base = 0.06 + spacing * fi;
    float y = base + lift * env * rlPeak(uv.x, fi, t);
    if (uv.y < y) {
      owner = i;
      ownerY = y;
      ownerBase = base;
      break;
    }
  }
  if (owner < 0) return 0.0;
  float d = ownerY - uv.y;
  float line = 1.0 - smoothstep(0.5 * cellH, 1.5 * cellH, d);
  float height = smoothstep(0.0, 1.0, (ownerY - ownerBase) / max(lift * 0.6, 1e-3));
  float body = rlShade * height * (1.0 - smoothstep(0.0, 0.14, d));
  return max(line, body);
}
`;

export interface DitherRidgelinesProps extends BackgroundBaseProps {
	/** How many lines, 6 to 64. A short box draws fewer. Default 32. */
	lines?: number | undefined;
	/** Height of the peaks. 2 is twice as tall. Default 1. */
	amplitude?: number | undefined;
	/** Width of the peaked band as a share of the box, 0.1 to 1. Default 0.5. */
	spread?: number | undefined;
	/** Faint tone under the raised peaks, 0 to 0.5. 0 keeps only the lines. Default 0.25. */
	shade?: number | undefined;
}

const DEFAULT_PALETTE = [PAPER, INK];

/** Stacked ridge lines like a pulsar plot, each hiding the ones behind it. */
export function DitherRidgelines({
	lines = 32,
	amplitude = 1,
	spread = 0.5,
	shade = 0.25,
	palette = DEFAULT_PALETTE,
	...rest
}: DitherRidgelinesProps) {
	return (
		<DitherShader
			{...shaderBase(rest, { staticTime: 6 })}
			field={FIELD}
			palette={palette}
			uniforms={{
				rlLines: clamp(Math.round(lines), 6, 64),
				rlAmp: Math.max(0, amplitude),
				rlSpread: clamp(spread, 0.1, 1),
				rlShade: clamp(shade, 0, 0.5),
			}}
		/>
	);
}
