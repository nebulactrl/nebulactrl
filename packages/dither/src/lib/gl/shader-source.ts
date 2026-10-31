/**
 * GLSL for the two-stage DitherShader.
 *
 * Stage 1 evaluates the user's `field()` (and optional `colorField()`) once per
 * dither cell into a cols x rows RGBA8 texture (alpha = tone, rgb = colour).
 * Stage 2 upscales that texture to the canvas, snaps the tone to `levels`,
 * applies the ordered Bayer threshold per cell and draws the cell as a square
 * or a dot in the palette ramp.
 *
 * GLSL_NOISE's simplex noise follows Ashima Arts / Stefan Gustavson's 2D
 * simplex noise (MIT).
 */
import { MAX_PALETTE } from "./palette";
import { type UniformSpec, uniformDeclarations } from "./uniforms";

/**
 * Helpers prepended to every field: hashes, value + simplex noise, fbm,
 * rotation and signed-distance primitives, hue ramp. Names are global.
 */
export const GLSL_NOISE = /* glsl */ `
float hash11(float p) {
  p = fract(p * 0.1031);
  p *= p + 33.33;
  p *= p + p;
  return fract(p);
}
float hash21(vec2 p) {
  vec3 p3 = fract(vec3(p.xyx) * 0.1031);
  p3 += dot(p3, p3.yzx + 33.33);
  return fract((p3.x + p3.y) * p3.z);
}
vec2 hash22(vec2 p) {
  vec3 p3 = fract(vec3(p.xyx) * vec3(0.1031, 0.1030, 0.0973));
  p3 += dot(p3, p3.yzx + 33.33);
  return fract((p3.xx + p3.yz) * p3.zy);
}
float vnoise(vec2 p) {
  vec2 i = floor(p);
  vec2 f = fract(p);
  vec2 u = f * f * (3.0 - 2.0 * f);
  return mix(
    mix(hash21(i), hash21(i + vec2(1.0, 0.0)), u.x),
    mix(hash21(i + vec2(0.0, 1.0)), hash21(i + vec2(1.0, 1.0)), u.x),
    u.y
  );
}
vec3 nc_permute(vec3 x) { return mod(((x * 34.0) + 10.0) * x, 289.0); }
float snoise(vec2 v) {
  const vec4 C = vec4(0.211324865405187, 0.366025403784439, -0.577350269189626, 0.024390243902439);
  vec2 i = floor(v + dot(v, C.yy));
  vec2 x0 = v - i + dot(i, C.xx);
  vec2 i1 = (x0.x > x0.y) ? vec2(1.0, 0.0) : vec2(0.0, 1.0);
  vec4 x12 = x0.xyxy + C.xxzz;
  x12.xy -= i1;
  i = mod(i, 289.0);
  vec3 p = nc_permute(nc_permute(i.y + vec3(0.0, i1.y, 1.0)) + i.x + vec3(0.0, i1.x, 1.0));
  vec3 m = max(0.5 - vec3(dot(x0, x0), dot(x12.xy, x12.xy), dot(x12.zw, x12.zw)), 0.0);
  m = m * m;
  m = m * m;
  vec3 x = 2.0 * fract(p * C.www) - 1.0;
  vec3 h = abs(x) - 0.5;
  vec3 ox = floor(x + 0.5);
  vec3 a0 = x - ox;
  m *= 1.79284291400159 - 0.85373472095314 * (a0 * a0 + h * h);
  vec3 g;
  g.x = a0.x * x0.x + h.x * x0.y;
  g.yz = a0.yz * x12.xz + h.yz * x12.yw;
  return 130.0 * dot(m, g);
}
float fbmN(vec2 p, int octaves) {
  float amp = 0.5;
  float sum = 0.0;
  mat2 r = mat2(0.8, -0.6, 0.6, 0.8);
  for (int i = 0; i < 6; i++) {
    if (i >= octaves) break;
    sum += amp * vnoise(p);
    p = r * p * 2.03 + 17.1;
    amp *= 0.5;
  }
  return sum / (1.0 - pow(0.5, float(clamp(octaves, 1, 6))));
}
float fbm(vec2 p) { return fbmN(p, 5); }
mat2 rot2(float a) {
  float c = cos(a);
  float s = sin(a);
  return mat2(c, -s, s, c);
}
float sdCircle(vec2 p, float r) { return length(p) - r; }
float sdBox(vec2 p, vec2 b) {
  vec2 d = abs(p) - b;
  return length(max(d, 0.0)) + min(max(d.x, d.y), 0.0);
}
float sdSegment(vec2 p, vec2 a, vec2 b) {
  vec2 pa = p - a;
  vec2 ba = b - a;
  float h = clamp(dot(pa, ba) / dot(ba, ba), 0.0, 1.0);
  return length(pa - ba * h);
}
float smin(float a, float b, float k) {
  float h = max(k - abs(a - b), 0.0) / k;
  return min(a, b) - h * h * k * 0.25;
}
vec3 spectrum(float h) {
  return clamp(abs(mod(h * 6.0 + vec3(0.0, 4.0, 2.0), 6.0) - 3.0) - 1.0, 0.0, 1.0);
}
`;

