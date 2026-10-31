/**
 * The Eclipse scene: a banded nebula orb (analytic ray/sphere), a ring with a
 * gap and the orb's shadow on it, four orbiting stars (the logo's sparks).
 * Accent hue only on the lit limb and the ring's inner lip. The shared passes
 * live in shader.ts.
 */

export const ECLIPSE = `
const float SRHO[4] = float[4](1.5, 1.9, 1.22, 2.4);
const float SW[4]   = float[4](0.21, -0.14, 0.31, 0.09);
const float SP[4]   = float[4](0.4, 2.2, 4.1, 5.3);

vec2 eclipse(ivec2 cell, vec2 px, float t) {
	vec3 L = normalize(vec3(0.66, -0.42, -0.62));
	float R = uOrb.z;
	vec2 c0 = uOrb.xy;
	vec2 uv = (px - c0) / R; float u = uv.x, v = uv.y;
	float a = 0.27 + 0.04 * sin(t * 0.07), b = -0.36 + 0.05 * sin(t * 0.045 + 1.0);
	vec3 Nr = vec3(-cos(a) * sin(b), cos(a) * cos(b), sin(a));
	float e1l = length(Nr.xy);
	vec3 e1 = vec3(Nr.y / e1l, -Nr.x / e1l, 0.0);
	vec3 e2 = cross(Nr, e1);
	float spin = t * 0.11, cs = cos(spin), sn = sin(spin);
	float tl = 0.42, ct = cos(tl), st = sin(tl);
	float r2 = u * u + v * v;
	float l = 0.0, h = 0.0;

	// faint dust
	float hh = hash2(cell);
	if (hh > 0.993) l = 0.08 + 0.08 * (0.5 + 0.5 * sin(t * 0.7 + hh * 977.0));

	// ring
	float ringL = 0.0, ringZ = 1e9, ringH = 0.0;
	float zp = -(Nr.x * u + Nr.y * v) / Nr.z;
	float rho = sqrt(r2 + zp * zp);
	if (rho > 1.34 && rho < 2.2) {
		float pxp = u * e1.x + v * e1.y, pyp = u * e2.x + v * e2.y + zp * e2.z;
		float phi = atan(pyp, pxp) - t * 0.18 / pow(rho, 1.5);
		float dens = 0.55 + 0.45 * sin(rho * 31.0 + 1.7 * sin(rho * 7.0));
		dens *= 0.7 + 0.3 * sin(phi * 9.0 + rho * 5.0);
		if (rho > 1.7 && rho < 1.8) dens *= 0.1;
		if (rho > 2.06) dens *= (2.2 - rho) / 0.14;
		vec3 P = vec3(u, v, zp);
		float pl = dot(P, L); vec3 dd = P - pl * L;
		float shadow = (pl > 0.0 && dot(dd, dd) < 1.0) ? 0.1 : 1.0;
		ringL = dens * 1.35 * shadow * (0.55 + 0.45 * abs(dot(Nr, L)) * 1.6);
		ringZ = zp;
		if (rho < 1.45 && shadow > 0.5) ringH = 1.0;
	}

	// sphere
	float sphL = 0.0, sphZ = 1e9, sphH = 0.0;
	if (r2 < 1.0) {
		float z = -sqrt(1.0 - r2);
		float x1 = u * cs + z * sn, z1 = -u * sn + z * cs;
		float y2 = v * ct - z1 * st, z2 = v * st + z1 * ct;
		float s = y2 * 7.0 + 1.7 * sin(x1 * 2.6 + z2 * 1.9 + t * 0.25) + 0.9 * sin(x1 * 5.1 - z2 * 4.3 - t * 0.15 + y2 * 3.0);
		float bands = 0.5 + 0.5 * sin(s);
		float diff = max(0.0, dot(vec3(u, v, z), L));
		float rim = pow(1.0 - abs(z), 3.0) * (0.2 + 0.8 * max(0.0, diff + 0.25));
		sphL = 0.06 + pow(diff, 0.95) * (0.34 + 0.66 * bands) + rim * 0.7;
		sphZ = z;
		if (rim > 0.28) sphH = 1.0;
	} else if (r2 < 1.26) {
		float r = sqrt(r2), f = 1.0 - (r - 1.0) / 0.12;
		float lit = max(0.0, dot(vec2(u, v) / r, L.xy));
		float hl = f * f * (0.15 + 0.5 * lit);
		if (hl > l) { l = hl; h = 1.0; }
	}
	if (sphL > 0.0 && ringL > 0.0) {
		if (ringZ < sphZ) { if (ringL > l) { l = ringL; h = ringH; } } else { l = sphL; h = sphH; }
	} else if (sphL > 0.0) { l = sphL; h = sphH; }
	else if (ringL > l) { l = ringL; h = ringH; }

	// four stars orbiting in the ring plane
	for (int k = 0; k < 4; k++) {
		float ph = SP[k] + t * SW[k];
		vec3 S = SRHO[k] * (cos(ph) * e1 + sin(ph) * e2);
		if (S.x * S.x + S.y * S.y < 1.0 && S.z > 0.0) continue;
		ivec2 d = cell - ivec2(floor((c0 + S.xy * R) / uCell));
		if (abs(d.x) <= 2 && abs(d.y) <= 1) {
			float dist = length(vec2(float(d.x) * 0.55, float(d.y)));
			float val = dist < 0.5 ? 1.0 : max(0.0, 0.5 - dist * 0.25);
			if (val > l) { l = val; h = 2.0; }
		}
	}
	return vec2(l, h);
}

vec4 field(ivec2 cell, vec2 px, float t) {
	vec2 r = eclipse(cell, px, t);
	return vec4(r, 0.0, 1.0);
}`;
