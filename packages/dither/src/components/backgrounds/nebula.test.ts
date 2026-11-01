import { describe, expect, test } from "bun:test";
import { NEBULA_PALETTE, resolveNebulaPalette } from "./nebula-palette";

describe("resolveNebulaPalette", () => {
	test("no palette gives the defaults", () => {
		expect(resolveNebulaPalette()).toEqual([...NEBULA_PALETTE]);
		expect(resolveNebulaPalette([])).toEqual([...NEBULA_PALETTE]);
	});

	test("always six colours: sky, thin gas, mid gas, knots, rim, stars", () => {
		expect(NEBULA_PALETTE).toHaveLength(6);
		expect(NEBULA_PALETTE[2]).toBe("#6d6bff");
		expect(resolveNebulaPalette(["a", "b"])).toHaveLength(6);
	});

	test("a short palette keeps its colours and takes the rest from the defaults", () => {
		const out = resolveNebulaPalette(["--surface", "navy", "--accent"]);
		expect(out.slice(0, 3)).toEqual(["--surface", "navy", "--accent"]);
		expect(out.slice(3)).toEqual([...NEBULA_PALETTE].slice(3));
	});

	test("blank entries fall back and entries are trimmed", () => {
		const out = resolveNebulaPalette(["  ", " #123456 ", ""]);
		expect(out[0]).toBe(NEBULA_PALETTE[0]);
		expect(out[1]).toBe("#123456");
		expect(out[2]).toBe(NEBULA_PALETTE[2]);
	});

	test("extra entries are dropped", () => {
		const out = resolveNebulaPalette(["a", "b", "c", "d", "e", "f", "g", "h"]);
		expect(out).toEqual(["a", "b", "c", "d", "e", "f"]);
	});
});
