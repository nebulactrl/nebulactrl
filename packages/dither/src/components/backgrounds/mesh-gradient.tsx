import { DitherShader } from "../../lib/gl";
import { resolveMatrix } from "./matrix";
import { type BackgroundProps, PAPER, tint } from "./shared";

const FIELD = /* glsl */ `
float field(vec2 uv, float t) {
  vec2 p = vec2(uv.x * u_aspect, uv.y);
  float sum = 0.0;
  float acc = 0.0;
  for (int i = 0; i < 5; i++) {
    if (float(i) >= mgBlobs) break;
    float fi = float(i);
    vec2 h = hash22(vec2(fi * 7.13 + 1.7, 3.1));
    vec2 h2 = hash22(vec2(fi * 3.77 + 9.2, 5.3));
    // Each blob drifts on its own slow Lissajous orbit.
    vec2 c = vec2(
      (0.5 + 0.38 * sin(t * (0.07 + 0.09 * h.x) + 6.2832 * h2.x)) * u_aspect,
      0.5 + 0.36 * sin(t * (0.06 + 0.08 * h.y) + 6.2832 * h2.y)
    );
    float r = mgSize * (0.8 + 0.4 * h2.y);
    float d = length(p - c) / r;
    float w = exp(-d * d * 1.6);
    sum += w;
    // Each blob sits at its own step of the palette; overlaps blend through the ramp.
    acc += w * ((fi + 1.0) / mgBlobs);
  }
  return acc / (sum + 0.12);
}
`;

export type DitherMeshGradientProps = BackgroundProps<{
	/** How many colour blobs drift across the box. */
	blobs?: 3 | 4 | 5;
	/** Size of each blob as a share of the box height. */
	size?: number;
}>;

/** Three to five colour blobs that drift and melt into each other. */
export function DitherMeshGradient({
	blobs = 4,
	size = 0.6,
	palette,
	matrix,
	n,
	levels,
	stillTime,
	staticTime,
	...rest
}: DitherMeshGradientProps) {
	// Paper, then one ink tint per blob, getting stronger.
	const ramp = palette ?? [
		PAPER,
		...Array.from({ length: blobs }, (_, i) =>
			tint(30 + (70 * (i + 1)) / blobs),
		),
	];
	const tune = resolveMatrix(
		{ matrix, n, levels, stillTime, staticTime },
		{ matrix: "blue-noise", staticTime: 8, levels: "full" },
	);
	return (
		<DitherShader
			{...rest}
			field={FIELD}
			palette={ramp}
			{...tune}
			uniforms={{ mgBlobs: blobs, mgSize: size }}
		/>
	);
}
