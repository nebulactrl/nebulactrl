/**
 * Fields shared by the Engine gallery. Each GLSL field has a JavaScript twin
 * that returns the same tone, so the WebGL, canvas and SVG paths can be shown
 * side by side drawing the same picture.
 */

/** A glow from the lower-left corner. Aspect is fixed so the GLSL and JS match. */
export const GLOW_GLSL = /* glsl */ `
float field(vec2 uv, vec2 res, float t) {
  return clamp(1.1 - length(vec2(uv.x * 1.6, uv.y)) * 0.9, 0.0, 1.0);
}
`;

export const glowTone = (u: number, v: number): number =>
	Math.min(1, Math.max(0, 1.1 - Math.hypot(u * 1.6, v) * 0.9));

/** A left-to-right ramp. */
export const RAMP_GLSL = /* glsl */ `
float field(vec2 uv, vec2 res, float t) { return uv.x; }
`;

/** A lit sphere on a soft vertical gradient: the classic still to dither. */
export const sphereTone = (u: number, v: number): number => {
	const dx = (u - 0.55) / 0.3;
	const dy = (v - 0.5) / 0.3;
	const inside = dx * dx + dy * dy;
	if (inside < 1) {
		const light = Math.max(0, 1 - Math.hypot(dx + 0.45, dy - 0.45) / 1.6);
		return 0.1 + 0.9 * light;
	}
	return 0.15 + 0.35 * (1 - v);
};

/** Slow drifting noise for the motion section. */
export const DRIFT_GLSL = /* glsl */ `
float field(vec2 uv, vec2 res, float t) {
  float aspect = res.x / res.y;
  vec2 p = vec2(uv.x * aspect, uv.y) * 1.6;
  float n = fbm(p + vec2(t * 0.06, t * 0.02));
  float m = fbm(p * 1.7 - vec2(t * 0.03, 0.0) + n);
  return smoothstep(0.25, 0.85, m) * (0.35 + 0.65 * uv.y);
}
`;

/**
 * The write-your-own example: ripples with uniforms for the knobs. Tone is
 * rings (cos of distance) fading out (exp), bent by noise.
 */
export const RIPPLES_GLSL = /* glsl */ `
float field(vec2 uv, vec2 res, float t) {
  float aspect = res.x / res.y;
  vec2 p = vec2(uv.x * aspect, uv.y);
  vec2 drop = vec2(demoDrop.x * aspect, 1.0 - demoDrop.y);
  float wobble = fbm(p * 2.2 + vec2(t * 0.03, 0.0)) - 0.5;
  float d = length(p - drop) + wobble * 0.08;
  float rings = 0.5 + 0.5 * cos(d * demoRings - t * 0.9);
  float fade = exp(-d * 2.6);
  return demoStrength * rings * fade;
}
`;

/** A rainbow along a wavy line; the field says where, `colorField` says which hue. */
export const SPECTRUM_FIELD_GLSL = /* glsl */ `
float field(vec2 uv, vec2 res, float t) {
  float y = 0.5 + 0.18 * sin(uv.x * 6.0 + t * 0.5);
  float d = abs(uv.y - y) * res.y / 1.4;
  return exp(-d * d) + 0.35 * exp(-d * 0.6);
}
`;

export const SPECTRUM_COLOR_GLSL = /* glsl */ `
vec3 colorField(vec2 uv, vec2 res, float t) {
  return mix(spectrum(uv.x * 0.75 - t * 0.03), paletteColor(0.0).rgb, 0.2);
}
`;
