import {
	type LatLon,
	latLonToUnit,
	latLonToUV,
	type MapBounds,
	sampleGlobeArc,
	sampleMapArc,
} from "./geo";
import { glslFloat, glslVec2, glslVec3 } from "./glsl";

/** Markers and arcs are baked into the shader as constants, so the counts are capped. */
export const MAX_MARKERS = 24;
export const MAX_ARCS = 8;
/** Samples per arc. Segments are `ARC_SAMPLES - 1`. */
export const ARC_SAMPLES = 25;

/**
 * Core scale, ring and halo reach, and brightness for a marker's `weight`
 * (0..1). No weight is the full look, `[1, 1, 1]`. A weight
 * of 0 is a single, dimmer dot; 1 is a core over two dots wide with the full halo.
 */
export function markerWeight(
	weight: number | undefined,
): [core: number, reach: number, lit: number] {
	if (weight === undefined || Number.isNaN(weight)) return [1, 1, 1];
	const w = Math.min(1, Math.max(0, weight));
	// Brightness never drops under 0.6, so the core still lifts a land dot (tone 0.5).
	return [1 + 1.2 * w, 0.45 + 0.55 * w, 0.6 + 0.4 * w];
}

export interface MarkerInput {
	at: LatLon;
	/** 0..1 size and brightness of a dot-map marker. See {@link markerWeight}. */
	weight?: number | undefined;
	/** Ramp position (0..1) of this marker's ink, for scenes with per-item colours. */
	ink?: number | undefined;
}

export interface ArcInput {
	from: LatLon;
	to: LatLon;
	/** Ramp position (0..1) of this arc's ink, for scenes with per-item colours. */
	ink?: number | undefined;
}

function constArray(type: string, name: string, items: string[]): string {
	return `const ${type} ${name}[${items.length}] = ${type}[${items.length}](${items.join(", ")});`;
}

// -- globe -------------------------------------------------------------------

/** Content key for markers and arcs, so callers can memoise generated GLSL without stable arrays. */
export function sceneKey(
	markers: readonly MarkerInput[],
	arcs: readonly ArcInput[],
): string {
	const m = markers
		.map((x) => `${x.at.join(",")}@${x.ink ?? ""}w${x.weight ?? ""}`)
		.join(";");
	const a = arcs
		.map((x) => `${x.from.join(",")}>${x.to.join(",")}@${x.ink ?? ""}`)
		.join(";");
	return `${m}|${a}`;
}

/**
 * Rotation globals and shared helpers for the globe. Set gbCl.. before use.
 * gbMk and gbAk are the ink positions of the strongest marker and arc at the
 * last gbMarkers / gbArcs call; the field copies the one that drew the cell
 * into gbInk for the colorField.
 */
export const GLOBE_HELPERS = /* glsl */ `
float gbMk;
float gbAk;
float gbInk;
float gbCl;
float gbSl;
float gbCt;
float gbSt;
vec3 gbView(vec3 v) {
  float x1 = v.x * gbCl - v.z * gbSl;
  float z1 = v.x * gbSl + v.z * gbCl;
  return vec3(x1, v.y * gbCt - z1 * gbSt, v.y * gbSt + z1 * gbCt);
}
vec3 gbWorld(vec3 v) {
  float y1 = v.y * gbCt + v.z * gbSt;
  float z1 = -v.y * gbSt + v.z * gbCt;
  return vec3(v.x * gbCl + z1 * gbSl, y1, -v.x * gbSl + z1 * gbCl);
}
/* One marker: a core, an expanding ring and a faint halo. dc is in cells. */
float gbMarker(vec2 p, vec3 mv, float R, float t, float phase) {
  if (mv.z <= 0.0) return 0.0;
  float dc = length(p - mv.xy) * R;
  float ph = fract(t * 0.45 + phase);
  float core = 1.0 - smoothstep(1.2, 2.0, dc);
  float rr = 1.5 + ph * 9.0;
  float qr = (dc - rr) / 1.3;
  float ring = (1.0 - ph) * exp(-qr * qr) * 0.85;
  float halo = exp(-dc * 0.35) * 0.35;
  return max(core, max(ring, halo)) * smoothstep(0.0, 0.25, mv.z);
}
`;

