/**
 * The Eclipse's shaders: a pixel-dithered ringed planet in two passes, one
 * full-screen triangle each (the vertex shader is shader.ts's `VERT`).
 *
 *   FIELD  cols x rows R8 target, one fragment per cell (2 CSS px x DPR). It
 *          shades the cell once and quantises it to three tones through an
 *          8x8 ordered dither, writing the tone index / 2.
 *   BLIT   full canvas. Each pixel finds its cell and reads the tone with
 *          texelFetch (nearest): tone 0 is transparent (the page and its
 *          halftone floor show through), tone 1 is uC1, tone 2 is uC2,
 *          premultiplied, alpha 1.
 *
 * The picture: a lit sphere with slowly drifting fbm bands, a ring tilted
 * -0.38 rad (its front half over the sphere, its back half behind it), a soft
 * glow past the limb, an fbm nebula haze and sparse twinkling single-cell
 * stars. The light eases toward the cursor (`uCur`, `uCurW`).
 *
 * Space is the orb's: `d` is the cell's offset from the orb centre in orb
 * radii, y up. The haze is noise over the canvas in units of its height.
 *
 * `uMask` is the cell grid's RG8 mask: R the soft dim (panel, quiet halo), G
 * the hard quiet region (text sits here: tone 0, stars off). Row 0 is the top,
 * as in the field target's cell numbering (target row 0 is the bottom).
 */
export const PLANET_FIELD_FRAG = `#version 300 es
precision highp float; precision highp int;
/* Canvas, device px. */
uniform vec2 uRes;
/* Cell size, device px. */
uniform float uCell;
/* Mask grid: cols, rows. */
uniform vec2 uGrid;
uniform float uT;
/* Orb centre (xy, device px, y down) and radius (z). */
uniform vec3 uOrb;
/* Field strength over the whole canvas (lower when the orb sits behind the card). */
uniform float uDim;
/* Eased cursor, as fractions of the canvas with y up, and how much it counts (0 = default light). */
uniform vec2 uCur;
uniform float uCurW;
uniform sampler2D uMask;
out vec4 o;

float h(vec2 p){ return fract(sin(dot(p, vec2(127.1, 311.7))) * 43758.5453); }
float n(vec2 p){
	vec2 i = floor(p), f = fract(p);
	f = f * f * (3.0 - 2.0 * f);
	return mix(mix(h(i), h(i + vec2(1.0, 0.0)), f.x), mix(h(i + vec2(0.0, 1.0)), h(i + 1.0), f.x), f.y);
}
float fbm(vec2 p){
	float s = 0.0, a = 0.5;
	for (int i = 0; i < 5; i++) { s += a * n(p); p = p * 2.03 + vec2(17.1, 3.7); a *= 0.5; }
	return s;
}
float b2(vec2 a){ a = floor(a); return fract(dot(a, vec2(0.5, a.y * 0.75))); }
float b4(vec2 a){ return b2(0.5 * a) * 0.25 + b2(a); }
float b8(vec2 a){ return b4(0.5 * a) * 0.25 + b2(a); }

void main(){
	vec2 cell = vec2(gl_FragCoord.x - 0.5, uGrid.y - 0.5 - gl_FragCoord.y);
	vec2 m = texelFetch(uMask, ivec2(cell), 0).rg;
	if (m.g < 0.004) { o = vec4(0.0); return; }

	vec2 pu = vec2((cell.x + 0.5) * uCell, uRes.y - (cell.y + 0.5) * uCell) / uRes.y;
	vec2 mm = uCur * uRes / uRes.y;
	vec2 oc = vec2(uOrb.x, uRes.y - uOrb.y) / uRes.y;
	float rad = uOrb.z / uRes.y;
	vec2 d = (pu - oc) / rad;
	float r = length(d);

	float hz = 0.0;
	if (r >= 1.0) {
		float w = fbm(pu * 1.6 + vec2(uT * 0.02, 0.0));
		hz = smoothstep(0.42, 0.95, fbm(pu * 2.2 + w * 1.6 - vec2(0.0, uT * 0.015)));
		hz *= mix(0.35, 1.0, 1.0 - smoothstep(0.7, 2.5, r));
	}
	float f = hz * 0.55;

	float ca = cos(-0.38), sa = sin(-0.38);
	vec2 dr = vec2(ca * d.x - sa * d.y, sa * d.x + ca * d.y);
	float rr = length(vec2(dr.x, dr.y * 3.4));
	vec2 lp = mix(vec2(-0.55, 0.6), (mm - oc) / rad * 0.75, uCurW);
	vec3 L = normalize(vec3(lp, 0.75));
	/* Gaussian bands, so the edges dither out: inner (centre 1.6 radii, only its core reaches tone 2), outer (1.95, mostly tone 1 with sparse tone 2), a dark gap between, and a faint dust band out to ~2.15. */
	float ring = exp(-pow((rr - 1.6) * 11.0, 2.0)) * 0.95 + exp(-pow((rr - 1.95) * 13.0, 2.0)) * 0.55 + exp(-pow((rr - 2.07) * 9.0, 2.0)) * 0.18;
	if (ring > 0.01) {
		/* The ring plane's point under this cell, and the planet's shadow (a cylinder along the light) on it. */
		vec3 P = vec3(d, -3.25 * dr.y);
		float pl = dot(P, L);
		if (pl < 0.0) ring *= mix(0.12, 1.0, smoothstep(0.9, 1.1, length(P - pl * L)));
	}

	if (r < 1.0) {
		vec3 nn = vec3(d, sqrt(max(0.0, 1.0 - r * r)));
		float lam = max(dot(nn, L), 0.0);
		float lon = atan(nn.x, nn.z) + uT * 0.12;
		f = 0.04 + lam * (0.5 + 0.75 * fbm(vec2(lon * 1.4, nn.y * 5.5)));
		if (dr.y < 0.0) f = max(f, ring);
	} else {
		f = max(f, ring);
		f += exp(-(r - 1.0) * 4.76) * 0.22;
	}
	f += uCurW * 0.38 * exp(-dot(pu - mm, pu - mm) * 16.0) * (0.45 + hz);
	f *= smoothstep(0.0, 0.14, pu.y) * m.r * m.g * uDim;

	float lv = floor(clamp(f, 0.0, 1.0) * 2.0 + b8(cell));
	if (r >= 1.0 && h(cell) > 0.9965 && m.r * m.g > 0.5) {
		float s = 0.55 + 0.45 * sin(uT * 1.6 + h(cell + 3.0) * 40.0);
		lv = max(lv, s > 0.2 ? 2.0 : 1.0);
	}
	o = vec4(lv * 0.5, 0.0, 0.0, 1.0);
}`;

export const PLANET_BLIT_FRAG = `#version 300 es
precision highp float; precision highp int;
uniform vec2 uRes;
uniform float uCell;
uniform vec2 uGrid;
uniform sampler2D uField;
uniform vec3 uC1;
uniform vec3 uC2;
out vec4 o;
void main(){
	vec2 p = vec2(gl_FragCoord.x, uRes.y - gl_FragCoord.y);
	ivec2 c = ivec2(clamp(floor(p / uCell), vec2(0.0), uGrid - 1.0));
	int tone = int(texelFetch(uField, ivec2(c.x, int(uGrid.y) - 1 - c.y), 0).r * 2.0 + 0.5);
	o = tone < 1 ? vec4(0.0) : vec4(tone < 2 ? uC1 : uC2, 1.0);
}`;
