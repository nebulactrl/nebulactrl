import { describe, expect, test } from "bun:test";
import {
	ILLUSTRATION_HEIGHT,
	ILLUSTRATION_WIDTH,
	ILLUSTRATIONS,
	illustrationPaths,
	illustrationScene,
} from "./illustration";

describe("illustrationScene", () => {
	test("every object fills part of the grid and leaves room around it", () => {
		for (const name of ILLUSTRATIONS) {
			const scene = illustrationScene(name);
			expect(scene.width).toBe(ILLUSTRATION_WIDTH);
			expect(scene.height).toBe(ILLUSTRATION_HEIGHT);
			const filled = scene.parts.filter((part) => part > 0).length;
			const total = scene.width * scene.height;
			expect(filled).toBeGreaterThan(total * 0.08);
			expect(filled).toBeLessThan(total * 0.5);
		}
	});

	test("a one-cell seam splits the parts: different parts never touch", () => {
		for (const name of ILLUSTRATIONS) {
			const { width, height, parts } = illustrationScene(name);
			const parted = new Set<number>();
			for (let y = 0; y < height; y++) {
				for (let x = 0; x < width; x++) {
					const p = parts[y * width + x] ?? 0;
					if (p > 0) parted.add(p);
					const right = x + 1 < width ? (parts[y * width + x + 1] ?? 0) : 0;
					const below = y + 1 < height ? (parts[(y + 1) * width + x] ?? 0) : 0;
					if (p > 0 && right > 0) expect(right).toBe(p);
					if (p > 0 && below > 0) expect(below).toBe(p);
				}
			}
			// The objects are built from several parts, not one blob.
			expect(parted.size).toBeGreaterThan(1);
		}
	});

	test("the halo keeps a clear margin round the object", () => {
		for (const name of ILLUSTRATIONS) {
			const { width, height, parts, ambient } = illustrationScene(name);
			for (let y = 0; y < height; y++) {
				for (let x = 0; x < width; x++) {
					if ((parts[y * width + x] ?? 0) === 0) continue;
					for (let dy = -2; dy <= 2; dy++) {
						for (let dx = -2; dx <= 2; dx++) {
							const nx = x + dx;
							const ny = y + dy;
							if (nx < 0 || ny < 0 || nx >= width || ny >= height) continue;
							expect(ambient[ny * width + nx]).toBe(0);
						}
					}
				}
			}
		}
	});

	test("the lit side is denser than the far side", () => {
		const { width, tones, parts } = illustrationScene("planet");
		// Row through the sphere: the upper-left is lit, the lower-right is not.
		const lit = tones[28 * width + 46] ?? 0;
		const dark = tones[44 * width + 66] ?? 1;
		expect(parts[28 * width + 46]).toBeGreaterThan(0);
		expect(lit).toBeGreaterThan(dark);
	});
});

describe("illustrationPaths", () => {
	test("paths are not empty, and differ by object and matrix", () => {
		const seen = new Set<string>();
		for (const name of ILLUSTRATIONS) {
			const bayer = illustrationPaths(name, "bayer4");
			expect(bayer.object.length).toBeGreaterThan(100);
			expect(bayer.ambient.length).toBeGreaterThan(0);
			seen.add(bayer.object);
			expect(illustrationPaths(name, "blue-noise").object).not.toBe(
				bayer.object,
			);
		}
		expect(seen.size).toBe(ILLUSTRATIONS.length);
	});

	test("is cached by object and matrix", () => {
		expect(illustrationPaths("box", "bayer8")).toBe(
			illustrationPaths("box", "bayer8"),
		);
	});

	test("rects stay on the grid", () => {
		const { object } = illustrationPaths("folder", "bayer4");
		for (const [, x, y, w] of object.matchAll(/M(\d+) (\d+)h(\d+)/g)) {
			expect(Number(x) + Number(w)).toBeLessThanOrEqual(ILLUSTRATION_WIDTH);
			expect(Number(y)).toBeLessThan(ILLUSTRATION_HEIGHT);
		}
	});
});