export const VERTEX_SOURCE = /* glsl */ `#version 300 es
out vec2 v_uv;
void main() {
  // One oversized triangle covers the viewport; no vertex buffers.
  vec2 p = vec2(float((gl_VertexID << 1) & 2), float(gl_VertexID & 2));
  v_uv = p;
  gl_Position = vec4(p * 2.0 - 1.0, 0.0, 1.0);
}
`;

/** Uniforms shared by both stages' preludes. Names are public API for fields. */
const ENGINE_UNIFORMS = /* glsl */ `
uniform vec2 u_res;
uniform float u_aspect;
uniform float u_time;
uniform vec2 u_pointer;
uniform float u_cell;
uniform int u_palN;
uniform vec4 u_pal[${MAX_PALETTE}];
`;

const PALETTE_COLOR = /* glsl */ `
// The ink colours (every palette entry after the paper) as a smooth 0..1 ramp.
vec4 paletteColor(float x) {
  int count = u_palN - 1;
  if (count <= 1) return u_pal[min(1, u_palN - 1)];
  float pos = clamp(x, 0.0, 1.0) * float(count - 1);
  int i = min(int(floor(pos)), count - 2);
  return mix(u_pal[1 + i], u_pal[2 + i], pos - float(i));
}
`;

export interface FieldProgramSources {
	field: string;
	colorField: string | undefined;
	uniforms: readonly UniformSpec[];
	textures: readonly string[];
}

const DEFAULT_COLOR_FIELD = "";

const FIELD_3ARG =
	/\bfloat\s+field\s*\(\s*vec2\s+\w+\s*,\s*vec2\s+\w+\s*,\s*float\s+\w+\s*\)/;
const COLOR_3ARG =
	/\b(vec3|vec4)\s+colorField\s*\(\s*vec2\s+\w+\s*,\s*vec2\s+\w+\s*,\s*float\s+\w+\s*\)/;

/**
 * Fields may be written against either signature:
 *
 * - ours:        `float field(vec2 uv, float t)`, `vec3 colorField(vec2 uv, float t, float tone)`
 * - three-arg:   `float field(vec2 uv, vec2 res, float t)`, `vec3|vec4 colorField(vec2 uv, vec2 res, float t)`
 *   where `res` is the grid size in cells.
 *
 * The three-arg form is renamed and called through a small wrapper, so the
 * rest of the pipeline sees one shape.
 */
export function adaptFieldSources(
	field: string,
	colorField: string | undefined,
): { field: string; colorField: string | undefined } {
	let nextField = field;
	if (FIELD_3ARG.test(field)) {
		nextField = `${field.replace(FIELD_3ARG, (m) => m.replace("field", "nc_field3"))}
float field(vec2 uv, float t) { return nc_field3(uv, u_res, t); }
`;
	}
	let nextColor = colorField;
	if (colorField !== undefined) {
		const match = COLOR_3ARG.exec(colorField);
		if (match) {
			const swizzle = match[1] === "vec4" ? ".rgb" : "";
			nextColor = `${colorField.replace(COLOR_3ARG, (m) => m.replace("colorField", "nc_colorField3"))}
vec3 colorField(vec2 uv, float t, float tone) { return nc_colorField3(uv, u_res, t)${swizzle}; }
`;
		}
	}
	return { field: nextField, colorField: nextColor };
}

