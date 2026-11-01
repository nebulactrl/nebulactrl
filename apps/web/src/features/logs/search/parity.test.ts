import { describe, expect, test } from "bun:test";
import { readFileSync } from "node:fs";
import { join } from "node:path";
import { type LogInput, parseLine } from "./line";
import { compileQuery } from "./match";
import { parseQuery } from "./query";

// tests/fixtures/logquery/cases.json is shared with the Go port of the search
// language (internal/agent/logstore/query/parity_test.go). Both implementations
// must reproduce it, so neither can drift from the other.

interface Fixture {
	lines: (LogInput & { id: string })[];
	cases: { query: string; matches: string[]; errors: string[] }[];
}

const fixture: Fixture = JSON.parse(
	readFileSync(
		join(
			import.meta.dir,
			"../../../../../../tests/fixtures/logquery/cases.json",
		),
		"utf8",
	),
);

const parsed = fixture.lines.map((line) => ({
	id: line.id,
	line: parseLine(line),
}));

describe("log query parity fixture", () => {
	test("covers enough lines and queries", () => {
		expect(fixture.lines.length).toBeGreaterThanOrEqual(60);
		expect(fixture.cases.length).toBeGreaterThanOrEqual(150);
	});

	for (const { query, matches, errors } of fixture.cases) {
		test(JSON.stringify(query), () => {
			const result = parseQuery(query);
			const match = compileQuery(result.ast);
			expect(result.errors.map((error) => error.message)).toEqual(errors);
			expect(parsed.filter((p) => match(p.line)).map((p) => p.id)).toEqual(
				matches,
			);
		});
	}
});
