import { GAP_DRIFT, GAP_EDGE } from "./scenes";

/**
 * Lost signal: static glyph noise with a gap in it where the page should be.
 * Every cell holds a symbol that re-rolls at its own 1-4 Hz, so the field
 * never strobes as a whole; tones lean dark with a few bright cells. The gap
 * is a soft-cornered box around `uFocus` (centre x, y, half-extent x, y) that
 * drifts by up to GAP_DRIFT (9%) of the short side over 40-70 s and breathes 3%. Noise thins
 * to nothing toward it and its rim holds a few accent cells. A slow bar of
 * extra brightness rolls up the screen every 18 s. Rare accent glitches
 * elsewhere.
 */
export const LOST_SIGNAL = `
vec4 field(ivec2 cell, vec2 px, float t) {
	float drift = ${GAP_DRIFT.toFixed(3)} * min(uRes.x, uRes.y);
	vec2 c = uFocus.xy + vec2(drift * sin(t * 0.13), 0.55 * drift * sin(t * 0.09 + 1.3));
	vec2 r = uFocus.zw * (1.0 + 0.03 * sin(t * 0.07 + 0.6));
	vec2 q = abs((px - c) / r);
	float g = pow(pow(q.x, 4.0) + pow(q.y, 4.0), 0.25);

	float rag = 0.05 * hash3(cell, 1);
	float presence = smoothstep(1.0, 1.0 + ${GAP_EDGE.toFixed(2)}, g - rag);
	if (presence <= 0.0) return vec4(0.0);

	float bar = exp(-pow((px.y / uRes.y - (1.0 - fract(t / 18.0))) * 7.0, 2.0));
	float dens = (0.58 + 0.24 * bar) * presence;
	if (hash3(cell, 2) > dens) return vec4(0.0);

	float rate = 1.0 + 3.0 * hash3(cell, 3);
	int e = int(floor(t * rate + 17.0 * hash3(cell, 4)));
	if (hash3(cell, 5 + e) > 0.9) return vec4(0.0);

	float lum = (0.1 + 0.62 * pow(hash3(cell, 6 + e), 2.2)) * mix(0.5, 1.0, presence);
	float variant = float(int(hash3(cell, 7 + e) * 3.0));
	float rim = 1.0 - smoothstep(1.0, 1.12, g);
	float hue = (hash3(cell, 8) > 0.992 || (rim > 0.0 && hash3(cell, 9 + e) < 0.5 * rim)) ? 1.0 : 0.0;
	return vec4(lum, hue, variant, 1.0);
}`;
