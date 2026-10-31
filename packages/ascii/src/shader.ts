/**
 * Shared GLSL ES 3.00 for every scene. Two passes, one full-screen triangle each:
 *
 *   FIELD  cols x rows RGBA8 target. A scene's `field()` runs once per CELL;
 *          the pass writes
 *            R  luminance (x the mask in "level" scenes)
 *            G  hue row / 255 (0 slate, 1 accent, 2 star white)
 *            B  opacity (x the mask in "alpha" scenes; 1 in the Eclipse)
 *            A  glyph select / 255: 0..V-1 picks a variant of the tone's glyph,
 *               128 + k picks the scene's k-th fixed-tone extra symbol
 *   BLIT   full canvas. Each pixel finds its cell, Bayer-dithers the luminance
 *          to a glyph level and samples the glyph atlas the engine baked once.
 *
 * A scene is one function, `vec4 field(ivec2 cell, vec2 px, float t)`, returning
 * (luminance 0..1, hue row, glyph select, opacity 0..1) for the cell whose
 * centre is `px` (device px, y down). Uniforms every scene may read:
 *   uRes device px, uGrid cols/rows, uCell cell size in device px,
 *   uOrb the Eclipse's orb, uFocus the scene's anchor (see `scenePlacement`).
 */
export const VERT = `#version 300 es
void main(){ vec2 p = vec2(float((gl_VertexID<<1)&2), float(gl_VertexID&2)); gl_Position = vec4(p*2.0-1.0, 0.0, 1.0); }`;

const FIELD_PRELUDE = `#version 300 es
precision highp float; precision highp int;
uniform float uT;
uniform vec2 uRes;
uniform vec2 uGrid;
uniform vec2 uCell;
/* Orb centre (xy) and radius (z), device px. */
uniform vec3 uOrb;
/* The scene's anchor, device px. */
uniform vec4 uFocus;
/* 1 when the mask lowers opacity, 0 when it lowers the tone. */
uniform float uMaskAlpha;
uniform sampler2D uMask;
float hash2(ivec2 p){ uint h = uint(p.x)*374761393u ^ uint(p.y)*668265263u; h = (h ^ (h>>13u))*1274126177u; return float(h ^ (h>>16u))/4294967295.0; }
float hash3(ivec2 p, int k){ uint h = uint(p.x)*374761393u ^ uint(p.y)*668265263u ^ uint(k)*2246822519u; h = (h ^ (h>>13u))*1274126177u; return float(h ^ (h>>16u))/4294967295.0; }
`;

const FIELD_MAIN = `
out vec4 o;
void main(){
	ivec2 cr = ivec2(gl_FragCoord.xy);
	ivec2 cell = ivec2(cr.x, int(uGrid.y) - 1 - cr.y);
	vec2 px = (vec2(cell) + 0.5) * uCell;
	vec4 s = field(cell, px, uT);
	float m = texelFetch(uMask, cell, 0).r;
	float lm = mix(m, 1.0, uMaskAlpha);
	float am = mix(1.0, m, uMaskAlpha) * s.w;
	o = vec4(s.x * lm, s.y / 255.0, am, s.z / 255.0);
}`;

/** The field program for a scene's `field()` body: the shared prelude, the body, the shared main. */
export function fieldFrag(body: string): string {
	return FIELD_PRELUDE + body + FIELD_MAIN;
}

export const BLIT_FRAG = `#version 300 es
precision highp float; precision highp int;
uniform vec2 uRes;
uniform vec2 uGrid;
uniform vec2 uCell;
uniform sampler2D uField;
uniform sampler2D uAtlas;
uniform float uLevels;
/* Glyphs per tone level. */
uniform float uVariants;
/* First atlas column of the extra symbols (levels x variants). */
uniform float uExtraBase;
uniform float uCut;
uniform float uDither;
out vec4 o;
const float BAYER[16] = float[16](0.,8.,2.,10.,12.,4.,14.,6.,3.,11.,1.,9.,15.,7.,13.,5.);
void main(){
	vec2 p = vec2(gl_FragCoord.x, uRes.y - gl_FragCoord.y);
	ivec2 c = ivec2(floor(p / uCell));
	ivec2 fr = ivec2(c.x, int(uGrid.y) - 1 - c.y);
	vec4 f = texelFetch(uField, fr, 0);
	int hue = int(f.g * 255.0 + 0.5);
	int sel = int(f.a * 255.0 + 0.5);
	int col = -1;
	if (sel >= 128) {
		col = int(uExtraBase) + sel - 128;
	} else if (f.r >= uCut) {
		float b = BAYER[((c.y & 3) << 2) | (c.x & 3)] / 16.0 - 0.47;
		int idx = clamp(int(floor(f.r * (uLevels - 1.0) + 0.5 + b * uDither)), 0, int(uLevels) - 1);
		if (idx > 0) col = idx * int(uVariants) + sel % int(uVariants);
	}
	vec4 px4 = vec4(0.0);
	if (col >= 0) {
		ivec2 cs = ivec2(uCell);
		ivec2 loc = ivec2(p) - c * cs;
		px4 = texelFetch(uAtlas, ivec2(col * cs.x + loc.x, hue * cs.y + loc.y), 0) * f.b;
	}
	o = px4;
}`;
