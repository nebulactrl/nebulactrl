import { DOME_GAP_ROWS, DOME_OMEGA } from "./scenes";

/**
 * Dome: a calm night sky over a low horizon. Stars sit on gentle concentric
 * arcs around a pole above the top right edge (`uFocus.xy`), and the whole sky
 * turns about that pole by DOME_OMEGA, three degrees a minute: the only
 * motion. On the arcs, per cell: many faint `.` and `'` pinpricks, fewer `:`
 * and `+`, and a handful of bright `*`. One accent `*` (`uFocus.zw` at time 0,
 * carried round with the sky) is the only accent. Along the bottom a sparse
 * dotted horizon of `.` with a `_` ridge, thinning upward. The sky thins out
 * toward the left, so it is nearly empty behind text there.
 *
 * Extras (glyph select 128 + k): 0 "_".
 */
export const DOME = `
const float OMEGA = ${DOME_OMEGA};
const float GAP = ${DOME_GAP_ROWS.toFixed(1)};

vec4 field(ivec2 cell, vec2 px, float t) {
	float W = uRes.x;
	float H = uRes.y;
	float cw = uCell.x;
	float ch = uCell.y;

	// the horizon: a fixed ridge, dots thinning upward, "_" on the ridge
	float col = px.x / cw;
	float rb = floor((H - px.y) / ch);
	float ridge = max(floor(1.4 + 1.1 * sin(col * 0.045 + 1.3) + 0.8 * sin(col * 0.11 + 0.4) + 0.5 * sin(col * 0.27 + 2.1)), 0.0);
	if (rb == ridge) return hash3(cell, 61) < 0.85 ? vec4(0.0, 0.0, 128.0, 0.7) : vec4(0.0);
	if (rb < ridge) {
		return hash3(cell, 62) < 0.8 * (1.0 - (rb + 0.5) / (ridge + 1.0)) ? vec4(0.2, 0.0, 0.0, 0.7) : vec4(0.0);
	}

	vec2 Q = uFocus.xy;
	vec2 d = px - Q;
	float r = length(d);
	float theta = atan(d.y, d.x);

	// the accent star, on its arc, carried round with the sky
	vec2 hd = uFocus.zw - Q;
	float rh = max(floor(length(hd) / (GAP * ch) + 0.5), 4.0) * GAP * ch;
	float ha = atan(hd.y, hd.x) - OMEGA * t;
	if (ivec2(floor((Q + rh * vec2(cos(ha), sin(ha))) / uCell)) == cell) return vec4(1.0, 1.0, 0.0, 1.0);

	// the arcs: one cell per column where an arc runs flatter than 1:2, one per row where it is steeper
	float rk = r / (GAP * ch);
	float k = floor(rk + 0.5);
	if (k < 4.0) return vec4(0.0);
	float R = k * GAP * ch;
	bool shallow = abs(d.x) * cw <= abs(d.y) * ch;
	float off = shallow
		? px.y - (Q.y + sqrt(max(R * R - d.x * d.x, 0.0)))
		: px.x - (Q.x - sqrt(max(R * R - d.y * d.y, 0.0)));
	if (abs(off) > 0.5 * (shallow ? ch : cw)) return vec4(0.0);

	// the sky is thin toward the left
	float free = smoothstep(0.2 * W, 0.58 * W, px.x);
	if (free <= 0.0) return vec4(0.0);
	int j = int(floor((theta + OMEGA * t) * R / (shallow ? cw : ch)));
	float m = (1.0 - hash3(ivec2(int(k), j), 31)) / max(free, 0.05);
	if (m < 0.003) return vec4(1.0, 0.0, 0.0, 1.0);
	if (m < 0.016) return vec4(0.8, 0.0, 0.0, 1.0);
	if (m < 0.04) return vec4(0.6, 0.0, 0.0, 1.0);
	if (m < 0.9) return vec4(hash3(ivec2(int(k), j), 32) < 0.97 ? 0.2 : 0.4, 0.0, 0.0, 1.0);
	return vec4(0.0);
}`;