/** `float gbMarkers(vec2 p, float R, float t)`: the strongest marker at p. */
export function globeMarkersGlsl(markers: readonly MarkerInput[]): string {
	const body = markers
		.slice(0, MAX_MARKERS)
		.map((m, i) => {
			const [x, y, z] = latLonToUnit(m.at[0], m.at[1]);
			const phase = glslFloat((i * 0.37) % 1);
			return `  v = gbMarker(p, gbView(${glslVec3(x, y, z)}), R, t, ${phase});
  if (v > mk) { mk = v; gbMk = ${glslFloat(m.ink ?? 0)}; }`;
		})
		.join("\n");
	return `float gbMarkers(vec2 p, float R, float t) {
  float mk = 0.0;
  float v;
${body}
  return mk;
}`;
}

/** `float gbArcs(vec2 p, float R, float t)`: lifted great-circle arcs with light running along them. */
export function globeArcsGlsl(arcs: readonly ArcInput[]): string {
	const list = arcs.slice(0, MAX_ARCS);
	const fns = list.map((arc, k) => {
		const pts = sampleGlobeArc(arc.from, arc.to, ARC_SAMPLES).map(({ point }) =>
			glslVec3(point[0], point[1], point[2]),
		);
		return `${constArray("vec3", `gbA${k}`, pts)}
float gbArc${k}(vec2 p, float R, float t) {
  float best = 0.0;
  vec3 prev = gbView(gbA${k}[0]);
  for (int i = 1; i < ${ARC_SAMPLES}; i++) {
    vec3 cur = gbView(gbA${k}[i]);
    vec2 m = 0.5 * (prev.xy + cur.xy);
    float mz = 0.5 * (prev.z + cur.z);
    if (mz > 0.0 || dot(m, m) > 1.02) {
      float d = bg2Seg(p, prev.xy, cur.xy) * R;
      float s = (float(i) - 0.5) / ${glslFloat(ARC_SAMPLES - 1)};
      float head = fract(t * 0.16 + ${glslFloat((k * 0.27) % 1)});
      float dl = fract(head - s);
      float light = pow(max(0.0, 1.0 - dl * 3.2), 2.0);
      best = max(best, (1.0 - smoothstep(0.5, 1.4, d)) * (0.55 + 0.45 * light));
    }
    prev = cur;
  }
  return best;
}`;
	});
	const calls = list
		.map(
			(arc, k) =>
				`  v = gbArc${k}(p, R, t);\n  if (v > a) { a = v; gbAk = ${glslFloat(arc.ink ?? 0)}; }`,
		)
		.join("\n");
	return `${fns.join("\n")}
float gbArcs(vec2 p, float R, float t) {
  float a = 0.0;
  float v;
${calls}
  return a;
}`;
}

// -- dot map -----------------------------------------------------------------

/**
 * Marker and arc helpers for the dot map. Cells are evaluated at dot centres.
 * dmMk and dmAk are the ink positions of the strongest marker and arc at the
 * last dmMarkers / dmArcs call; the field copies the one that drew the dot
 * into dmInk for the colorField.
 */
export const DOTMAP_HELPERS = /* glsl */ `
float dmMk;
float dmAk;
float dmInk;
/* Marker at m (uv). cellDeg is the dot pitch in degrees; halo and pulse in degrees.
   k.x scales the core, k.y the ring and halo reach, k.z the brightness; 1,1,1 is the full look. */
vec3 dmMarker(vec2 dotUv, vec2 m, float t, float phase, vec3 k) {
  vec2 dDeg = (dotUv - m) * vec2(dmSpan.x, dmSpan.y);
  float d = length(dDeg);
  float ph = fract(t * 0.4 + phase);
  float core = 1.0 - smoothstep(0.55 * dmPitchDeg * k.x, 0.9 * dmPitchDeg * k.x, d);
  float reach = dmHalo * k.y;
  float ringR = ph * reach;
  float qr = (d - ringR) / max(dmPitchDeg, 1e-3);
  float ring = (1.0 - ph) * exp(-qr * qr) * 0.9;
  float halo = dmHalo > 0.0 ? (1.0 - smoothstep(0.0, reach, d)) * (0.45 + 0.1 * sin(t * 1.7 + phase * 6.28)) : 0.0;
  return vec3(core, ring, halo) * k.z;
}
`;

