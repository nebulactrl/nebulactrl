import { describe, expect, test } from "bun:test";
import {
	FacetStats,
	facetTermState,
	formatValue,
	toggleFacetTerm,
	topValues,
} from "./facets";
import { parseLine } from "./line";

const line = (text: string, serviceName?: string) =>
	parseLine(serviceName ? { line: text, serviceName } : { line: text });

describe("FacetStats", () => {
	test("counts built-in facets as lines arrive", () => {
		const stats = new FacetStats();
		stats.add(line("ERROR a", "api"));
		stats.add(line("ERROR b", "api"));
		stats.add(line("INFO c", "web"));
		stats.add(line("no level", "web"));
		expect(stats.lines).toBe(4);
		expect([...stats.facets.level]).toEqual([
			["error", 2],
			["info", 1],
		]);
		expect([...stats.facets.service]).toEqual([
			["api", 2],
			["web", 2],
		]);
	});

	test("uncounts a line that leaves the buffer", () => {
		const stats = new FacetStats();
		const a = line("ERROR a", "api");
		const b = line("ERROR b", "api");
		stats.add(a);
		stats.add(b);
		stats.remove(a);
		expect(stats.facets.level.get("error")).toBe(1);
		stats.remove(b);
		expect(stats.facets.level.size).toBe(0);
		expect(stats.facets.service.size).toBe(0);
		expect(stats.lines).toBe(0);
	});

	test("status codes of access lines", () => {
		const stats = new FacetStats();
		stats.add(line('1.1.1.1 - - [x] "GET /a HTTP/1.1" 200 1'));
		stats.add(line('1.1.1.1 - - [x] "GET /b HTTP/1.1" 200 1'));
		stats.add(line('1.1.1.1 - - [x] "GET /c HTTP/1.1" 404 1'));
		expect(topValues(stats.facets.status)).toEqual([
			{ value: "200", count: 2 },
			{ value: "404", count: 1 },
		]);
	});

	test("attribute paths with counts, numeric share and distinct values", () => {
		const stats = new FacetStats();
		stats.add(line('{"user":{"id":1},"duration":5}'));
		stats.add(line('{"user":{"id":1},"duration":7}'));
		stats.add(line('{"user":{"id":2}}'));
		const user = stats.attrs.get("user.id");
		expect(user?.count).toBe(3);
		expect(user?.numeric).toBe(3);
		expect(user?.values.get("1")).toBe(2);
		expect(stats.attrs.get("duration")?.count).toBe(2);
		expect(stats.attrs.get("user")).toBeUndefined();
	});

	test("removing lines removes attribute paths nobody has any more", () => {
		const stats = new FacetStats();
		const a = line('{"only":"x"}');
		stats.add(a);
		stats.remove(a);
		expect(stats.attrs.size).toBe(0);
	});

	test("version moves on every change", () => {
		const stats = new FacetStats();
		const before = stats.version;
		stats.add(line("x"));
		expect(stats.version).toBeGreaterThan(before);
	});

	test("reset empties everything", () => {
		const stats = new FacetStats();
		stats.add(line("ERROR x", "api"));
		stats.reset();
		expect(stats.lines).toBe(0);
		expect(stats.facets.level.size).toBe(0);
	});
});

describe("topValues", () => {
	test("most frequent first, ties by name with numbers in order", () => {
		const map = new Map([
			["b", 1],
			["a", 1],
			["10", 3],
			["9", 3],
		]);
		expect(topValues(map).map((v) => v.value)).toEqual(["9", "10", "a", "b"]);
		expect(topValues(map, 2)).toHaveLength(2);
	});
});

describe("formatValue", () => {
	test("plain words stay bare, anything else is quoted and escaped", () => {
		expect(formatValue("api-prod")).toBe("api-prod");
		expect(formatValue("my svc")).toBe('"my svc"');
		expect(formatValue('say "hi"')).toBe('"say \\"hi\\""');
		expect(formatValue("a*b")).toBe('"a*b"');
		expect(formatValue("OR")).toBe('"OR"');
		expect(formatValue("-x")).toBe('"-x"');
	});
});

describe("toggleFacetTerm", () => {
	test("include appends an AND term", () => {
		expect(toggleFacetTerm("", "level", "error", "include")).toBe(
			"level:error",
		);
		expect(toggleFacetTerm("timeout", "level", "error", "include")).toBe(
			"timeout level:error",
		);
	});

	test("exclude appends a negated term", () => {
		expect(toggleFacetTerm("timeout", "service", "web", "exclude")).toBe(
			"timeout -service:web",
		);
	});

	test("clicking the active choice again removes it", () => {
		expect(toggleFacetTerm("level:error", "level", "error", "include")).toBe(
			"",
		);
		expect(
			toggleFacetTerm("a -level:error b", "level", "error", "exclude"),
		).toBe("a b");
		expect(
			toggleFacetTerm("a NOT level:error", "level", "error", "exclude"),
		).toBe("a");
	});

	test("the other choice swaps it", () => {
		expect(toggleFacetTerm("a level:error", "level", "error", "exclude")).toBe(
			"a -level:error",
		);
		expect(toggleFacetTerm("-level:error a", "level", "error", "include")).toBe(
			"a level:error",
		);
	});

	test("values with spaces are quoted and found again", () => {
		const once = toggleFacetTerm("", "service", "my svc", "include");
		expect(once).toBe('service:"my svc"');
		expect(facetTermState(once, "service", "my svc")).toBe("include");
		expect(toggleFacetTerm(once, "service", "my svc", "include")).toBe("");
	});

	test("a top-level OR is wrapped so the term narrows all of it", () => {
		expect(toggleFacetTerm("a OR b", "level", "error", "include")).toBe(
			"(a OR b) level:error",
		);
		expect(toggleFacetTerm("(a OR b)", "level", "error", "include")).toBe(
			"(a OR b) level:error",
		);
	});

	test("other values of the same facet are untouched", () => {
		expect(toggleFacetTerm("level:error", "level", "warn", "include")).toBe(
			"level:error level:warn",
		);
	});
});

describe("facetTermState", () => {
	test("reads include, exclude and none", () => {
		expect(facetTermState("level:error", "level", "error")).toBe("include");
		expect(facetTermState("-level:error", "level", "ERROR")).toBe("exclude");
		expect(facetTermState("NOT level:error", "level", "error")).toBe("exclude");
		expect(facetTermState("level:warn", "level", "error")).toBeNull();
		expect(facetTermState("", "level", "error")).toBeNull();
	});

	test("terms inside parentheses or next to OR are not top-level choices", () => {
		expect(facetTermState("(level:error OR x)", "level", "error")).toBeNull();
		expect(facetTermState("level:error OR x", "level", "error")).toBeNull();
	});

	test("a range or comparison is not an equality choice", () => {
		expect(facetTermState("status:>500", "status", "500")).toBeNull();
	});
});