/** The stage-1 fragment and where the author's GLSL sits inside it. */
export interface FieldFragment {
	source: string;
	/** The field as placed (a three-argument field gains a wrapper at its end). */
	fieldSource: string;
	/** 1-based line of `source` where `fieldSource` starts. */
	fieldStartLine: number;
	colorSource: string | undefined;
	/** 1-based line of `source` where `colorSource` starts (after the field). */
	colorStartLine: number;
}

const newlines = (text: string) => text.split("\n").length - 1;

/** Stage 1 as one source, plus the line offsets that map compile errors back. */
export function assembleFieldFragment(src: FieldProgramSources): FieldFragment {
	const samplers = src.textures
		.map((name) => `uniform sampler2D ${name};`)
		.join("\n");
	const { field: fieldSource, colorField: colorSource } = adaptFieldSources(
		src.field,
		src.colorField,
	);
	const hasColor = colorSource !== undefined;
	const head = `#version 300 es
precision highp float;
precision highp int;
precision highp sampler2D;
in vec2 v_uv;
out vec4 o_color;
${ENGINE_UNIFORMS}
${uniformDeclarations(src.uniforms)}
${samplers}
${GLSL_NOISE}
${PALETTE_COLOR}
`;
	const fieldStartLine = newlines(head) + 1;
	const source = `${head}${fieldSource}
${colorSource ?? DEFAULT_COLOR_FIELD}
void main() {
  vec2 uv = gl_FragCoord.xy / u_res;
  float tone = clamp(field(uv, u_time), 0.0, 1.0);
  ${hasColor ? "vec3 c = clamp(colorField(uv, u_time, tone), 0.0, 1.0);\n  o_color = vec4(c, tone);" : "o_color = vec4(0.0, 0.0, 0.0, tone);"}
}
`;
	return {
		source,
		fieldSource,
		fieldStartLine,
		colorSource,
		colorStartLine: fieldStartLine + newlines(fieldSource) + 1,
	};
}

/** One compile error, placed on a line of the author's own GLSL. */
export interface CompileDiagnostic {
	/** 1-based line within the author's source. */
	line: number;
	message: string;
	/** That line's text. */
	source: string;
}

const LOG_ERROR = /^\s*ERROR:\s*\d+:(\d+):\s*(.*)$/;

/**
 * The errors of a shader info log (`ERROR: 0:<line>: <message>`) that fall
 * inside the author's GLSL, with lines counted from its first line.
 * `fieldStartLine` is where `fieldSource` starts in the assembled source.
 * Errors in the engine's own code are left out.
 */
export function mapCompileLog(
	log: string,
	fieldStartLine: number,
	fieldSource: string,
): CompileDiagnostic[] {
	const lines = fieldSource.split("\n");
	const out: CompileDiagnostic[] = [];
	for (const raw of log.split("\n")) {
		const match = LOG_ERROR.exec(raw.replace(/[\0\r]/g, ""));
		if (!match) continue;
		const line = Number(match[1]) - fieldStartLine + 1;
		if (line < 1 || line > lines.length) continue;
		out.push({
			line,
			message: (match[2] ?? "").trim(),
			source: lines[line - 1] ?? "",
		});
	}
	return out;
}

/** `[dither] field failed to compile, line 3: <message>` and the offending line under it. */
export function formatCompileDiagnostic(
	diagnostic: CompileDiagnostic,
	label: "field" | "colorField",
): string {
	return `[dither] ${label} failed to compile, line ${diagnostic.line}: ${diagnostic.message}\n${diagnostic.source.trimEnd()}`;
}

/**
 * What to log when the stage-1 fragment fails: each error placed on a line of
 * the `field` or `colorField` the author wrote, or the raw log when the
 * errors lie outside both.
 */
