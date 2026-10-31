import { describe, expect, test } from "bun:test";
import { parseFrontmatter, readDocFrontmatter } from "./frontmatter";

describe("parseFrontmatter", () => {
	test("reads strings, quoted strings, numbers and booleans", () => {
		const { data, body, lines } = parseFrontmatter(
			[
				"---",
				"title: Variables and secrets",
				'description: "Encrypted: values, references"',
				"section: Applications",
				"order: 30",
				"generated: true",
				"---",
				"# Heading",
			].join("\n"),
		);
		expect(data).toEqual({
			title: "Variables and secrets",
			description: "Encrypted: values, references",
			section: "Applications",
			order: 30,
			generated: true,
		});
		expect(body).toBe("# Heading");
		expect(lines).toBe(7);
	});

	test("a file without a fence has no data and keeps its body", () => {
		const parsed = parseFrontmatter("# Title\n\ntext");
		expect(parsed.data).toEqual({});
		expect(parsed.body).toBe("# Title\n\ntext");
		expect(parsed.lines).toBe(0);
	});

	test("rejects an unclosed fence, duplicate keys and structures it cannot read", () => {
		expect(() => parseFrontmatter("---\ntitle: a\n# x")).toThrow("not closed");
		expect(() => parseFrontmatter("---\ntitle: a\ntitle: b\n---\n")).toThrow(
			"duplicate",
		);
		expect(() => parseFrontmatter("---\ntags: [a, b]\n---\n")).toThrow(
			"not supported",
		);
		expect(() => parseFrontmatter("---\nnot a pair\n---\n")).toThrow(
			"key: value",
		);
	});
});

describe("readDocFrontmatter", () => {
	test("validates types", () => {
		expect(readDocFrontmatter({ title: "A", order: 2 })).toMatchObject({
			title: "A",
			order: 2,
		});
		expect(() => readDocFrontmatter({ title: 4 })).toThrow("title");
		expect(() => readDocFrontmatter({ order: "x" })).toThrow("order");
	});
});
