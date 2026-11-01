/**
 * Dial-out: calm signals rising from the bottom edge toward one clearly marked
 * point, like servers calling home. Five paths leave the bottom edge across
 * the half of the canvas the point is on (the right 55% when it is on the
 * right), rise straight up and bend in only near the top. Each sends one
 * pulse per cycle, an accent head with a tail of slate glyphs behind it. The
 * cycles are staggered, so four or five pulses are in flight at any moment;
 * their speeds differ by a few percent, so they never settle into step.
 * Pulses dissolve into the point's halo.
 *
 * The point `uFocus.xy` is steady: an accent `*` in a soft round halo of `.`
 * and `'` that thins with distance. It does not breathe, spread or react.
 * Constant speed, independent of anything on the page: atmosphere, never
 * progress.
 */
export const DIAL_OUT = `
const int N = 5;
/* Tail length, as a fraction of the climb. */
const float TAIL = 0.5;
/* Climb speed in rows per second, before the per-path factor. */
const float RISE = 3.2;
/* Halo radius in rows (scenes.ts keeps the point this far plus the quiet clearance from text). */
const float HALO = 6.5;

float rnd(int i, int k) { return hash3(ivec2(i, 11), k); }

vec4 field(ivec2 cell, vec2 px, float t) {
	vec2 P = uFocus.xy;
	float W = uRes.x;
	float H = uRes.y;
	float rise = H - P.y;

	// the point: a steady accent mark in a soft round halo
	ivec2 pc = ivec2(floor(P / uCell));
	if (cell == pc) return vec4(1.0, 1.0, 0.0, 1.0);
	float dist = length((px - (vec2(pc) + 0.5) * uCell) / uCell.y);
	float l = 0.0, hue = 0.0;
	if (dist < HALO) {
		float f = 1.0 - dist / HALO;
		if (hash3(cell, 21) < f * f * 0.9) l = hash3(cell, 22) < 0.5 ? 0.12 : 0.26;
	}

	// signals
	float eta = (H - px.y) / rise;
	if (eta > -0.02 && eta < 1.0) {
		bool right = P.x > 0.5 * W;
		for (int i = 0; i < N; i++) {
			float u = (float(i) + 0.5 + 0.5 * (rnd(i, 1) - 0.5)) / float(N);
			float ox = right ? W * (0.45 + 0.55 * u) : W * 0.55 * (1.0 - u);
			float k = 3.0 + 1.0 * rnd(i, 2);
			float dxo = ox - P.x;
			float up = max(eta, 0.0);
			float xc = P.x + dxo * (1.0 - pow(up, k));
			float slope = abs(dxo) * k * pow(up, k - 1.0) / rise;
			float root = sqrt(1.0 + slope * slope);
			float support = 0.5 * (uCell.x + slope * uCell.y) / root;
			float rel = abs(px.x - xc) / root / support;
			if (rel > 1.2) continue;

			float v = uCell.y * RISE * (0.93 + 0.14 * rnd(i, 3));
			float cycle = rise / v * (1.0 + TAIL) + 0.8;
			float tau = mod(t + cycle * (float(i) + 0.4 * rnd(i, 5)) / float(N), cycle);
			float delta = tau * v / rise - eta;
			if (delta < 0.0 || delta >= TAIL) continue;

			float edge = 1.0 - smoothstep(0.55, 1.15, rel);
			float fade = 1.0 - smoothstep(0.72, 0.95, eta);
			float val = pow(1.0 - delta / TAIL, 1.5) * edge * fade;
			if (delta < 0.04) val = max(val, 0.95 * edge * fade);
			if (val > l) { l = val; hue = delta < 0.04 ? 1.0 : 0.0; }
		}
	}
	return vec4(l, hue, 0.0, 1.0);
}`;
