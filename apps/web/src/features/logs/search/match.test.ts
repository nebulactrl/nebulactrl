import { describe, expect, test } from "bun:test";
import { type LogInput, parseLine } from "./line";
import {
	compileQuery,
	findHighlights,
	highlightTerms,
	matchAll,
} from "./match";
import { parseQuery } from "./query";

const matcher = (query: string) => compileQuery(parseQuery(query).ast);
const line = (text: string, extra: Omit<LogInput, "line"> = {}) =>
	parseLine({ line: text, ...extra });
const hits = (query: string, text: string, extra?: Omit<LogInput, "line">) =>
	matcher(query)(line(text, extra));

describe("free text", () => {
	test("is a case-insensitive substring", () => {
		expect(hits("timeout", "Request TIMEOUT after 5s")).toBe(true);
		expect(hits("timeout", "all fine")).toBe(false);
	});

	test("terms are ANDed, in any order", () => {
		expect(hits("db timeout", "timeout talking to db")).toBe(true);
		expect(hits("db timeout", "timeout talking to cache")).toBe(false);
	});

	test("a quoted phrase matches as a whole", () => {
		expect(hits('"connection refused"', "dial: connection refused")).toBe(true);
		expect(hits('"connection refused"', "connection was refused")).toBe(false);
	});

	test("wildcards match in order", () => {
		expect(hits("err*out", "error timeout")).toBe(true);
		expect(hits("err*out", "timeout error")).toBe(false);
		expect(hits("time*", "timeout")).toBe(true);
	});

	test("a wildcard inside quotes is a plain star", () => {
		expect(hits('"a*b"', "a*b")).toBe(true);
		expect(hits('"a*b"', "axxb")).toBe(false);
	});
});

describe("boolean logic", () => {
	const table: [string, string, boolean][] = [
		["foo OR bar", "only bar", true],
		["foo OR bar", "neither", false],
		["foo bar OR baz", "baz alone", true],
		["foo bar OR baz", "foo alone", false],
		["(foo OR bar) baz", "bar baz", true],
		["(foo OR bar) baz", "bar alone", false],
		["-health", "GET /orders", true],
		["-health", "GET /health", false],
		["NOT health", "GET /health", false],
		["foo -bar", "foo only", true],
		["foo -bar", "foo bar", false],
		["-(foo OR bar)", "baz", true],
		["-(foo OR bar)", "foo", false],
		["foo AND bar", "bar foo", true],
		["foo AND bar", "foo", false],
	];
	for (const [query, text, expected] of table) {
		test(`${query} on "${text}" is ${expected}`, () => {
			expect(hits(query, text)).toBe(expected);
		});
	}
});

describe("facets", () => {
	test("level: exact, with aliases", () => {
		expect(hits("level:error", "ERROR boom")).toBe(true);
		expect(hits("level:error", "WARN slow")).toBe(false);
		expect(hits("level:warning", "WARN slow")).toBe(true);
		expect(hits("level:ERR", "ERROR boom")).toBe(true);
		expect(hits("level:error", "plain text")).toBe(false);
		expect(hits("-level:error", "plain text")).toBe(true);
	});

	test("level:(a OR b)", () => {
		expect(hits("level:(error OR warn)", "WARN slow")).toBe(true);
		expect(hits("level:(error OR warn)", "INFO ok")).toBe(false);
	});

	test("service: case-insensitive exact, with wildcards and exists", () => {
		const api = { serviceName: "API-prod" };
		expect(hits("service:api-prod", "x", api)).toBe(true);
		expect(hits("service:api", "x", api)).toBe(false);
		expect(hits("service:api-*", "x", api)).toBe(true);
		expect(hits("service:*-prod", "x", api)).toBe(true);
		expect(hits("service:*", "x", api)).toBe(true);
		expect(hits("service:*", "x")).toBe(false);
		expect(hits('service:"api-prod"', "x", api)).toBe(true);
	});

	test("stream, replica and deployment", () => {
		const meta = { stream: "stderr", replica: 2, deploymentId: "dep-1" };
		expect(hits("stream:stderr", "x", meta)).toBe(true);
		expect(hits("stream:stdout", "x", meta)).toBe(false);
		expect(hits("replica:2", "x", meta)).toBe(true);
		expect(hits("replica:>=2", "x", meta)).toBe(true);
		expect(hits("replica:>2", "x", meta)).toBe(false);
		expect(hits("deployment:dep-*", "x", meta)).toBe(true);
	});

	test("status from an access line, exact, wildcard and numeric", () => {
		const text = '1.2.3.4 - - [x] "GET /x HTTP/1.1" 404 12';
		expect(hits("status:404", text)).toBe(true);
		expect(hits("status:4*", text)).toBe(true);
		expect(hits("status:>=400", text)).toBe(true);
		expect(hits("status:>=500", text)).toBe(false);
		expect(hits("status:[400 TO 499]", text)).toBe(true);
		expect(hits("status:200", "no status here")).toBe(false);
	});
});

