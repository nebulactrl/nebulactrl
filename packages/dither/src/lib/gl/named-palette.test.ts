import { describe, expect, test } from "bun:test";
import { DITHER_COLOR_NAMES } from "../colors";
import { cssColorExpr } from "./palette";

describe("cssColorExpr with named colours", () => {
	test("every name resolves through its --dither-<name> token with a fallback", () => {
		for (const name of DITHER_COLOR_NAMES) {
			expect(cssColorExpr(name)).toMatch(
				new RegExp(`^var\\(--dither-${name}, #[0-9a-f]{6}\\)$`),
			);
		}
	});

	test("ink and paper keep their theme fallbacks", () => {
		expect(cssColorExpr("ink")).toContain("var(--dither-ink, ");
		expect(cssColorExpr("paper")).toContain("var(--dither-paper, ");
	});

	test("a name inside whitespace still resolves; real colours pass through", () => {
		expect(cssColorExpr("  blue ")).toContain("var(--dither-blue, ");
		expect(cssColorExpr("rebeccapurple")).toBe("rebeccapurple");
		expect(cssColorExpr("transparent")).toBe("transparent");
	});
});
