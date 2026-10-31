import { describe, expect, test } from "bun:test";
import { collapseDiff, composeChangeText, diffLines, diffStats } from "./diff";

const OLD = `services:
  web:
    image: nginx:1.26
    ports:
      - "80"
  db:
    image: postgres:16
`;
const NEW = `services:
  web:
    image: nginx:1.27
    ports:
      - "80"
      - "443"
  db:
    image: postgres:16
`;

describe("diffLines", () => {
	test("marks removed and added lines with their numbers", () => {
		const diff = diffLines(OLD, NEW);
		expect(
			diff.filter((l) => l.kind !== "same").map((l) => [l.kind, l.text]),
		).toEqual([
			["remove", "    image: nginx:1.26"],
			["add", "    image: nginx:1.27"],
			["add", '      - "443"'],
		]);
		const removed = diff.find((l) => l.kind === "remove");
		expect([removed?.oldLine, removed?.newLine]).toEqual([3, null]);
		const added = diff.find((l) => l.text.includes("443"));
		expect([added?.oldLine, added?.newLine]).toEqual([null, 6]);
	});

	test("equal texts are all the same", () => {
		const diff = diffLines(OLD, OLD);
		expect(diff.every((l) => l.kind === "same")).toBe(true);
		expect(diffStats(diff)).toEqual({ added: 0, removed: 0 });
	});

	test("an empty side is all added or all removed", () => {
		expect(diffStats(diffLines("", "a\nb\n"))).toEqual({
			added: 2,
			removed: 0,
		});
		expect(diffStats(diffLines("a\nb\n", ""))).toEqual({
			added: 0,
			removed: 2,
		});
	});

	test("line endings do not count as changes", () => {
		expect(diffStats(diffLines("a\r\nb\r\n", "a\nb\n"))).toEqual({
			added: 0,
			removed: 0,
		});
	});

	test("reordered blocks keep the common lines", () => {
		const diff = diffLines("a\nb\nc\nd\n", "a\nc\nb\nd\n");
		expect(diffStats(diff)).toEqual({ added: 1, removed: 1 });
	});
});

describe("collapseDiff", () => {
	test("keeps context around changes and counts the hidden lines", () => {
		const base = Array.from({ length: 20 }, (_, i) => `line ${i}`);
		const changed = [...base];
		changed[10] = "changed";
		const items = collapseDiff(
			diffLines(base.join("\n"), changed.join("\n")),
			2,
		);
		expect(items[0]).toEqual({ kind: "gap", hidden: 8 });
		expect(items.at(-1)).toEqual({ kind: "gap", hidden: 7 });
		expect(items.filter((i) => i.kind !== "gap")).toHaveLength(6);
	});
});

describe("composeChangeText", () => {
	test("says how many lines changed", () => {
		expect(composeChangeText(OLD, NEW)).toBe("compose file: +2 −1 lines");
		expect(composeChangeText("a\n", "b\n")).toBe("compose file: +1 −1 lines");
		expect(composeChangeText("a\n", "a\n")).toBe("compose file unchanged");
	});
});
