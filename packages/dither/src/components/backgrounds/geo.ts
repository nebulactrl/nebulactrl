/** Pure geometry for the globe and dot map. Angles in degrees unless named `rad`. */

/** `[latitude, longitude]` in degrees. */
export type LatLon = readonly [lat: number, lon: number];
export type Vec3 = [number, number, number];

export interface MapBounds {
	north: number;
	south: number;
	west: number;
	east: number;
}

/** Everything the dot map shows by default: 78°N to 56°S, all longitudes. */
export const DEFAULT_BOUNDS: MapBounds = {
	north: 78,
	south: -56,
	west: -180,
	east: 180,
};

export const RAD = Math.PI / 180;

/** Longitude wrapped into [-180, 180). */
export function wrapLon(lon: number): number {
	return ((((lon + 180) % 360) + 360) % 360) - 180;
}

/**
 * Position of `lat`, `lon` inside `bounds` as `[u, v]` in 0..1, `u` left to
 * right and `v` bottom to top (the same orientation as the shader's `uv`).
 * Points outside the bounds fall outside 0..1.
 */
export function latLonToUV(
	lat: number,
	lon: number,
	bounds: MapBounds = DEFAULT_BOUNDS,
): [number, number] {
	const span = bounds.east - bounds.west;
	// Wrap relative to the west edge so maps that straddle the antimeridian work.
	let dx = (((lon - bounds.west) % 360) + 360) % 360;
	// Outside the span, land on whichever side is nearer.
	if (dx > span && 360 - dx < dx - span) dx -= 360;
	const u = dx / span;
	const v = (lat - bounds.south) / (bounds.north - bounds.south);
	return [u, v];
}

/** Width / height of a map showing `bounds` with square degree cells. */
export function boundsAspect(bounds: MapBounds = DEFAULT_BOUNDS): number {
	return (bounds.east - bounds.west) / (bounds.north - bounds.south);
}

/** Unit vector for `lat`, `lon`: x east, y north, z towards lon 0 on the equator. */
export function latLonToUnit(lat: number, lon: number): Vec3 {
	const la = lat * RAD;
	const lo = lon * RAD;
	return [
		Math.cos(la) * Math.sin(lo),
		Math.sin(la),
		Math.cos(la) * Math.cos(lo),
	];
}

/** Inverse of {@link latLonToUnit}. */
export function unitToLatLon([x, y, z]: Vec3): [number, number] {
	const len = Math.hypot(x, y, z) || 1;
	return [Math.asin(y / len) / RAD, Math.atan2(x, z) / RAD];
}

/**
 * Rotates a world vector into view space for a globe with `lon` facing the
 * viewer and the north pole leaning `tilt` degrees towards them. The viewer
 * sits on +z, x is right and y is up. Mirrors the globe shader.
 */
export function toView([x, y, z]: Vec3, lon: number, tilt: number): Vec3 {
	const a = lon * RAD;
	const x1 = x * Math.cos(a) - z * Math.sin(a);
	const z1 = x * Math.sin(a) + z * Math.cos(a);
	const b = tilt * RAD;
	return [
		x1,
		y * Math.cos(b) - z1 * Math.sin(b),
		y * Math.sin(b) + z1 * Math.cos(b),
	];
}

/** Central angle in radians between two surface points. */
export function angularDistance(a: LatLon, b: LatLon): number {
	const [ax, ay, az] = latLonToUnit(a[0], a[1]);
	const [bx, by, bz] = latLonToUnit(b[0], b[1]);
	const dot = ax * bx + ay * by + az * bz;
	return Math.acos(Math.max(-1, Math.min(1, dot)));
}

/** Samples along a great circle, lifted off the surface into an arc. */
export interface ArcSample3 {
	/** Position relative to a unit sphere, length `1 + lift`. */
	point: Vec3;
	/** Progress along the arc, 0..1. */
	s: number;
}

/**
 * `count` points (>= 2) along the great circle from `from` to `to`, raised
 * above the surface by `liftScale` times the arc's span in turns of π, with a
 * sine profile so both ends touch the ground.
 */
export function sampleGlobeArc(
	from: LatLon,
	to: LatLon,
	count: number,
	liftScale = 0.35,
): ArcSample3[] {
	const n = Math.max(2, Math.floor(count));
	const a = latLonToUnit(from[0], from[1]);
	const b = latLonToUnit(to[0], to[1]);
	const omega = angularDistance(from, to);
	const sinO = Math.sin(omega);
	const lift = liftScale * (omega / Math.PI);
	const out: ArcSample3[] = [];
	for (let i = 0; i < n; i++) {
		const s = i / (n - 1);
		// Slerp; fall back to lerp for coincident or antipodal-adjacent endpoints.
		const wa = sinO < 1e-6 ? 1 - s : Math.sin((1 - s) * omega) / sinO;
		const wb = sinO < 1e-6 ? s : Math.sin(s * omega) / sinO;
		const v: Vec3 = [
			wa * a[0] + wb * b[0],
			wa * a[1] + wb * b[1],
			wa * a[2] + wb * b[2],
		];
		const len = Math.hypot(...v) || 1;
		const radius = 1 + lift * Math.sin(Math.PI * s);
		out.push({
			point: [
				(v[0] / len) * radius,
				(v[1] / len) * radius,
				(v[2] / len) * radius,
			],
			s,
		});
	}
	return out;
}

/**
 * `count` points (>= 2) along a quadratic curve between two map positions
 * (`uv`, `v` up), bowed upward by `lift` times the chord length. `aspect` is
 * the map's width over height so the bow is measured in square units.
 */
export function sampleMapArc(
	from: readonly [number, number],
	to: readonly [number, number],
	count: number,
	aspect: number,
	lift = 0.3,
): [number, number][] {
	const n = Math.max(2, Math.floor(count));
	const chord = Math.hypot((to[0] - from[0]) * aspect, to[1] - from[1]);
	const cx = (from[0] + to[0]) / 2;
	const cy = (from[1] + to[1]) / 2 + lift * chord;
	const out: [number, number][] = [];
	for (let i = 0; i < n; i++) {
		const s = i / (n - 1);
		const m = 1 - s;
		out.push([
			m * m * from[0] + 2 * m * s * cx + s * s * to[0],
			m * m * from[1] + 2 * m * s * cy + s * s * to[1],
		]);
	}
	return out;
}
