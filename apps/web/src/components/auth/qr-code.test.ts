import { describe, expect, test } from "bun:test";
import { qrMatrix, qrPath } from "./qr-code";

const URI =
	"otpauth://totp/NebulaCtrl:you@example.com?secret=JBSWY3DPEHPK3PXP&issuer=NebulaCtrl";

describe("qrMatrix", () => {
	test("is square with the three finder patterns", () => {
		const matrix = qrMatrix(URI);
		const size = matrix.length;
		expect((size - 17) % 4).toBe(0);
		for (const row of matrix) expect(row).toHaveLength(size);
		// The outer ring of a finder pattern is dark, its inner ring light.
		for (const [r, c] of [
			[0, 0],
			[0, size - 7],
			[size - 7, 0],
		] as const) {
			expect(matrix[r]?.[c]).toBe(true);
			expect(matrix[r + 6]?.[c + 6]).toBe(true);
			expect(matrix[r + 1]?.[c + 1]).toBe(false);
			expect(matrix[r + 3]?.[c + 3]).toBe(true);
		}
	});
	test("the same text gives the same code", () => {
		expect(qrMatrix(URI)).toEqual(qrMatrix(URI));
	});
});

describe("qrPath", () => {
	test("merges a run of dark modules into one rectangle, inside the quiet zone", () => {
		expect(qrPath([[true, true, false, true]])).toBe(
			"M4 4h2v1h-2zM7 4h1v1h-1z",
		);
	});
	test("draws nothing for an empty matrix", () => {
		expect(qrPath([[false, false]])).toBe("");
	});
});