/** `vec3 dmMarkers(vec2 dotUv, float t)`: x core, y ring, z halo, each the max over markers. */
export function dotMapMarkersGlsl(
	markers: readonly MarkerInput[],
	bounds: MapBounds,
): string {
	const body = markers
		.slice(0, MAX_MARKERS)
		.map((mk, i) => {
			const [u, v] = latLonToUV(mk.at[0], mk.at[1], bounds);
			const [core, reach, lit] = markerWeight(mk.weight);
			return `  m = dmMarker(dotUv, ${glslVec2(u, v)}, t, ${glslFloat((i * 0.37) % 1)}, ${glslVec3(core, reach, lit)});
  r = max(r, m);
  s = max(m.x, max(m.y, m.z));
  if (s > best) { best = s; dmMk = ${glslFloat(mk.ink ?? 0)}; }`;
		})
		.join("\n");
	return `vec3 dmMarkers(vec2 dotUv, float t) {
  vec3 r = vec3(0.0);
  vec3 m;
  float s;
  float best = 0.0;
${body}
  return r;
}`;
}

/**
 * `float dmArcs(vec2 dotUv, float t)`: dotted arcs. Evaluated at dot centres so
 * a dot is either on the curve or not: alternating dots along the curve are
 * lit, and a bright stretch runs along it.
 */
export function dotMapArcsGlsl(
	arcs: readonly ArcInput[],
	bounds: MapBounds,
	aspect: number,
	pitchV: number,
): string {
	const list = arcs.slice(0, MAX_ARCS);
	const fns = list.map((arc, k) => {
		const from = latLonToUV(arc.from[0], arc.from[1], bounds);
		const to = latLonToUV(arc.to[0], arc.to[1], bounds);
		const pts = sampleMapArc(from, to, ARC_SAMPLES, aspect);
		const items = pts.map(([u, v]) => glslVec2(u * aspect, v));
		let length = 0;
		for (let i = 1; i < pts.length; i++) {
			const [u0, v0] = pts[i - 1] as [number, number];
			const [u1, v1] = pts[i] as [number, number];
			length += Math.hypot((u1 - u0) * aspect, v1 - v0);
		}
		const dots = Math.max(2, Math.round(length / pitchV));
		const xs = pts.map(([u]) => u * aspect);
		const ys = pts.map(([, v]) => v);
		const minX = Math.min(...xs) - pitchV * 2;
		const maxX = Math.max(...xs) + pitchV * 2;
		const minY = Math.min(...ys) - pitchV * 2;
		const maxY = Math.max(...ys) + pitchV * 2;
		return `${constArray("vec2", `dmA${k}`, items)}
float dmArc${k}(vec2 p, float t) {
  if (p.x < ${glslFloat(minX)} || p.x > ${glslFloat(maxX)} || p.y < ${glslFloat(minY)} || p.y > ${glslFloat(maxY)}) return 0.0;
  float bestD = 1e9;
  float bestS = 0.0;
  for (int i = 1; i < ${ARC_SAMPLES}; i++) {
    vec2 a = dmA${k}[i - 1];
    vec2 b = dmA${k}[i];
    vec2 pa = p - a;
    vec2 ba = b - a;
    float h = clamp(dot(pa, ba) / max(dot(ba, ba), 1e-9), 0.0, 1.0);
    float d = length(pa - ba * h);
    if (d < bestD) {
      bestD = d;
      bestS = (float(i - 1) + h) / ${glslFloat(ARC_SAMPLES - 1)};
    }
  }
  float onLine = 1.0 - smoothstep(${glslFloat(pitchV * 0.55)}, ${glslFloat(pitchV * 0.95)}, bestD);
  float dash = step(0.5, fract(bestS * ${glslFloat(dots)} * 0.5));
  float head = fract(t * 0.14 + ${glslFloat((k * 0.27) % 1)});
  float light = pow(max(0.0, 1.0 - fract(head - bestS) * 3.0), 2.0);
  return onLine * dash * (0.6 + 0.4 * light);
}`;
	});
	const calls = list
		.map(
			(arc, k) =>
				`  v = dmArc${k}(vec2(dotUv.x * ${glslFloat(aspect)}, dotUv.y), t);\n  if (v > a) { a = v; dmAk = ${glslFloat(arc.ink ?? 0)}; }`,
		)
		.join("\n");
	return `${fns.join("\n")}
float dmArcs(vec2 dotUv, float t) {
  float a = 0.0;
  float v;
${calls}
  return a;
}`;
}
