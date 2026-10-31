import { describe, expect, test } from "bun:test";
import {
	facetValue,
	getAttr,
	isBlankLine,
	parsedFor,
	parseLine,
	stripAnsi,
} from "./line";

const parse = (line: string) => parseLine({ line });

describe("parseLine: JSON", () => {
	const line = parse(
		'{"time":"2025-01-02T03:04:05Z","level":"warn","msg":"slow","status":502,"replica":2,"http":{"method":"GET","path":"/x"},"tags":["a","b"]}',
	);

	test("reads level, status, replica and time from fields", () => {
		expect(line.format).toBe("json");
		expect(line.level).toBe("warn");
		expect(line.status).toBe(502);
		expect(line.replica).toBe("2");
		expect(line.ts).toBe("2025-01-02T03:04:05Z");
	});

	test("exposes nested fields as @paths", () => {
		expect(getAttr(line.attrs, "http.method")).toBe("GET");
		expect(getAttr(line.attrs, "tags")).toEqual(["a", "b"]);
		expect(getAttr(line.attrs, "http.nope")).toBeUndefined();
		expect(getAttr(line.attrs, "constructor")).toBeUndefined();
	});

	test("flattens scalars for counting", () => {
		const paths = line.flat.map(([path]) => path);
		expect(paths).toContain("http.method");
		expect(paths).toContain("status");
		expect(paths).not.toContain("tags");
	});

	test("pino's numeric levels", () => {
		expect(parse('{"level":30,"msg":"a"}').level).toBe("info");
		expect(parse('{"level":50,"msg":"a"}').level).toBe("error");
		expect(parse('{"level":20,"msg":"a"}').level).toBe("debug");
	});

	test("severity and lvl are levels too; a broken JSON line is text", () => {
		expect(parse('{"severity":"ERROR"}').level).toBe("error");
		expect(parse('{"lvl":"debug"}').level).toBe("debug");
		expect(parse('{"level":"warn"').format).toBe("text");
	});
});

describe("parseLine: logfmt", () => {
	test("reads pairs, quoted values and the level key", () => {
		const line = parse(
			'time=2025-01-02T03:04:05Z level=error msg="db is down" duration=512 deployment=dep-1',
		);
		expect(line.format).toBe("logfmt");
		expect(line.level).toBe("error");
		expect(line.attrs.msg).toBe("db is down");
		expect(line.attrs.duration).toBe("512");
		expect(line.deployment).toBe("dep-1");
		expect(line.ts).toBe("2025-01-02T03:04:05Z");
	});

	test("status in logfmt is the status facet", () => {
		expect(parse("method=GET path=/x status=404").status).toBe(404);
	});

	test("one pair is not logfmt", () => {
		expect(parse("retry=3").format).toBe("text");
		expect(parse("GET /?a=1&b=2").format).toBe("text");
	});
});

describe("parseLine: level prefixes", () => {
	test("ERROR, WARN and INFO words", () => {
		expect(parse("ERROR boom").level).toBe("error");
		expect(parse("WARN disk at 91%").level).toBe("warn");
		expect(parse("INFO: listening on :8080").level).toBe("info");
	});

	test("bracketed, lower case, with or without a timestamp", () => {
		expect(parse("[warn] slow").level).toBe("warn");
		expect(parse("[info] ready").level).toBe("info");
		expect(
			parse("2025/01/02 03:04:05 [error] 29#29: *1 connect() failed").level,
		).toBe("error");
		expect(parse("2025-01-02T03:04:05Z DEBUG tick").level).toBe("debug");
	});

	test("remembers where the level word is", () => {
		const line = parse("[ERROR] boom");
		expect(line.levelSpan).toEqual({ start: 1, end: 6 });
	});

	test("never guesses from prose", () => {
		expect(parse("Error loading config").level).toBeUndefined();
		expect(parse("the error rate is low").level).toBeUndefined();
		expect(parse("listening on :8080").level).toBeUndefined();
	});
});

describe("parseLine: access logs", () => {
	test("nginx / combined format", () => {
		const line = parse(
			'127.0.0.1 - - [01/Jan/2025:00:00:00 +0000] "GET /api/users?id=1 HTTP/1.1" 502 157 "-" "curl/8"',
		);
		expect(line.status).toBe(502);
		expect(line.attrs.method).toBe("GET");
		expect(line.attrs.path).toBe("/api/users?id=1");
		expect(line.level).toBeUndefined();
	});

	test("traefik access line", () => {
		const line = parse(
			'172.18.0.1 - - [01/Jan/2025:00:00:00 +0000] "POST /v1/x HTTP/2.0" 201 12 "-" "-" 4 "web@docker" "http://172.18.0.5:80" 3ms',
		);
		expect(line.status).toBe(201);
		expect(line.attrs.method).toBe("POST");
	});

	test("gin's table", () => {
		const line = parse(
			'[GIN] 2025/01/02 - 03:04:05 | 404 |    1.2ms |  127.0.0.1 | GET      "/nope"',
		);
		expect(line.status).toBe(404);
		expect(line.attrs.path).toBe("/nope");
	});

	test("a bare method path status line", () => {
		expect(parse("GET /health 200 3ms").status).toBe(200);
	});

	test("ordinary lines have no status", () => {
		expect(parse("listening on :8080").status).toBeUndefined();
		expect(parse("took 200 ms").status).toBeUndefined();
		expect(parse('GET "/x" HTTP/1.1 999').status).toBeUndefined();
	});
});

describe("parseLine: fields from the API", () => {
	test("service, stream, replica and deployment come from the line's metadata", () => {
		const line = parseLine({
			line: "hello",
			ts: "2025-01-01T00:00:00Z",
			serviceName: "api",
			stream: "runtime",
			replica: 1,
			deploymentId: "dep-9",
		});
		expect(line.service).toBe("api");
		expect(line.stream).toBe("runtime");
		expect(line.replica).toBe("1");
		expect(line.deployment).toBe("dep-9");
		expect(facetValue(line, "service")).toBe("api");
		expect(facetValue(line, "level")).toBeUndefined();
	});

	test("metadata wins over a field of the same name in the line", () => {
		const line = parseLine({
			line: '{"stream":"stderr","replica":3}',
			stream: "runtime",
			replica: 0,
		});
		expect(line.stream).toBe("runtime");
		expect(line.replica).toBe("0");
	});

	test("a stream or replica the line states is used when the API says nothing", () => {
		const line = parse('{"stream":"stderr","replica":3}');
		expect(line.stream).toBe("stderr");
		expect(line.replica).toBe("3");
	});
});

describe("helpers", () => {
	test("colour escapes are stripped from the text", () => {
		const line = parse("\u001b[31mERROR\u001b[0m boom");
		expect(line.text).toBe("ERROR boom");
		expect(line.level).toBe("error");
		expect(stripAnsi("\u001b[1mx\u001b[0m")).toBe("x");
	});

	test("blank lines", () => {
		expect(isBlankLine("  \u001b[0m ")).toBe(true);
		expect(isBlankLine("x")).toBe(false);
	});

	test("parsedFor remembers the parse per line object", () => {
		const input = { line: "ERROR x" };
		expect(parsedFor(input)).toBe(parsedFor(input));
		expect(parsedFor({ line: "ERROR x" })).not.toBe(parsedFor(input));
	});
});
