import { describe, expect, test } from "bun:test";
import {
	gaugeActive,
	gaugeBand,
	gaugeFraction,
	gaugeStatus,
	labelAnchor,
	polygonPath,
	radarMax,
	radarPolygon,
	radarRing,
	radarValues,
	sliceAngles,
	stepSlice,
	TAU,
} from "./radial-math";

describe("sliceAngles", () => {
	test("a full circle with gaps: slices + gaps add up to the turn, seam at 12 o'clock", () => {
		const gap = 0.1;
		const slices = sliceAngles([1, 1, 2], { gap });
		expect(slices[0]?.from).toBeCloseTo(gap / 2);
		const last = slices[2];
		expect((last?.to ?? 0) + gap / 2).toBeCloseTo(TAU);
		// Proportional: the third slice is twice the first.
		const sweep = (i: number) => (slices[i]?.to ?? 0) - (slices[i]?.from ?? 0);
		expect(sweep(2)).toBeCloseTo(sweep(0) * 2);
		// The gap between neighbours is the requested one.
		expect((slices[1]?.from ?? 0) - (slices[0]?.to ?? 0)).toBeCloseTo(gap);
		expect(slices.map((s) => s.fraction)).toEqual([0.25, 0.25, 0.5]);
	});

	test("a lone slice is a full ring without a gap", () => {
		const [only] = sliceAngles([5, 0], { gap: 0.2 });
		expect(only?.from).toBe(0);
		expect(only?.to).toBeCloseTo(TAU);
		const slices = sliceAngles([5, 0], { gap: 0.2 });
		expect(slices[1]?.from).toBe(slices[1]?.to);
	});

	test("zero, negative and NaN values take no angle and no gap", () => {
		const slices = sliceAngles([1, 0, -3, Number.NaN, 1], { gap: 0.1 });
		expect(slices.map((s) => s.value)).toEqual([1, 0, 0, 0, 1]);
		expect((slices[4]?.from ?? 0) - (slices[0]?.to ?? 0)).toBeCloseTo(0.1);
	});

	test("an all-zero input collapses to the start", () => {
		const slices = sliceAngles([0, 0], { start: 1 });
		expect(slices.every((s) => s.from === 1 && s.to === 1)).toBe(true);
	});

	test("a huge gap is capped so slices keep at least half the sweep", () => {
		const slices = sliceAngles([1, 1, 1, 1], { gap: 5 });
		const drawn = slices.reduce((sum, s) => sum + (s.to - s.from), 0);
		expect(drawn).toBeCloseTo(TAU / 2);
	});

	test("a partial sweep has no gap at its ends", () => {
		const slices = sliceAngles([1, 1], { gap: 0.2, start: 1, sweep: Math.PI });
		expect(slices[0]?.from).toBe(1);
		expect(slices[1]?.to).toBeCloseTo(1 + Math.PI);
	});
});

describe("stepSlice", () => {
	const slices = sliceAngles([3, 0, 2, 1]);
	test("skips empty slices and wraps", () => {
		expect(stepSlice(slices, 0, 1)).toBe(2);
		expect(stepSlice(slices, 3, 1)).toBe(0);
		expect(stepSlice(slices, 0, -1)).toBe(3);
	});
	test("starts at the first / last from nothing", () => {
		expect(stepSlice(slices, null, 1)).toBe(0);
		expect(stepSlice(slices, null, -1)).toBe(3);
	});
	test("nothing live keeps the selection", () => {
		expect(stepSlice(sliceAngles([0, 0]), null, 1)).toBeNull();
		expect(stepSlice([], null, 1)).toBeNull();
	});
});

