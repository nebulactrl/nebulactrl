import { describe, expect, test } from "bun:test";
import {
	LATTICE_BLEED,
	LATTICE_MIN_PITCH,
	LATTICE_PITCH,
	latticeLayer,
} from "./floor";

describe("latticeLayer", () => {
	test("the resting viewport sits a whole number of pitches off the layer's corner", () => {
		const layer = latticeLayer({ x: 0, y: 0, zoom: 1 });
		expect(layer.pitch).toBe(24);
		// bleed 48 is two whole pitches: the grid already lines up with 0.
		expect(layer).toEqual({ pitch: 24, x: 0, y: 0 });
	});

	test("a pan moves the layer by the offset modulo the pitch", () => {
		const layer = latticeLayer({ x: 50, y: -10, zoom: 1 });
		expect(layer.x).toBe((50 + LATTICE_BLEED) % 24);
		expect(layer.y).toBe(14); // (-10 + 48) mod 24
		expect(layer.x).toBeGreaterThanOrEqual(0);
		expect(layer.x).toBeLessThan(layer.pitch);
	});

	test("the shift stays inside one pitch however far the pan", () => {
		for (const x of [-100_000.37, -1, 0.5, 23.99, 99_999.9]) {
			const layer = latticeLayer({ x, y: x, zoom: 0.6 });
			expect(layer.x).toBeGreaterThanOrEqual(0);
			expect(layer.x).toBeLessThan(layer.pitch);
			expect(layer.y).toBeGreaterThanOrEqual(0);
			expect(layer.y).toBeLessThan(layer.pitch);
		}
	});

	test("the pitch follows the zoom and never gets tighter than the floor", () => {
		expect(latticeLayer({ x: 0, y: 0, zoom: 0.5 }).pitch).toBe(12);
		expect(latticeLayer({ x: 0, y: 0, zoom: 0.2 }).pitch).toBe(
			LATTICE_MIN_PITCH,
		);
		expect(latticeLayer({ x: 0, y: 0, zoom: 2 }).pitch).toBe(LATTICE_PITCH * 2);
	});

	test("the bleed covers the widest pitch", () => {
		expect(LATTICE_BLEED).toBeGreaterThanOrEqual(LATTICE_PITCH * 2);
	});
});
