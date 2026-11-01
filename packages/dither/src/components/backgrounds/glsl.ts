/**
 * Small GLSL helpers shared by the part-2 backgrounds. The engine prepends
 * GLSL_NOISE (hash11, hash21, hash22, vnoise, snoise, fbm, rot2, …); these
 * add what the scenes need and are prefixed `bg2` so they never clash.
 */

/** A float literal GLSL accepts: always has a decimal point, never an exponent. */
export function glslFloat(n: number): string {
	const v = Number.isFinite(n) ? n : 0;
	const s = v.toFixed(6).replace(/0+$/, "");
	return s.endsWith(".") ? `${s}0` : s;
}

export function glslVec2(x: number, y: number): string {
	return `vec2(${glslFloat(x)}, ${glslFloat(y)})`;
}

export function glslVec3(x: number, y: number, z: number): string {
	return `vec3(${glslFloat(x)}, ${glslFloat(y)}, ${glslFloat(z)})`;
}

export const BG2_COMMON = `
mat2 bg2Rot(float a) {
  float c = cos(a);
  float s = sin(a);
  return mat2(c, -s, s, c);
}
float bg2Seg(vec2 p, vec2 a, vec2 b) {
  vec2 pa = p - a;
  vec2 ba = b - a;
  float h = clamp(dot(pa, ba) / max(dot(ba, ba), 1e-9), 0.0, 1.0);
  return length(pa - ba * h);
}
/* Direction for a CSS angle: 0 is up, 90 is right (y up). */
vec2 bg2Dir(float deg) {
  float r = radians(deg);
  return vec2(sin(r), cos(r));
}
`;
