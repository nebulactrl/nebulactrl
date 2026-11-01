import { describe, expect, test } from "bun:test";
import { highlightSegments, type Node, parseQuery, tokenize } from "./query";

const kinds = (input: string) => tokenize(input).map((t) => t.kind);
const texts = (input: string) => tokenize(input).map((t) => t.text);
const ast = (input: string): Node | null => parseQuery(input).ast;

describe("tokenize", () => {
	test("splits words and keeps positions", () => {
		const tokens = tokenize("foo  bar");
		expect(tokens.map((t) => [t.kind, t.start, t.end])).toEqual([
			["text", 0, 3],
			["text", 5, 8],
		]);
	});

	test("a quoted phrase stays whole and escapes decode", () => {
		const [token] = tokenize('"say \\"hi\\" \\\\ there"');
		expect(token?.kind).toBe("phrase");
		expect(token?.text).toBe('say "hi" \\ there');
		expect(token?.unterminated).toBe(false);
	});

	test("an unterminated quote runs to the end and is flagged", () => {
		const [token] = tokenize('"open phrase');
		expect(token?.unterminated).toBe(true);
		expect(token?.text).toBe("open phrase");
		expect(token?.end).toBe(12);
	});

	test("negation: a minus glued to a term, NOT as a word", () => {
		expect(kinds("-foo")).toEqual(["minus", "text"]);
		expect(kinds("NOT foo")).toEqual(["not", "text"]);
		expect(kinds('-"a b"')).toEqual(["minus", "phrase"]);
		// A lone dash or a double dash is text, not negation of nothing or of a flag.
		expect(kinds("- foo")).toEqual(["text", "text"]);
		expect(texts("--verbose")).toEqual(["--verbose"]);
	});

	test("OR, AND and parentheses; lower case is a word", () => {
		expect(kinds("a OR b")).toEqual(["text", "or", "text"]);
		expect(kinds("(a AND b)")).toEqual([
			"lparen",
			"text",
			"and",
			"text",
			"rparen",
		]);
		expect(kinds("a or b")).toEqual(["text", "text", "text"]);
	});

	test("facet keys, attribute keys and their values", () => {
		const tokens = tokenize("level:error @http.status:500");
		expect(tokens.map((t) => [t.kind, t.text, t.attr === true])).toEqual([
			["key", "level", false],
			["value", "error", false],
			["key", "http.status", true],
			["value", "500", false],
		]);
	});

	test("a quoted value belongs to its key", () => {
		const tokens = tokenize('service:"my svc"');
		expect(tokens.map((t) => [t.kind, t.text, t.quoted === true])).toEqual([
			["key", "service", false],
			["value", "my svc", true],
		]);
	});

	test("comparison operators are part of the value", () => {
		const [, value] = tokenize("@duration:>=500");
		expect(value?.op).toBe(">=");
		expect(value?.text).toBe("500");
		expect(value?.start).toBe(10);
	});

	test("ranges, inclusive and exclusive", () => {
		const [, inclusive] = tokenize("@status:[400 TO 499]");
		expect(inclusive?.range).toEqual({
			lo: "400",
			hi: "499",
			loInc: true,
			hiInc: true,
			closed: true,
			ok: true,
		});
		const [, exclusive] = tokenize("@n:{1 TO *}");
		expect(exclusive?.range?.loInc).toBe(false);
		expect(exclusive?.range?.hiInc).toBe(false);
		expect(exclusive?.range?.hi).toBe("*");
	});

	test("wildcards split a value into parts; an escaped star is literal", () => {
		const [, value] = tokenize("service:api-*-prod");
		expect(value?.parts).toEqual(["api-", "-prod"]);
		const [, escaped] = tokenize("@v:a\\*b");
		expect(escaped?.parts).toEqual(["a*b"]);
	});

	test("@paths with dots and dashes are keys", () => {
		const [key] = tokenize("@usr.net-work.ip:1");
		expect(key?.kind).toBe("key");
		expect(key?.text).toBe("usr.net-work.ip");
	});

	test("key:( opens a value group where words are values", () => {
		const tokens = tokenize("level:(error OR warn)");
		expect(tokens.map((t) => t.kind)).toEqual([
			"key",
			"lparen",
			"value",
			"or",
			"value",
			"rparen",
		]);
		expect(tokens[1]?.group).toBe(true);
	});

	test("a URL is text, not a key", () => {
		expect(kinds("http://host/x")).toEqual(["text"]);
		expect(kinds("12:30:45")).toEqual(["text"]);
	});
});

