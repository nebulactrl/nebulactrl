import { DitherShader } from "../../lib/gl";
import {
	type BackgroundBaseProps,
	clamp,
	INK,
	PAPER,
	shaderBase,
} from "./common";

// Each blob adds r^2 / d^2 to a scalar field; where two come close the sum
// bridges them. "solid" maps the sum to tone (densest at the core, soft halo);
// "rings" draws contour lines of sqrt(sum), which merge and split with the blobs.
const FIELD = /* glsl */ `
float field(vec2 uv, float t) {
  float a = u_aspect;
  vec2 p = vec2(uv.x * a, uv.y);
  // Sized by the shorter side, so a tall narrow box gets blobs sized by its width.
  float sc = min(a, 1.0);
  float f = 0.0;
  for (int i = 0; i < 8; i++) {
    float fi = float(i);
    if (fi >= mbCount) break;
    float h1 = hash11(fi * 7.31 + 1.0);
    float h2 = hash11(fi * 3.17 + 5.0);
    float h3 = hash11(fi * 9.77 + 2.0);
    vec2 c = vec2(
      0.5 + 0.36 * sin(t * (0.10 + 0.07 * h1) + fi * 2.399),
      0.5 + 0.34 * sin(t * (0.13 + 0.06 * h2) + fi * 1.713 + 1.0)
    );
    c.x *= a;
    float r = (0.13 + 0.07 * h3 + 0.015 * sin(t * 0.3 + fi)) * mbSize * sc;
    vec2 d = p - c;
    f += r * r / (dot(d, d) + 1e-4);
  }
  if (mbRings > 0.5) {
    float v = sqrt(f) * 3.2;
    float fr = fract(v);
    float dline = min(fr, 1.0 - fr);
    float aa = max(fwidth(v), 1e-3);
    float line = 1.0 - smoothstep(0.0, aa * 1.5, dline);
    return line * smoothstep(0.12, 0.4, f);
  }
  return pow(smoothstep(0.1, 2.6, f), 0.85);
}
`;

export interface DitherMetaballsProps extends BackgroundBaseProps {
	/** Shaded blobs with a soft halo, or contour lines around them. Default "solid". */
	variant?: "solid" | "rings" | undefined;
	/** How many blobs, 2 to 8. Default 5. */
	count?: number | undefined;
	/** Size of the blobs. In a tall box they are sized by the width. Default 1. */
	size?: number | undefined;
}

const DEFAULT_PALETTE = [PAPER, INK];

/** Soft blobs that drift, merge and split like a lava lamp. */
export function DitherMetaballs({
	variant = "solid",
	count = 5,
	size = 1,
	palette = DEFAULT_PALETTE,
	...rest
}: DitherMetaballsProps) {
	return (
		<DitherShader
			{...shaderBase(rest, { staticTime: 8 })}
			field={FIELD}
			palette={palette}
			uniforms={{
				mbCount: clamp(Math.round(count), 2, 8),
				mbSize: Math.max(0.1, size),
				mbRings: variant === "rings" ? 1 : 0,
			}}
		/>
	);
}
