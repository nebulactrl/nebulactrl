import { describe, expect, test } from "bun:test";
import {
	isSafeHref,
	parseInline,
	parseMarkdown,
} from "./release-notes-markdown";

describe("parseInline", () => {
	test("plain text stays one node", () => {
		expect(parseInline("Faster deploys")).toEqual([
			{ type: "text", value: "Faster deploys" },
		]);
	});

	test("code, bold and italic", () => {
		expect(parseInline("Run `make up` **now** or *later*")).toEqual([
			{ type: "text", value: "Run " },
			{ type: "code", value: "make up" },
			{ type: "text", value: " " },
			{ type: "strong", children: [{ type: "text", value: "now" }] },
			{ type: "text", value: " or " },
			{ type: "em", children: [{ type: "text", value: "later" }] },
		]);
	});

	test("snake_case words are not italic", () => {
		expect(parseInline("set update_check_enabled to false")).toEqual([
			{ type: "text", value: "set update_check_enabled to false" },
		]);
	});

	test("an http link is kept", () => {
		expect(parseInline("[notes](https://nebulactrl.com/r/1)")).toEqual([
			{
				type: "link",
				href: "https://nebulactrl.com/r/1",
				children: [{ type: "text", value: "notes" }],
			},
		]);
	});

	test("a script or relative link falls back to its label", () => {
		expect(parseInline("[x](javascript:alert(1))")).toEqual([
			{ type: "text", value: "x)" },
		]);
		expect(parseInline("[docs](/docs)")).toEqual([
			{ type: "text", value: "docs" },
		]);
	});

	test("raw HTML is only ever text", () => {
		expect(parseInline("<img src=x onerror=alert(1)>")).toEqual([
			{ type: "text", value: "<img src=x onerror=alert(1)>" },
		]);
	});

	test("an escaped marker is literal", () => {
		expect(parseInline("2 \\* 3")).toEqual([{ type: "text", value: "2 * 3" }]);
	});
});

describe("isSafeHref", () => {
	test("accepts http and https only", () => {
		expect(isSafeHref("https://a.example/x")).toBe(true);
		expect(isSafeHref("http://a.example")).toBe(true);
		expect(isSafeHref("javascript:alert(1)")).toBe(false);
		expect(isSafeHref("data:text/html,x")).toBe(false);
		expect(isSafeHref("//a.example")).toBe(false);
	});
});

describe("parseMarkdown", () => {
	test("headings, a paragraph and a bullet list", () => {
		const blocks = parseMarkdown(
			"## What's new\n\nFaster\ndeploys.\n\n- One\n- Two\n  more\n",
		);
		expect(blocks).toEqual([
			{ type: "heading", children: [{ type: "text", value: "What's new" }] },
			{
				type: "paragraph",
				children: [{ type: "text", value: "Faster deploys." }],
			},
			{
				type: "list",
				ordered: false,
				items: [
					[{ type: "text", value: "One" }],
					[{ type: "text", value: "Two more" }],
				],
			},
		]);
	});

	test("a numbered list", () => {
		expect(parseMarkdown("1. a\n2. b")).toEqual([
			{
				type: "list",
				ordered: true,
				items: [[{ type: "text", value: "a" }], [{ type: "text", value: "b" }]],
			},
		]);
	});

	test("a fenced block keeps its lines verbatim", () => {
		expect(parseMarkdown("```sh\nsudo bash\n  --flag\n```\nafter")).toEqual([
			{ type: "code", value: "sudo bash\n  --flag" },
			{ type: "paragraph", children: [{ type: "text", value: "after" }] },
		]);
	});

	test("an unclosed fence runs to the end", () => {
		expect(parseMarkdown("```\nx")).toEqual([{ type: "code", value: "x" }]);
	});

	test("a rule is dropped and CRLF is read", () => {
		expect(parseMarkdown("a\r\n\r\n---\r\n\r\nb")).toEqual([
			{ type: "paragraph", children: [{ type: "text", value: "a" }] },
			{ type: "paragraph", children: [{ type: "text", value: "b" }] },
		]);
	});

	test("empty notes parse to nothing", () => {
		expect(parseMarkdown("")).toEqual([]);
		expect(parseMarkdown("  \n\n ")).toEqual([]);
	});
});
