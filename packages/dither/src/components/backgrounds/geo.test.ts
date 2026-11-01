import { describe, expect, test } from "bun:test";
import {
	angularDistance,
	boundsAspect,
	latLonToUnit,
	latLonToUV,
	sampleGlobeArc,
	sampleMapArc,
	toView,
	unitToLatLon,
	wrapLon,
} from "./geo";

const close = (a: number, b: number, eps = 1e-6) =>
	expect(Math.abs(a - b)).toBeLessThan(eps);

describe("wrapLon", () => {
	test("wraps into [-180, 180)", () => {
		expect(wrapLon(190)).toBe(-170);
		expect(wrapLon(-190)).toBe(170);
		expect(wrapLon(0)).toBe(0);
		expect(wrapLon(540)).toBe(-180);
	});
});

describe("latLonToUV", () => {
	test("maps the default bounds corners", () => {
		const [u0, v0] = latLonToUV(-56, -180);
		close(u0, 0);
		close(v0, 0);
		const [u1, v1] = latLonToUV(78, 0);
		close(u1, 0.5);
		close(v1, 1);
	});

	test("places the equator and prime meridian", () => {
		const [u, v] = latLonToUV(0, 0);
		close(u, 0.5);
		close(v, 56 / 134);
	});

	test("v grows northwards and u eastwards", () => {
		const a = latLonToUV(10, 10);
		const b = latLonToUV(20, 20);
		expect(b[0]).toBeGreaterThan(a[0]);
		expect(b[1]).toBeGreaterThan(a[1]);
	});

	test("respects custom bounds", () => {
		const europe = { north: 60, south: 35, west: -10, east: 30 };
		const [u, v] = latLonToUV(47.5, 10, europe);
		close(u, 0.5);
		close(v, 0.5);
		expect(latLonToUV(0, 10, europe)[1]).toBeLessThan(0);
	});

	test("wraps longitudes beyond ±180", () => {
		const a = latLonToUV(10, 170);
		const b = latLonToUV(10, -190);
		close(a[0], b[0]);
	});

	test("maps straddling the antimeridian stay continuous", () => {
		const pacific = { north: 40, south: -40, west: 150, east: 210 };
		close(latLonToUV(0, 180, pacific)[0], 0.5);
		close(latLonToUV(0, -170, pacific)[0], 40 / 60);
	});

	test("boundsAspect matches degree span ratio", () => {
		close(boundsAspect(), 360 / 134);
	});
});

describe("unit vectors", () => {
	test("round-trip lat/lon", () => {
		const [lat, lon] = unitToLatLon(latLonToUnit(35.68, 139.69));
		close(lat, 35.68, 1e-9);
		close(lon, 139.69, 1e-9);
	});

	test("the longitude facing the viewer lands on +z", () => {
		const [x, y, z] = toView(latLonToUnit(0, 40), 40, 0);
		close(x, 0);
		close(y, 0);
		close(z, 1);
	});

	test("tilt leans the north pole towards the viewer", () => {
		const [, y, z] = toView([0, 1, 0], 0, 18);
		close(y, Math.cos((18 * Math.PI) / 180));
		close(z, Math.sin((18 * Math.PI) / 180));
	});

	test("view rotation preserves length", () => {
		const v = toView(latLonToUnit(-33, 151), 12, 18);
		close(Math.hypot(...v), 1);
	});

	test("east of the facing meridian is on the right", () => {
		expect(toView(latLonToUnit(0, 30), 0, 0)[0]).toBeGreaterThan(0);
	});
});

describe("sampleGlobeArc", () => {
	const london = [51.51, -0.13] as const;
	const tokyo = [35.68, 139.69] as const;

	test("returns the requested number of samples from 0 to 1", () => {
		const arc = sampleGlobeArc(london, tokyo, 24);
		expect(arc).toHaveLength(24);
		expect(arc[0]?.s).toBe(0);
		expect(arc[23]?.s).toBe(1);
	});

	test("ends sit on the surface at the endpoints", () => {
		const arc = sampleGlobeArc(london, tokyo, 24);
		const start = arc[0]?.point ?? [0, 0, 0];
		const end = arc[23]?.point ?? [0, 0, 0];
		const [lat0, lon0] = unitToLatLon(start);
		const [lat1, lon1] = unitToLatLon(end);
		close(lat0, london[0], 1e-6);
		close(lon0, london[1], 1e-6);
		close(lat1, tokyo[0], 1e-6);
		close(lon1, tokyo[1], 1e-6);
		close(Math.hypot(...start), 1);
		close(Math.hypot(...end), 1);
	});

	test("the middle rises above the surface", () => {
		const arc = sampleGlobeArc(london, tokyo, 25);
		const mid = arc[12]?.point ?? [0, 0, 0];
		expect(Math.hypot(...mid)).toBeGreaterThan(1.05);
	});

	test("follows the great circle: the midpoint is equidistant from both ends", () => {
		const arc = sampleGlobeArc(london, tokyo, 25);
		const mid = unitToLatLon(arc[12]?.point ?? [0, 0, 0]);
		close(angularDistance(london, mid), angularDistance(mid, tokyo), 1e-6);
	});

	test("longer arcs lift higher than short ones", () => {
		const short = sampleGlobeArc([0, 0], [0, 10], 11);
		const long = sampleGlobeArc([0, 0], [0, 120], 11);
		expect(Math.hypot(...(long[5]?.point ?? [0, 0, 0]))).toBeGreaterThan(
			Math.hypot(...(short[5]?.point ?? [0, 0, 0])),
		);
	});

	test("coincident endpoints do not produce NaN", () => {
		const arc = sampleGlobeArc(london, london, 5);
		for (const { point } of arc)
			for (const c of point) expect(Number.isFinite(c)).toBe(true);
	});
});

describe("sampleMapArc", () => {
	test("starts and ends at the endpoints", () => {
		const arc = sampleMapArc([0.2, 0.5], [0.8, 0.4], 16, 2.5);
		expect(arc).toHaveLength(16);
		expect(arc[0]).toEqual([0.2, 0.5]);
		const last = arc[15] ?? [0, 0];
		close(last[0], 0.8);
		close(last[1], 0.4);
	});

	test("bows above the chord", () => {
		const arc = sampleMapArc([0.2, 0.5], [0.8, 0.5], 17, 2);
		const mid = arc[8] ?? [0, 0];
		close(mid[0], 0.5);
		expect(mid[1]).toBeGreaterThan(0.5);
	});

	test("no lift gives a straight line", () => {
		const arc = sampleMapArc([0, 0], [1, 1], 5, 1, 0);
		close(arc[2]?.[0] ?? 0, 0.5);
		close(arc[2]?.[1] ?? 0, 0.5);
	});
});
