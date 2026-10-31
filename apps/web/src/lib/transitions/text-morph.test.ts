import { describe, expect, test } from "bun:test";
import { sharedAffixes } from "./text-morph";

describe("sharedAffixes", () => {
	test("Copy -> Copied keeps the shared start", () => {
		expect(sharedAffixes("Copy", "Copied")).toEqual({ start: 3, end: 0 });
	});

	test("shared end is found after the start", () => {
		// "Copy page as Markdown" -> "Copied as Markdown"
		const prev = "Copy page as Markdown";
		const next = "Copied as Markdown";
		const { start, end } = sharedAffixes(prev, next);
		expect(prev.slice(0, start)).toBe("Cop");
		expect(prev.slice(prev.length - end)).toBe(" as Markdown");
	});

	test("the two shared runs never overlap", () => {
		expect(sharedAffixes("aa", "aaa")).toEqual({ start: 2, end: 0 });
		expect(sharedAffixes("abab", "ab")).toEqual({ start: 2, end: 0 });
	});

	test("texts that share nothing change as a whole", () => {
		expect(sharedAffixes("Processing", "Done")).toEqual({ start: 0, end: 0 });
	});

	test("identical texts share everything", () => {
		expect(sharedAffixes("Save", "Save")).toEqual({ start: 4, end: 0 });
	});
});