describe("attributes", () => {
	const json =
		'{"user":{"id":42,"name":"Bob"},"duration":612,"tags":["a","b"],"ok":true}';

	test("@path equality, nested, case-insensitive for strings", () => {
		expect(hits("@user.id:42", json)).toBe(true);
		expect(hits("@user.id:43", json)).toBe(false);
		expect(hits("@user.name:bob", json)).toBe(true);
		expect(hits("@user.name:b*", json)).toBe(true);
		expect(hits("@ok:true", json)).toBe(true);
	});

	test("a missing attribute does not match, and its negation does", () => {
		expect(hits("@nope:1", json)).toBe(false);
		expect(hits("-@nope:1", json)).toBe(true);
		expect(hits("@nope:*", json)).toBe(false);
		expect(hits("@user.id:*", json)).toBe(true);
	});

	test("an array attribute matches when any element does", () => {
		expect(hits("@tags:b", json)).toBe(true);
		expect(hits("@tags:c", json)).toBe(false);
	});

	test("numeric comparisons", () => {
		expect(hits("@duration:>500", json)).toBe(true);
		expect(hits("@duration:>612", json)).toBe(false);
		expect(hits("@duration:>=612", json)).toBe(true);
		expect(hits("@duration:<1000", json)).toBe(true);
		expect(hits("@duration:<=611", json)).toBe(false);
	});

	test("comparisons read logfmt strings as numbers", () => {
		expect(hits("@duration:>500", "level=info duration=612 path=/x")).toBe(
			true,
		);
		expect(hits("@path:>5", "level=info duration=612 path=/x")).toBe(false);
	});

	test("ranges: inclusive, exclusive, open ended", () => {
		expect(hits("@duration:[600 TO 612]", json)).toBe(true);
		expect(hits("@duration:{600 TO 612}", json)).toBe(false);
		expect(hits("@duration:[600 TO 612}", json)).toBe(false);
		expect(hits("@duration:[* TO 700]", json)).toBe(true);
		expect(hits("@duration:[700 TO *]", json)).toBe(false);
	});

	test("access-line fields are attributes", () => {
		const text = '1.2.3.4 - - [x] "GET /api/x HTTP/1.1" 502 12';
		expect(hits("@status:[500 TO 599]", text)).toBe(true);
		expect(hits("@method:get", text)).toBe(true);
		expect(hits("@path:/api/*", text)).toBe(true);
	});

	test("unknown key with a group is read as an attribute", () => {
		expect(hits("user.id:(42 OR 7)", json)).toBe(true);
	});
});

describe("partial and empty queries", () => {
	test("empty and fully invalid queries match everything", () => {
		expect(matcher("")).toBe(matchAll);
		expect(hits("level:", "anything")).toBe(true);
		expect(hits("()", "anything")).toBe(true);
	});

	test("a broken piece is dropped, the rest filters", () => {
		expect(hits("timeout level:", "timeout")).toBe(true);
		expect(hits("timeout level:", "ok")).toBe(false);
		expect(hits("timeout OR", "timeout")).toBe(true);
	});
});

describe("highlights", () => {
	test("free text and attribute values are marked; negated and facet terms are not", () => {
		const { ast } = parseQuery(
			'"db down" @user:bob -skip level:error service:api',
		);
		const terms = highlightTerms(ast);
		expect(terms.map((t) => t.parts.join("*"))).toEqual(["db down", "bob"]);
	});

	test("findHighlights returns merged ranges", () => {
		const terms = highlightTerms(parseQuery("err error").ast);
		expect(findHighlights(terms, "an error here")).toEqual([[3, 8]]);
		expect(findHighlights(terms, "nothing")).toEqual([]);
	});

	test("every occurrence, case-insensitive through the lowered line", () => {
		const terms = highlightTerms(parseQuery("ab").ast);
		expect(findHighlights(terms, "ab xab ab")).toEqual([
			[0, 2],
			[4, 6],
			[7, 9],
		]);
	});

	test("wildcard terms mark their literal pieces", () => {
		const terms = highlightTerms(parseQuery("err*out").ast);
		expect(findHighlights(terms, "error timeout")).toEqual([
			[0, 3],
			[10, 13],
		]);
	});
});
