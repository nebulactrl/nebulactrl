import { describe, expect, test } from "bun:test";
import {
	AVATAR_HUES,
	avatarColor,
	avatarColors,
	avatarColorsFrom,
	avatarFace,
	FACE_SIZE,
	faceBitmap,
	faceFor,
	faceRuns,
	hashName,
	seededRandom,
} from "./face";

const NAMES = [
	"Alan Turing",
	"ada@example.com",
	"Ada Lovelace",
	"Grace Hopper",
	"x",
	"",
	"Админ",
	"田中 太郎",
	"deploy-bot",
];

describe("hashName", () => {
	test("is stable, case/space-insensitive and 32-bit", () => {
		expect(hashName("Ada")).toBe(hashName("  ada "));
		expect(hashName("ada")).not.toBe(hashName("adah"));
		for (const name of NAMES) {
			const h = hashName(name);
			expect(Number.isInteger(h)).toBe(true);
			expect(h).toBeGreaterThanOrEqual(0);
			expect(h).toBeLessThan(2 ** 32);
		}
	});
});

describe("seededRandom", () => {
	test("repeats for a seed and stays in [0, 1)", () => {
		const a = seededRandom(42);
		const b = seededRandom(42);
		for (let i = 0; i < 50; i++) {
			const value = a();
			expect(value).toBe(b());
			expect(value).toBeGreaterThanOrEqual(0);
			expect(value).toBeLessThan(1);
		}
	});
});

describe("faceBitmap", () => {
	test("is deterministic", () => {
		for (const name of NAMES) {
			expect(Array.from(faceBitmap(name))).toEqual(
				Array.from(faceBitmap(name)),
			);
		}
	});

	test("is left/right symmetric", () => {
		for (const name of NAMES) {
			const bits = faceBitmap(name);
			expect(bits.length).toBe(FACE_SIZE * FACE_SIZE);
			for (let y = 0; y < FACE_SIZE; y++) {
				for (let x = 0; x < FACE_SIZE; x++) {
					expect(bits[y * FACE_SIZE + x]).toBe(
						bits[y * FACE_SIZE + (FACE_SIZE - 1 - x)] ?? -1,
					);
				}
			}
		}
	});

	test("always has eyes and a mouth, and only 0/1 cells", () => {
		for (const name of NAMES) {
			const bits = faceBitmap(name);
			const rows = (from: number, to: number) =>
				bits.slice(from * FACE_SIZE, to * FACE_SIZE).some((cell) => cell === 1);
			expect(rows(3, 5)).toBe(true);
			expect(rows(5, 8)).toBe(true);
			for (const cell of bits) expect(cell === 0 || cell === 1).toBe(true);
		}
	});

	test("different names give a spread of faces", () => {
		const seen = new Set(
			Array.from({ length: 60 }, (_, i) => faceBitmap(`user-${i}`).join("")),
		);
		expect(seen.size).toBeGreaterThan(20);
	});
});

describe("faceFor", () => {
	test("hue comes from the palette and matches the hash", () => {
		for (const name of NAMES) {
			const face = faceFor(name);
			expect(face.hueIndex).toBe(hashName(name) % AVATAR_HUES.length);
			expect(face.hue).toBe(AVATAR_HUES[face.hueIndex] as number);
		}
	});
});

describe("faceRuns", () => {
	test("covers exactly the inked cells", () => {
		for (const name of NAMES) {
			const bits = faceBitmap(name);
			const covered = faceRuns(bits).reduce((sum, run) => sum + run.w, 0);
			expect(covered).toBe(bits.reduce((sum, cell) => sum + cell, 0));
		}
	});
});

describe("avatarColors", () => {
	test("tints every colour with the hue", () => {
		for (const value of Object.values(avatarColors(165))) {
			expect(value).toContain("165");
		}
	});
});

describe("avatarColorsFrom", () => {
	test("mixes the given colour into all four slots", () => {
		const colors = avatarColorsFrom("var(--dither-blue)");
		for (const value of Object.values(colors)) {
			expect(value).toContain("var(--dither-blue)");
			expect(value).toContain("color-mix");
		}
		expect(colors.light).toContain("white");
		expect(colors.ink).toContain("black");
	});
});

describe("avatarFace and avatarColor", () => {
	test("are the face and skin colour the avatar draws for a name", () => {
		expect([...avatarFace("Ada Lovelace")]).toEqual([
			...faceBitmap("Ada Lovelace"),
		]);
		expect(avatarColor("Ada Lovelace")).toBe(
			avatarColors(faceFor("Ada Lovelace").hue).base,
		);
		expect(avatarColor("Ada Lovelace")).toBe(avatarColor("  ada lovelace "));
	});
});
