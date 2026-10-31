import { describe, expect, test } from "bun:test";
import {
	COAST_MAX,
	COAST_STOP,
	clampTiltOffset,
	type GlobeView,
	REST_VIEW,
	smoothSpeed,
	stepGlobe,
	TILT_RANGE,
	tiltOf,
} from "./globe-motion";

const idle = { spin: 6, held: false, coast: true, tilt: 18 };

describe("stepGlobe", () => {
	test("idle spin turns the longitude down at the spin rate", () => {
		const next = stepGlobe(REST_VIEW, 1, idle);
		expect(next.lon).toBeCloseTo(-6, 9);
		expect(next.tilt).toBe(0);
	});

	test("a negative spin turns the other way", () => {
		expect(stepGlobe(REST_VIEW, 1, { ...idle, spin: -6 }).lon).toBeCloseTo(
			6,
			9,
		);
	});

	test("held: nothing moves and the idle spin resets to zero", () => {
		const view: GlobeView = { ...REST_VIEW, vLon: 90, gain: 1 };
		const next = stepGlobe(view, 0.5, { ...idle, held: true });
		expect(next.lon).toBe(0);
		expect(next.gain).toBe(0);
		expect(next.vLon).toBe(90);
	});

	test("after a release the globe coasts, slowing down, then stops", () => {
		let view: GlobeView = { ...REST_VIEW, vLon: 120, gain: 0 };
		let last = Number.POSITIVE_INFINITY;
		for (let i = 0; i < 20; i++) {
			view = stepGlobe(view, 0.1, { ...idle, spin: 0 });
			expect(Math.abs(view.vLon)).toBeLessThanOrEqual(last);
			last = Math.abs(view.vLon);
		}
		expect(view.lon).toBeGreaterThan(20);
		for (let i = 0; i < 80; i++)
			view = stepGlobe(view, 0.1, { ...idle, spin: 0 });
		expect(view.vLon).toBe(0);
	});

	test("speeds under the stop threshold are dropped", () => {
		const view: GlobeView = { ...REST_VIEW, vLon: COAST_STOP * 0.9, gain: 1 };
		expect(stepGlobe(view, 0.1, { ...idle, spin: 0 }).vLon).toBe(0);
	});

	test("reduced motion: no coast", () => {
		const view: GlobeView = { ...REST_VIEW, vLon: 200, vTilt: 50, gain: 0 };
		const next = stepGlobe(view, 0.1, { ...idle, spin: 0, coast: false });
		expect(next.vLon).toBe(0);
		expect(next.vTilt).toBe(0);
		expect(next.lon).toBeCloseTo(0, 9);
	});

	test("the idle spin eases back in after a release", () => {
		let view: GlobeView = { ...REST_VIEW, gain: 0 };
		const gains: number[] = [];
		for (let i = 0; i < 40; i++) {
			view = stepGlobe(view, 0.1, idle);
			gains.push(view.gain);
		}
		for (let i = 1; i < gains.length; i++) {
			expect(gains[i] as number).toBeGreaterThan(gains[i - 1] as number);
		}
		expect(gains[0] as number).toBeLessThan(0.2);
		expect(gains.at(-1) as number).toBeGreaterThan(0.98);
	});

	test("a tilt coast stops at the end of the range", () => {
		let view: GlobeView = { ...REST_VIEW, vTilt: COAST_MAX, gain: 0 };
		for (let i = 0; i < 100; i++)
			view = stepGlobe(view, 0.1, { ...idle, spin: 0 });
		expect(tiltOf(18, view.tilt)).toBe(TILT_RANGE.max);
		expect(view.vTilt).toBe(0);
	});

	test("a zero step changes nothing", () => {
		expect(stepGlobe(REST_VIEW, 0, idle)).toBe(REST_VIEW);
	});
});

describe("tilt helpers", () => {
	test("tiltOf and clampTiltOffset stay inside the range", () => {
		expect(tiltOf(18, 500)).toBe(TILT_RANGE.max);
		expect(tiltOf(18, -500)).toBe(TILT_RANGE.min);
		expect(tiltOf(18, 10)).toBe(28);
		expect(clampTiltOffset(18, 500)).toBe(TILT_RANGE.max - 18);
		expect(clampTiltOffset(18, -500)).toBe(TILT_RANGE.min - 18);
	});
});

describe("smoothSpeed", () => {
	test("blends the new sample with the history", () => {
		expect(smoothSpeed(0, 10, 0.1)).toBeCloseTo(60, 9);
		expect(smoothSpeed(100, 10, 0.1)).toBeCloseTo(100, 9);
	});

	test("clamps absurd speeds and ignores a zero interval", () => {
		expect(smoothSpeed(0, 1e6, 0.01)).toBeCloseTo(COAST_MAX * 0.6, 9);
		expect(smoothSpeed(42, 5, 0)).toBe(42);
	});
});
