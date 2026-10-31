import { describe, expect, test } from "bun:test";
import { matrixTones } from "../../lib/bayer";
import { pickMatrix, resolveMatrix } from "./matrix";

describe("matrixTones", () => {
	test("is the cell count plus one, capped for blue noise", () => {
		expect(matrixTones("bayer2")).toBe(5);
		expect(matrixTones("bayer4")).toBe(17);
		expect(matrixTones("bayer8")).toBe(65);
		expect(matrixTones("blue-noise")).toBe(256);
	});
});

describe("pickMatrix", () => {
	test("matrix beats n beats the fallback", () => {
		expect(pickMatrix("blue-noise", 8, "bayer4")).toBe("blue-noise");
		expect(pickMatrix(undefined, 2, "bayer4")).toBe("bayer2");
		expect(pickMatrix(undefined, undefined, "bayer8")).toBe("bayer8");
	});
});

describe("resolveMatrix", () => {
	test("uses the defaults when nothing is given", () => {
		expect(
			resolveMatrix({}, { matrix: "bayer4", staticTime: 5, levels: "full" }),
		).toEqual({ matrix: "bayer4", levels: 17, staticTime: 5 });
	});

	test("full levels follow the chosen matrix", () => {
		expect(
			resolveMatrix(
				{ matrix: "bayer8" },
				{ matrix: "bayer4", staticTime: 0, levels: "full" },
			).levels,
		).toBe(65);
		expect(
			resolveMatrix(
				{ n: 2 },
				{ matrix: "bayer4", staticTime: 0, levels: "full" },
			).levels,
		).toBe(5);
	});

	test("explicit levels and a numeric default are kept", () => {
		expect(
			resolveMatrix(
				{ levels: 3 },
				{ matrix: "bayer4", staticTime: 0, levels: 12 },
			).levels,
		).toBe(3);
		expect(
			resolveMatrix({}, { matrix: "bayer4", staticTime: 0, levels: 12 }).levels,
		).toBe(12);
	});

	test("levels stay undefined without a default", () => {
		expect(
			resolveMatrix({}, { matrix: "bayer4", staticTime: 0 }).levels,
		).toBeUndefined();
	});

	test("stillTime beats staticTime beats the default", () => {
		const d = { matrix: "bayer4", staticTime: 7 } as const;
		expect(resolveMatrix({ stillTime: 1, staticTime: 2 }, d).staticTime).toBe(
			1,
		);
		expect(resolveMatrix({ staticTime: 2 }, d).staticTime).toBe(2);
		expect(resolveMatrix({}, d).staticTime).toBe(7);
		expect(resolveMatrix({ stillTime: 0 }, d).staticTime).toBe(0);
	});
});