describe("radar", () => {
	test("axis 0 points straight up and the ring is regular", () => {
		const ring = radarRing(4, 100, 100, 50);
		expect(ring[0]?.x).toBeCloseTo(100);
		expect(ring[0]?.y).toBeCloseTo(50);
		expect(ring[1]?.x).toBeCloseTo(150);
		expect(ring[1]?.y).toBeCloseTo(100);
		expect(ring[2]?.y).toBeCloseTo(150);
	});

	test("polygon scales with value/max and clamps to the ring", () => {
		const pts = radarPolygon([50, 200, 0, Number.NaN], 100, 0, 0, 10);
		expect(pts[0]?.y).toBeCloseTo(-5);
		// over max sits on the outer ring
		expect(pts[1]?.x).toBeCloseTo(10);
		expect(pts[2]?.y).toBeCloseTo(0);
		expect(pts[3]?.x).toBeCloseTo(0);
	});

	test("polygonPath closes the shape; empty input is empty", () => {
		expect(polygonPath([])).toBe("");
		expect(
			polygonPath([
				{ x: 0, y: 0 },
				{ x: 10.04, y: 0 },
				{ x: 5, y: 5 },
			]),
		).toBe("M0 0L10 0L5 5Z");
	});

	test("radarValues reads arrays and records in axis order", () => {
		const axes = ["a", "b", "c"];
		expect(radarValues([1, 2], axes)).toEqual([1, 2, 0]);
		expect(radarValues({ c: 3, a: 1, z: 9 }, axes)).toEqual([1, 0, 3]);
		expect(radarValues([1, Number.NaN, 3, 4], axes)).toEqual([1, 0, 3]);
	});

	test("radarMax honours max, else rounds the data up", () => {
		expect(radarMax([[1, 2]], 10)).toBe(10);
		expect(radarMax([[3, 72], [10]])).toBe(80);
		expect(radarMax([[0, 0]])).toBe(1);
	});

	test("labelAnchor hugs the side a label sits on", () => {
		expect(labelAnchor(0)).toBe("middle");
		expect(labelAnchor(Math.PI / 2)).toBe("start");
		expect(labelAnchor(-Math.PI / 2)).toBe("end");
		expect(labelAnchor(Math.PI)).toBe("middle");
	});
});

describe("gauge", () => {
	test("fraction clamps and survives bad input", () => {
		expect(gaugeFraction(50, 0, 100)).toBe(0.5);
		expect(gaugeFraction(150, 0, 100)).toBe(1);
		expect(gaugeFraction(-5, 0, 100)).toBe(0);
		expect(gaugeFraction(5, 10, 10)).toBe(0);
		expect(gaugeFraction(Number.NaN, 0, 100)).toBe(0);
		expect(gaugeFraction(0, -50, 50)).toBe(0.5);
	});

	test("the active segment is the one that holds the value", () => {
		expect(gaugeActive(0, 24)).toBe(-1);
		expect(gaugeActive(0.001, 24)).toBe(0);
		expect(gaugeActive(0.5, 24)).toBe(11);
		expect(gaugeActive(1, 24)).toBe(23);
	});

	test("bands: densest behind the value, thinnest at the start", () => {
		expect(gaugeBand(10, 11, 4)).toBe(0);
		expect(gaugeBand(0, 11, 4)).toBe(3);
		expect(gaugeBand(0, 0, 4)).toBe(0);
		expect(gaugeBand(3, 11, 1)).toBe(0);
		const bands = Array.from({ length: 11 }, (_, i) => gaugeBand(i, 11, 4));
		expect([...bands].sort((a, b) => b - a)).toEqual(bands);
	});

	test("status: higher is worse by default, lower is worse when badAt < warnAt", () => {
		expect(gaugeStatus(50, 70, 90)).toBe("ok");
		expect(gaugeStatus(70, 70, 90)).toBe("warn");
		expect(gaugeStatus(95, 70, 90)).toBe("bad");
		expect(gaugeStatus(95, 70)).toBe("warn");
		expect(gaugeStatus(95, undefined, 90)).toBe("bad");
		expect(gaugeStatus(95)).toBe("ok");
		expect(gaugeStatus(80, 50, 20)).toBe("ok");
		expect(gaugeStatus(40, 50, 20)).toBe("warn");
		expect(gaugeStatus(10, 50, 20)).toBe("bad");
	});
});
