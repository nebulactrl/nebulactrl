import { describe, expect, test } from "bun:test";
import { parseLevel } from "./level";

const word = (line: string) => {
	const parsed = parseLevel(line);
	return parsed ? line.slice(parsed.start, parsed.end) : null;
};

describe("parseLevel", () => {
	test("reads a leading bracketed or bare upper-case level", () => {
		expect(parseLevel("[ERROR] boom")?.level).toBe("error");
		expect(parseLevel("WARN disk at 91%")?.level).toBe("warn");
		expect(parseLevel("INFO: listening on :8080")?.level).toBe("info");
		expect(word("[ERROR] boom")).toBe("ERROR");
	});

	test("reads a level after an ISO timestamp", () => {
		const line = "2025-01-02T03:04:05Z WARN slow query";
		expect(parseLevel(line)?.level).toBe("warn");
		expect(word(line)).toBe("WARN");
	});

	test("reads logfmt and JSON level keys", () => {
		expect(parseLevel('time=1 level=error msg="x"')?.level).toBe("error");
		expect(word('time=1 level=error msg="x"')).toBe("error");
		expect(parseLevel('{"ts":1,"level":"warn","msg":"x"}')?.level).toBe("warn");
		expect(word('{"ts":1,"level":"warn","msg":"x"}')).toBe("warn");
		expect(parseLevel("lvl=debug a=b")?.level).toBe("debug");
		expect(parseLevel("severity: CRITICAL down")?.level).toBe("error");
	});

	test("maps aliases onto the four levels", () => {
		expect(parseLevel("FATAL out of memory")?.level).toBe("error");
		expect(parseLevel("TRACE enter")?.level).toBe("debug");
		expect(parseLevel("level=warning x")?.level).toBe("warn");
	});

	test("reads a lower-case bracketed level, with or without a timestamp", () => {
		expect(parseLevel("[info] ready")?.level).toBe("info");
		expect(word("[warn] slow")).toBe("warn");
		expect(
			parseLevel("2025/01/02 03:04:05 [error] 29#29: *1 connect() failed")
				?.level,
		).toBe("error");
	});

	test("never guesses from free text", () => {
		expect(parseLevel("Error loading config")).toBeNull();
		expect(parseLevel("the error rate is low")).toBeNull();
		expect(parseLevel("GET /health 200 3ms")).toBeNull();
		expect(parseLevel("level=banana")).toBeNull();
		expect(parseLevel("")).toBeNull();
	});
});
