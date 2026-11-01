import { describe, expect, test } from "bun:test";
import { type BufferLine, LogBuffer } from "./buffer";
import { compileQuery } from "./match";
import { parseQuery } from "./query";

const mk = (id: number, line: string, serviceName = "api"): BufferLine => ({
	id,
	line,
	serviceName,
});
const matcher = (query: string) => compileQuery(parseQuery(query).ast);
const ids = (lines: readonly BufferLine[]) => lines.map((l) => l.id);

describe("LogBuffer", () => {
	test("takes only the new lines on each sync", () => {
		const buffer = new LogBuffer<BufferLine>();
		buffer.setMatcher(matcher("error"));
		const a = mk(1, "error one");
		const b = mk(2, "fine");
		buffer.sync([a, b]);
		expect(ids(buffer.visible)).toEqual([1]);
		expect(buffer.stats.lines).toBe(2);
		buffer.sync([a, b, mk(3, "another error")]);
		expect(ids(buffer.visible)).toEqual([1, 3]);
		expect(buffer.stats.lines).toBe(3);
		expect(buffer.total).toBe(3);
	});

	test("an unchanged stream changes nothing and keeps the array", () => {
		const buffer = new LogBuffer<BufferLine>();
		const lines = [mk(1, "a"), mk(2, "b")];
		buffer.sync(lines);
		const before = buffer.visible;
		expect(buffer.sync(lines)).toBe(false);
		expect(buffer.visible).toBe(before);
	});

	test("a new query re-matches the buffer", () => {
		const buffer = new LogBuffer<BufferLine>();
		buffer.sync([mk(1, "ERROR x"), mk(2, "INFO y")]);
		expect(buffer.visible).toHaveLength(2);
		buffer.setMatcher(matcher("level:error"));
		expect(ids(buffer.visible)).toEqual([1]);
		buffer.setMatcher(matcher(""));
		expect(ids(buffer.visible)).toEqual([1, 2]);
	});

	test("lines that the stream dropped are uncounted", () => {
		const buffer = new LogBuffer<BufferLine>();
		buffer.setMatcher(matcher("error"));
		const lines = [mk(1, "ERROR a"), mk(2, "ERROR b"), mk(3, "ok")];
		buffer.sync(lines);
		buffer.sync([
			lines[1] as BufferLine,
			lines[2] as BufferLine,
			mk(4, "ERROR c"),
		]);
		expect(ids(buffer.visible)).toEqual([2, 4]);
		expect(buffer.stats.facets.level.get("error")).toBe(2);
		expect(buffer.total).toBe(3);
	});

	test("the buffer is capped at its size, oldest first", () => {
		const buffer = new LogBuffer<BufferLine>(3);
		const lines = [1, 2, 3, 4, 5].map((n) => mk(n, `ERROR ${n}`));
		buffer.sync(lines);
		expect(buffer.total).toBe(3);
		expect(ids(buffer.visible)).toEqual([3, 4, 5]);
		expect(buffer.stats.lines).toBe(3);
		expect(buffer.stats.facets.level.get("error")).toBe(3);
	});

	test("clearing the stream clears the buffer", () => {
		const buffer = new LogBuffer<BufferLine>();
		buffer.sync([mk(1, "ERROR a")]);
		expect(buffer.sync([])).toBe(true);
		expect(buffer.visible).toEqual([]);
		expect(buffer.stats.lines).toBe(0);
		expect(buffer.sync([])).toBe(false);
	});

	test("a restarted stream (ids start over) is ingested afresh", () => {
		const buffer = new LogBuffer<BufferLine>();
		buffer.sync([mk(1, "a"), mk(2, "b"), mk(3, "c")]);
		buffer.sync([mk(1, "x")]);
		expect(buffer.total).toBe(1);
		expect(buffer.visible[0]?.line).toBe("x");
	});

	test("blank lines can be left out", () => {
		const buffer = new LogBuffer<BufferLine>(5000, true);
		buffer.sync([mk(1, "a"), mk(2, "   "), mk(3, "\u001b[0m"), mk(4, "b")]);
		expect(ids(buffer.visible)).toEqual([1, 4]);
		expect(buffer.total).toBe(2);
	});

	test("facet counts follow the buffer, not the query", () => {
		const buffer = new LogBuffer<BufferLine>();
		buffer.setMatcher(matcher("service:nope"));
		buffer.sync([mk(1, "ERROR a", "api"), mk(2, "INFO b", "web")]);
		expect(buffer.visible).toHaveLength(0);
		expect(buffer.stats.facets.service.get("api")).toBe(1);
		expect(buffer.stats.facets.level.get("info")).toBe(1);
	});

	test("lines added in front of the ones seen are taken, not skipped", () => {
		const buffer = new LogBuffer<BufferLine>();
		buffer.setMatcher(matcher("error"));
		const newer = [mk(30, "ERROR c"), mk(40, "fine")];
		buffer.sync(newer);
		expect(ids(buffer.visible)).toEqual([30]);
		buffer.sync([mk(10, "ERROR a"), mk(20, "ERROR b"), ...newer]);
		expect(ids(buffer.visible)).toEqual([10, 20, 30]);
		expect(buffer.total).toBe(4);
		expect(buffer.stats.lines).toBe(4);
	});

	test("matching 5,000 lines incrementally stays fast", () => {
		const buffer = new LogBuffer<BufferLine>();
		buffer.setMatcher(matcher("level:error timeout -service:web"));
		const lines: BufferLine[] = [];
		for (let i = 1; i <= 5000; i++) lines.push(mk(i, `ERROR timeout ${i}`));
		const start = performance.now();
		buffer.sync(lines);
		lines.push(mk(5001, "ERROR timeout last"));
		buffer.sync(lines);
		expect(performance.now() - start).toBeLessThan(500);
		expect(buffer.total).toBe(5000);
	});
});