describe("parseQuery", () => {
	test("an empty or blank query has no ast and no errors", () => {
		for (const input of ["", "   "]) {
			const parsed = parseQuery(input);
			expect(parsed.ast).toBeNull();
			expect(parsed.errors).toEqual([]);
		}
	});

	test("terms are ANDed by default", () => {
		const node = ast("foo bar baz");
		expect(node?.type).toBe("and");
		expect(node?.type === "and" && node.children.length).toBe(3);
	});

	test("AND binds tighter than OR", () => {
		const node = ast("a b OR c");
		expect(node?.type).toBe("or");
		if (node?.type !== "or") throw new Error("expected or");
		expect(node.children.map((c) => c.type)).toEqual(["and", "text"]);
	});

	test("parentheses regroup", () => {
		const node = ast("a (b OR c)");
		expect(node?.type).toBe("and");
		if (node?.type !== "and") throw new Error("expected and");
		expect(node.children[1]?.type).toBe("or");
	});

	test("negation wraps one term", () => {
		const node = ast("-foo bar");
		if (node?.type !== "and") throw new Error("expected and");
		expect(node.children[0]?.type).toBe("not");
		expect(node.children[1]?.type).toBe("text");
		expect(ast("NOT level:error")?.type).toBe("not");
	});

	test("field nodes carry key, attr flag and a typed value", () => {
		expect(ast("level:error")).toMatchObject({
			type: "field",
			key: "level",
			attr: false,
			value: { kind: "eq", text: "error" },
		});
		expect(ast("@duration:>500")).toMatchObject({
			type: "field",
			key: "duration",
			attr: true,
			value: { kind: "cmp", op: ">", n: 500 },
		});
		expect(ast("@status:[400 TO 499]")).toMatchObject({
			value: { kind: "range", lo: 400, hi: 499, loInc: true, hiInc: true },
		});
		expect(ast("@n:[10 TO *]")).toMatchObject({
			value: { kind: "range", lo: 10, hi: null },
		});
	});

	test("key:(a OR b) expands to an OR of fields on that key", () => {
		const node = ast("level:(error OR warn)");
		if (node?.type !== "or") throw new Error("expected or");
		expect(node.children).toMatchObject([
			{ type: "field", key: "level", value: { text: "error" } },
			{ type: "field", key: "level", value: { text: "warn" } },
		]);
	});

	test("nodes know where they start and end", () => {
		expect(ast("x level:error")).toMatchObject({ start: 0, end: 13 });
		const node = ast("x level:error");
		if (node?.type !== "and") throw new Error("expected and");
		expect(node.children[1]).toMatchObject({ start: 2, end: 13 });
	});
});

