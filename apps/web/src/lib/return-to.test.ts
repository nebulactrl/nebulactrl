import { describe, expect, test } from "bun:test";
import { safeReturnTo } from "./return-to";

describe("safeReturnTo", () => {
	test("keeps a same-origin path with its query", () => {
		expect(safeReturnTo("/p/abc/production?service=1")).toBe(
			"/p/abc/production?service=1",
		);
	});

	test("falls back to the home page for anything that could leave the origin", () => {
		expect(safeReturnTo(undefined)).toBe("/");
		expect(safeReturnTo("")).toBe("/");
		expect(safeReturnTo("https://evil.example/")).toBe("/");
		expect(safeReturnTo("//evil.example/")).toBe("/");
		expect(safeReturnTo("/\\evil.example")).toBe("/");
	});
});
