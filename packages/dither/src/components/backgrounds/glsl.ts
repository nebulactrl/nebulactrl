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

/**
 * Opt-in line and disc recipes, measured in cells so a line stays the same
 * number of cells wide at any DPR. Append it to a field's source; positions
 * and distances are in cells (uv * u_res).
 */
export const BG2_LINES = /* glsl */ `
/* A line widthCells wide, dist cells from its centre: 1 on the line, 0 off it, one cell of soft edge. */
float bg2LineCells(float dist, float widthCells) {
  return clamp(widthCells * 0.5 + 0.5 - dist, 0.0, 1.0);
}
/* count contour lines of the height field h (0..1), each widthCells wide on any slope. Uses screen derivatives, so call it in uniform control flow; fbm octaves in h break the lines up. */
float bg2Contour(float h, float count, float widthCells) {
  float s = h * count;
  float d = abs(fract(s + 0.5) - 0.5);
  float slope = max(length(vec2(dFdx(s), dFdy(s))), 0.00001);
  return bg2LineCells(d / slope, widthCells);
}
/* The arc of radius r round c from angle a0 to a1 (radians, counter-clockwise from +x), widthCells wide, with round ends. A span of 2 pi or more is a full ring. */
float bg2Arc(vec2 p, vec2 c, float r, float a0, float a1, float widthCells) {
  vec2 d = p - c;
  float mid = 0.5 * (a0 + a1);
  float reach = 0.5 * (a1 - a0);
  float off = abs(mod(atan(d.y, d.x) - mid + 3.14159265, 6.2831853) - 3.14159265);
  float dist;
  if (off <= reach) {
    dist = abs(length(d) - r);
  } else {
    vec2 e0 = c + r * vec2(cos(a0), sin(a0));
    vec2 e1 = c + r * vec2(cos(a1), sin(a1));
    dist = min(length(p - e0), length(p - e1));
  }
  return bg2LineCells(dist, widthCells);
}
/* A shaded disc of radius r at c: tone 0..1 from a lit sphere, lit from the screen direction lightDir (y up); 0 outside the disc, one cell of soft edge. */
float bg2Lit(vec2 p, vec2 c, float r, vec2 lightDir) {
  vec2 d = (p - c) / max(r, 0.0001);
  float cover = clamp(r - length(p - c) + 0.5, 0.0, 1.0);
  float z = sqrt(max(1.0 - dot(d, d), 0.0));
  vec3 l = normalize(vec3(lightDir, 0.75));
  float lit = clamp(dot(vec3(d, z), l), 0.0, 1.0);
  return mix(0.15, 1.0, lit) * cover;
}
`;
