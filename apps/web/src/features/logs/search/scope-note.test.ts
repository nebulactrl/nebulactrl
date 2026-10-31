import { describe, expect, test } from "bun:test";
import { searchScopeNote } from "./scope-note";

describe("searchScopeNote", () => {
	test("one line is not 'the last 1 lines'", () => {
		expect(searchScopeNote(1)).not.toContain("1 lines");
		expect(searchScopeNote(1)).toBe(
			"Searching the only line. Older lines are not kept.",
		);
	});

	test("many lines are counted with a fixed locale", () => {
		expect(searchScopeNote(1200)).toBe(
			"Searching the last 1,200 lines. Older lines are not kept.",
		);
	});

	test("an empty buffer says nothing is searched yet", () => {
		expect(searchScopeNote(0)).toBe(
			"Nothing to search yet. Older lines are not kept.",
		);
	});
});