export function describeFieldCompileFailure(
	log: string,
	fragment: FieldFragment,
): string {
	const found = [
		...mapCompileLog(log, fragment.fieldStartLine, fragment.fieldSource).map(
			(d) => formatCompileDiagnostic(d, "field"),
		),
		...(fragment.colorSource === undefined
			? []
			: mapCompileLog(log, fragment.colorStartLine, fragment.colorSource).map(
					(d) => formatCompileDiagnostic(d, "colorField"),
				)),
	];
	return found.length > 0
		? found.join("\n")
		: `[dither] shader compile failed\n${log}`;
}

/** Prelude functions a field would redefine by declaring its own. */
const PRELUDE_FUNCTIONS = [
	"hash21",
	"snoise",
	"fbm",
	"vnoise",
	"spectrum",
	"paletteColor",
] as const;

const GLSL_COMMENT = /\/\*[\s\S]*?\*\/|\/\/[^\n]*/g;
const PRELUDE_DECLARATION = new RegExp(
	`\\b(?:void|bool|u?int|float|[biu]?vec[234]|mat[234](?:x[234])?)\\s+(${PRELUDE_FUNCTIONS.join("|")})\\s*\\(`,
	"g",
);

/**
 * The prelude functions (`hash21`, `snoise`, `fbm`, `vnoise`, `spectrum`,
 * `paletteColor`) that `source` declares again; a matching signature fails
 * to compile. Calls and comments do not count.
 */
export function findPreludeRedefinitions(source: string): string[] {
	const names = new Set<string>();
	for (const match of source
		.replace(GLSL_COMMENT, " ")
		.matchAll(PRELUDE_DECLARATION)) {
		names.add(match[1] as string);
	}
	return [...names];
}

/** Fragment shader for stage 2: Bayer threshold, levels, shape and palette. */
export const DITHER_FRAGMENT = /* glsl */ `#version 300 es
precision highp float;
precision highp int;
precision highp sampler2D;
in vec2 v_uv;
out vec4 o_color;
uniform sampler2D u_cells;
uniform vec2 u_grid;
uniform float u_sub;
uniform float u_levels;
uniform float u_n;
uniform sampler2D u_blue;
uniform float u_shape;
uniform float u_hasColor;
uniform int u_palN;
uniform vec4 u_pal[${MAX_PALETTE}];

float bayer2(vec2 a) { a = floor(a); return fract(a.x / 2.0 + a.y * a.y * 0.75); }
float bayer4(vec2 a) { return bayer2(0.5 * a) * 0.25 + bayer2(a); }
float bayer8(vec2 a) { return bayer4(0.5 * a) * 0.25 + bayer2(a); }

void main() {
  vec2 frag = floor(gl_FragCoord.xy);
  vec2 cell = floor(frag / u_sub);
  vec4 s = texelFetch(u_cells, ivec2(cell), 0);

  float steps = max(u_levels - 1.0, 1.0);
  float q = floor(s.a * steps + 0.5) / steps;

  // Anchor the matrix to the top-left so resizing never shifts the pattern.
  vec2 bc = vec2(cell.x, u_grid.y - 1.0 - cell.y);
  float thr;
  if (u_n < 3.0) thr = bayer2(bc) + 0.125;
  else if (u_n < 6.0) thr = bayer4(bc) + 0.03125;
  else if (u_n < 12.0) thr = bayer8(bc) + 0.0078125;
  else {
    // Blue noise: a 64x64 tile of 8-bit thresholds, tiled from the top-left.
    ivec2 b = ivec2(mod(bc, 64.0));
    thr = texelFetch(u_blue, b, 0).r + 0.001953125;
  }

  bool inside = true;
  if (u_shape > 0.5 && u_sub > 1.5) {
    vec2 f = (mod(frag, u_sub) + 0.5) / u_sub - 0.5;
    inside = length(f) < 0.5;
  }

  vec4 col;
  if (u_hasColor > 0.5) {
    col = (q > thr && inside) ? vec4(s.rgb, 1.0) : u_pal[0];
  } else {
    float pos = q * float(u_palN - 1);
    int i = min(int(floor(pos)), u_palN - 2);
    float f = pos - float(i);
    col = (f > thr && inside) ? u_pal[i + 1] : u_pal[i];
  }
  o_color = vec4(col.rgb * col.a, col.a);
}
`;
