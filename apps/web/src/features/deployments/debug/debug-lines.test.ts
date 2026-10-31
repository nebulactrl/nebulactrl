import { describe, expect, test } from "bun:test";
import type { LogLine } from "#/api/sse";
import {
	filterDebugLines,
	highlightWords,
	levelOf,
	NO_FILTER,
	searchWords,
	streamCounts,
	toggled,
} from "./debug-lines";

let n = 0;
function line(text: string, stream = "debug"): LogLine {
	n += 1;
	return { id: n, line: text, stream };
}

const lines = [
	line("command abc: deploy web", "debug"),
	line(
		"WARN health nc-web-1: probe GET http://10.0.0.2:80/: HTTP 503 in 4ms",
		"debug",
	),
	line(
		"health nc-web-1: probe GET http://10.0.0.2:80/: HTTP 200 in 2ms (pass)",
		"debug",
	),
	line("[pull] Pulling nginx:1", "deploy"),
	line("Step 1/4 : FROM node", "build"),
	line('ERROR abort: "boom"', "debug"),
];

describe("levelOf", () => {
	test("reads a stated level", () => {
		expect(levelOf({ line: "WARN x" })).toBe("warn");
		expect(levelOf({ line: "ERROR x" })).toBe("error");
	});
	test("a line that states none is info", () => {
		expect(levelOf({ line: "plain text with error inside" })).toBe("info");
	});
});

describe("filterDebugLines", () => {
	test("no filter keeps everything", () => {
		expect(filterDebugLines(lines, NO_FILTER)).toHaveLength(lines.length);
	});

	test("by stream", () => {
		const out = filterDebugLines(lines, {
			...NO_FILTER,
			streams: new Set(["debug"]),
		});
		expect(out.map((l) => l.stream)).toEqual([
			"debug",
			"debug",
			"debug",
			"debug",
		]);
	});

	test("by level", () => {
		const out = filterDebugLines(lines, {
			...NO_FILTER,
			levels: new Set(["warn", "error"]),
		});
		expect(out).toHaveLength(2);
	});

	test("search needs every word, in any case", () => {
		const out = filterDebugLines(lines, {
			...NO_FILTER,
			query: "PROBE http 503",
		});
		expect(out).toHaveLength(1);
		expect(out[0]?.line).toContain("503");
	});

	test("a blank query is no query", () => {
		expect(
			filterDebugLines(lines, { ...NO_FILTER, query: "   " }),
		).toHaveLength(lines.length);
	});

	test("nothing selected shows nothing", () => {
		expect(
			filterDebugLines(lines, { ...NO_FILTER, streams: new Set() }),
		).toEqual([]);
	});

	test("a line without a stream is not dropped by the stream filter", () => {
		const bare: LogLine = { id: 999, line: "x" };
		expect(
			filterDebugLines([bare], { ...NO_FILTER, streams: new Set(["debug"]) }),
		).toEqual([bare]);
	});
});

describe("helpers", () => {
	test("streamCounts", () => {
		expect(streamCounts(lines)).toEqual({ debug: 4, deploy: 1, build: 1 });
	});
	test("search words", () => {
		expect(searchWords("  Foo  BAR ")).toEqual(["foo", "bar"]);
		expect(highlightWords("a b")).toEqual([{ parts: ["a"] }, { parts: ["b"] }]);
	});
	test("toggled returns a new set", () => {
		const a = new Set(["x"]);
		const b = toggled(a, "y");
		expect([...a]).toEqual(["x"]);
		expect([...b].sort()).toEqual(["x", "y"]);
		expect([...toggled(b, "x")]).toEqual(["y"]);
	});
});