describe("parseQuery error recovery", () => {
	test("an unclosed parenthesis is reported at the paren; the rest still parses", () => {
		const parsed = parseQuery("(a OR b");
		expect(parsed.errors).toEqual([
			{ start: 0, end: 1, message: "Missing closing parenthesis" },
		]);
		expect(parsed.ast?.type).toBe("or");
	});

	test("a stray closing parenthesis is reported and skipped", () => {
		const parsed = parseQuery("a ) b");
		expect(parsed.errors).toEqual([
			{ start: 2, end: 3, message: "Unmatched closing parenthesis" },
		]);
		expect(parsed.ast?.type).toBe("and");
	});

	test("empty parentheses", () => {
		const parsed = parseQuery("a ()");
		expect(parsed.errors).toEqual([
			{ start: 2, end: 3, message: "Empty parentheses" },
		]);
		expect(parsed.ast).toMatchObject({ type: "text", text: "a" });
	});

	test("a dangling OR names the missing side", () => {
		expect(parseQuery("a OR").errors).toEqual([
			{ start: 2, end: 4, message: "OR needs a term after it" },
		]);
		expect(parseQuery("OR a").errors).toEqual([
			{ start: 0, end: 2, message: "OR needs a term before it" },
		]);
		expect(parseQuery("a OR OR b").errors.map((e) => e.message)).toEqual([
			"OR needs a term after it",
		]);
	});

	test("a dangling AND", () => {
		expect(parseQuery("a AND").errors).toEqual([
			{ start: 2, end: 5, message: "AND needs a term on both sides" },
		]);
	});

	test("nothing to negate", () => {
		expect(parseQuery("a NOT").errors).toEqual([
			{ start: 2, end: 5, message: "Nothing to negate" },
		]);
		expect(parseQuery("a NOT").ast).toMatchObject({ type: "text", text: "a" });
	});

	test("a facet with no value is dropped; free text 'Error:' is not a facet", () => {
		const parsed = parseQuery("level: foo");
		expect(parsed.errors).toEqual([
			{ start: 0, end: 6, message: "Add a value after level:" },
		]);
		expect(parsed.ast).toMatchObject({ type: "text", text: "foo" });
		expect(parseQuery("Error: boom").errors).toEqual([]);
	});

	test("an unknown facet with a value is searched as text and says how to fix it", () => {
		const parsed = parseQuery("servce:api");
		expect(parsed.errors).toHaveLength(1);
		expect(parsed.errors[0]).toMatchObject({ start: 0, end: 7 });
		expect(parsed.errors[0]?.message).toContain('Unknown facet "servce"');
		expect(parsed.errors[0]?.message).toContain("@servce");
		expect(parsed.ast).toMatchObject({ type: "text", text: "servce:api" });
	});

	test("a non-numeric comparison", () => {
		const parsed = parseQuery("@duration:>fast ok");
		expect(parsed.errors).toEqual([
			{ start: 10, end: 15, message: "Expected a number after >" },
		]);
		expect(parsed.ast).toMatchObject({ type: "text", text: "ok" });
	});

	test("a broken range", () => {
		expect(parseQuery("@n:[1 2]").errors[0]?.message).toBe(
			"Write a range as [400 TO 499]",
		);
		expect(parseQuery("@n:[1 TO 5").errors[0]?.message).toBe(
			"Missing closing ]",
		);
		expect(parseQuery("@n:[a TO b]").errors[0]?.message).toBe(
			"Range bounds are numbers or *",
		);
	});

	test("an unterminated quote still searches what was typed", () => {
		const parsed = parseQuery('"half');
		expect(parsed.errors).toEqual([
			{ start: 0, end: 5, message: "Missing closing quote" },
		]);
		expect(parsed.ast).toMatchObject({
			type: "text",
			text: "half",
			phrase: true,
		});
	});

	test("never throws on junk", () => {
		for (const input of [
			")",
			"((((",
			"))))",
			"-",
			"NOT NOT",
			"OR OR",
			":::",
			"@:",
			'"',
			"[",
			"a:(",
			"a:(b",
			"\\",
		]) {
			expect(() => parseQuery(input)).not.toThrow();
		}
	});
});

describe("highlightSegments", () => {
	test("covers the whole input with classified runs", () => {
		const parsed = parseQuery("level:error -@id:>5 foo");
		const segments = highlightSegments(parsed);
		expect(
			segments.map((s) => parsed.input.slice(s.start, s.end)).join(""),
		).toBe(parsed.input);
		const byText = segments.map((s) => [
			parsed.input.slice(s.start, s.end),
			s.kind,
		]);
		expect(byText).toContainEqual(["level:", "key"]);
		expect(byText).toContainEqual(["error", "value"]);
		expect(byText).toContainEqual(["-", "operator"]);
		expect(byText).toContainEqual(["@id:", "attr"]);
		expect(byText).toContainEqual([">", "operator"]);
		expect(byText).toContainEqual(["5", "value"]);
		expect(byText).toContainEqual(["foo", "text"]);
	});

	test("errors are marked invalid", () => {
		const parsed = parseQuery("a OR");
		const invalid = highlightSegments(parsed).filter(
			(s) => s.kind === "invalid",
		);
		expect(invalid.map((s) => parsed.input.slice(s.start, s.end))).toEqual([
			"OR",
		]);
	});
});
