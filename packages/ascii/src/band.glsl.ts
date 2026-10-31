/**
 * Band: a night sky with one stippled diagonal Milky Way and a small dotted
 * constellation beside it. The river is the scene: a dense core of `.` `'`
 * `,` and a few `:` that falls away to a soft, thin edge, rising to the
 * right. Around it the sky is sparse: a few faint `.` and a handful of
 * brighter stars (some accent) that twinkle by tone, each at its own 0.1-0.3
 * Hz and phase. The river drifts a few cells a minute. The constellation is
 * five stars joined by dotted links that touch them (never a slash), on the
 * river's lower side.
 *
 * Extras (glyph select 128 + k): 0 "." (the links, brighter than the ramp's).
 *
 * uFocus: x of the river's centre line at the top edge, x at the bottom edge,
 * its half-width, all device px (see `scenePlacement`).
 */
export const BAND = `
/* Band drift, cells per second (a few a minute). */
const vec2 DRIFT = vec2(0.07, 0.03);

/* The constellation's stars in the river's frame: (how far down the river, 0..1; how far to its lower side, in half-widths). */
const vec2 CS[5] = vec2[5](vec2(0.3, 1.7), vec2(0.45, 2.5), vec2(0.58, 1.8), vec2(0.72, 2.6), vec2(0.5, 3.4));

/* A star of the constellation before it is fitted to the canvas, device px. */
vec2 csRaw(int i) {
	vec2 dir = vec2(uFocus.y - uFocus.x, uRes.y);
	vec2 dn = dir / length(dir);
	vec2 nn = vec2(dn.y, -dn.x);
	return vec2(uFocus.x, 0.0) + dir * CS[i].x + nn * CS[i].y * uFocus.z;
}

/*
 * A star of the constellation, device px. The shape keeps its form: it is
 * scaled down if it is larger than the canvas allows and then moved inside
 * the canvas's margin, never squashed against an edge.
 */
vec2 csPos(int i) {
	vec2 lo = vec2(1e5);
	vec2 hi = vec2(-1e5);
	for (int j = 0; j < 5; j++) {
		vec2 p = csRaw(j);
		lo = min(lo, p);
		hi = max(hi, p);
	}
	vec2 mid = 0.5 * (lo + hi);
	vec2 mlo = 0.06 * uRes;
	vec2 mhi = 0.94 * uRes;
	vec2 size = max(hi - lo, vec2(1.0));
	float k = min(1.0, min((mhi.x - mlo.x) / size.x, (mhi.y - mlo.y) / size.y));
	vec2 slo = mid + (lo - mid) * k;
	vec2 shi = mid + (hi - mid) * k;
	vec2 shift = max(mlo - slo, 0.0) + min(mhi - shi, 0.0);
	return mid + (csRaw(i) - mid) * k + shift;
}

float tw(ivec2 c, float lo) {
	float f = 0.1 + 0.2 * hash3(c, 52);
	return mix(lo, 1.0, 0.5 + 0.5 * sin(6.2832 * (f * uT + hash3(c, 53))));
}

vec4 field(ivec2 cell, vec2 px, float t) {
	float W = uRes.x;
	float H = uRes.y;
	vec2 cs = uCell;

	// the constellation: stars, then dotted links that touch them
	vec2 pts[5];
	ivec2 sc[5];
	for (int i = 0; i < 5; i++) {
		pts[i] = csPos(i);
		sc[i] = ivec2(floor(pts[i] / cs));
	}
	for (int i = 0; i < 5; i++) {
		if (cell == sc[i]) {
			bool accent = i == 1;
			bool bright = i == 1 || i == 3;
			return vec4(bright ? 1.0 : 0.86, accent ? 1.0 : 0.0, 0.0, 1.0);
		}
	}
	for (int i = 0; i < 4; i++) {
		int b = i == 3 ? 4 : i + 1;
		int a = i == 3 ? 2 : i;
		vec2 p0 = (vec2(sc[a]) + 0.5) * cs;
		vec2 p1 = (vec2(sc[b]) + 0.5) * cs;
		vec2 seg = p1 - p0;
		float len = length(seg);
		vec2 nn = seg / len;
		vec2 rel = px - p0;
		float s = dot(rel, nn);
		float perp = abs(rel.x * nn.y - rel.y * nn.x);
		float support = 0.5 * (abs(nn.y) * cs.x + abs(nn.x) * cs.y);
		if (s < 0.0 || s > len || perp > 0.5 * support) continue;
		// a dot in most cells along the link, anchored at the star it leaves
		float stride = 1.4 * length(nn * cs);
		float ph = fract(s / stride);
		if (ph < 0.92) return vec4(0.0, 0.0, 128.0, 1.0);
	}

	// the stars, three tiers on the cell grid, thinning toward the left
	float free = smoothstep(0.25 * W, 0.6 * W, px.x);
	float m = (1.0 - hash3(cell, 50)) / max(free, 0.05);
	if (free > 0.0) {
		if (m < 0.0012) return vec4(tw(cell, 0.78), hash3(cell, 51) > 0.5 ? 1.0 : 0.0, 0.0, 1.0);
		if (m < 0.006) return vec4(0.86 * tw(cell, 0.8), 0.0, 0.0, 1.0);
		if (m < 0.014) return vec4(0.7 * tw(cell, 0.8), 0.0, 0.0, 1.0);
		if (m < 0.02) return vec4(0.3 * tw(cell, 0.35), 0.0, 0.0, 1.0);
	}

	// the Milky Way: a dense stipple around a diagonal centre line, drifting
	vec2 sp = px - DRIFT * cs * t;
	vec2 a = vec2(uFocus.x, 0.0);
	vec2 dir = vec2(uFocus.y - uFocus.x, H);
	vec2 dn = dir / length(dir);
	vec2 rp = sp - a;
	float perp = (rp.x * dn.y - rp.y * dn.x);
	float u = abs(perp) / uFocus.z;
	// a flat core, then a soft edge that thins to nothing
	float core = 1.0 - smoothstep(0.3, 1.5, u);
	if (core < 0.01) return vec4(0.0);
	ivec2 sc2 = ivec2(floor(sp / cs));
	float clump = 0.88 + 0.12 * sin(sp.x * 0.012 + sp.y * 0.007) * sin(sp.y * 0.011 - sp.x * 0.004 + 1.7);
	if (hash3(sc2, 41) > 0.97 * core * clump) return vec4(0.0);
	// the core carries the brighter marks, the edge only the faintest
	float q = hash3(sc2, 42) * core;
	return vec4(q < 0.12 ? 0.18 : (q < 0.26 ? 0.34 : (q < 0.5 ? 0.5 : 0.66)), 0.0, 0.0, 1.0);
}`;
