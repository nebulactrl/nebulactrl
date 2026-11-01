/**
 * Helpers shared by the part-1 backgrounds (gradient ... ripples): the prop
 * shape every wrapper accepts, ink tints for default palettes, and GLSL for
 * masks and "farthest corner" distances.
 */
import type { DitherShaderProps } from "../../lib/gl";

/** Props every background forwards to `DitherShader`. */
export type BackgroundProps<Own> = Omit<
	DitherShaderProps,
	"field" | "colorField" | "uniforms" | "textures"
> &
	Own;

/** A palette entry: a `--token` name or any CSS colour. */
export type DitherColor = string;

export const PAPER = "--dither-paper";
export const INK = "--dither-ink";

/** `percent` of the ink over the paper, for default ramps that follow the theme. */
export function tint(percent: number): string {
	return `color-mix(in oklch, var(${INK}) ${percent}%, var(${PAPER}))`;
}

export const DEG = Math.PI / 180;

export type MaskMode = "none" | "center" | "top" | "bottom" | "edges";

export const MASK_INDEX: Readonly<Record<MaskMode, number>> = {
	none: 0,
	center: 1,
	top: 2,
	bottom: 3,
	edges: 4,
};

/**
 * `dMask(uv, mode, size)`: 1 keeps the field, 0 clears it to paper. `center`
 * clears the middle, `top`/`bottom` clear that end, `edges` keeps only the
 * middle. `size` 0 clears nothing, 1 clears the most.
 */
export const GLSL_MASK = /* glsl */ `
float dMask(vec2 uv, float mode, float size) {
  if (mode < 0.5) return 1.0;
  if (mode < 1.5) {
    float d = length((uv - 0.5) * vec2(0.7, 1.3));
    return smoothstep(0.0, 0.22, d - size * 0.55 + 0.22);
  }
  if (mode < 2.5) return smoothstep(0.0, 0.25, (1.0 - uv.y) - size * 0.8 + 0.25);
  if (mode < 3.5) return smoothstep(0.0, 0.25, uv.y - size * 0.8 + 0.25);
  float a = abs(uv.x - 0.5) * 2.0;
  return 1.0 - smoothstep(1.0 - size * 0.9, 1.0 - size * 0.9 + 0.3, a);
}
`;

/**
 * `bg2Clear(px)`: 1 on open ground, 0 inside any clear box, feathered out of
 * each box with a rounded-box distance. Multiply the part of a field that
 * must stay off text by it. `px` is the cell's position in CSS px from the
 * host's top-left (`vec2(uv.x * u_res.x, (1.0 - uv.y) * u_res.y) * u_cell`),
 * the unit of `clearBoxUniforms(boxes, { cellCss: 1 })`; with a cell layout in
 * `clearBoxUniforms` pass the cell position instead. The boxes arrive as the
 * user uniforms `clearRect0..3`, `clearShape01`, `clearShape23` and `clearN`,
 * which the field must get from `clearBoxUniforms` (the engine declares them).
 * With no boxes (`clearN` 0) it returns exactly 1.
 */
export const GLSL_CLEAR = /* glsl */ `
float bg2ClearBox(vec2 p, vec4 r, float radius, float feather) {
  vec2 hs = r.zw * 0.5;
  if (hs.x <= 0.0 || hs.y <= 0.0) return 1.0;
  float rad = min(radius, min(hs.x, hs.y));
  vec2 q = abs(p - (r.xy + hs)) - hs + rad;
  float d = length(max(q, 0.0)) + min(max(q.x, q.y), 0.0) - rad;
  return smoothstep(0.0, max(feather, 0.0001), d);
}
float bg2Clear(vec2 px) {
  float k = 1.0;
  if (clearN > 0.5) k *= bg2ClearBox(px, clearRect0, clearShape01.x, clearShape01.y);
  if (clearN > 1.5) k *= bg2ClearBox(px, clearRect1, clearShape01.z, clearShape01.w);
  if (clearN > 2.5) k *= bg2ClearBox(px, clearRect2, clearShape23.x, clearShape23.y);
  if (clearN > 3.5) k *= bg2ClearBox(px, clearRect3, clearShape23.z, clearShape23.w);
  return k;
}
`;

/** `dFar(c)`: distance from `c` (aspect-corrected, y up) to the farthest box corner. */
export const GLSL_FAR = /* glsl */ `
float dFar(vec2 c) {
  float a = length(c);
  float b = length(c - vec2(u_aspect, 0.0));
  float d = length(c - vec2(0.0, 1.0));
  float e = length(c - vec2(u_aspect, 1.0));
  return max(max(a, b), max(d, e));
}
`;
