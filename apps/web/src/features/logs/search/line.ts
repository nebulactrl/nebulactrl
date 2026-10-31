// Turns one raw log line into the fields the search works on: what the API
// already says about it (time, service, stream) plus what the line itself
// states (a level, an HTTP status, JSON or logfmt fields). Nothing is guessed:
// a field is set only when the line says it.

import { type LogLevel, levelFromName, parseLevel } from "../level";
import type { FacetKey } from "./query";

export type Scalar = string | number | boolean;

/** What the log API gives for a line, whichever stream it came from. */
export interface LogInput {
	line: string;
	ts?: string;
	/** `build` | `deploy` | `runtime` for deployment logs, `stdout`/`stderr` when a source says so. */
	stream?: string;
	serviceName?: string;
	replica?: string | number;
	deploymentId?: string;
}

export interface ParsedLine {
	ts?: string;
	/** The line without colour escapes. Free text and highlights work on it. */
	text: string;
	/** `text` in lower case, for matching. */
	lower: string;
	level?: LogLevel;
	/** Where the level word sits in `text`, so the viewer can colour just it. */
	levelSpan?: { start: number; end: number };
	stream?: string;
	service?: string;
	replica?: string;
	/** HTTP status, from a JSON/logfmt field or an access-log line. */
	status?: number;
	deployment?: string;
	/** JSON or logfmt fields, plus `method`, `path` and `status` of an access line. */
	attrs: Record<string, unknown>;
	/** `attrs` flattened to dotted scalar paths, for counting. */
	flat: [string, Scalar][];
	format: "json" | "logfmt" | "text";
}

/** Strips ANSI colour escapes: they are noise in a browser log view. */
export function stripAnsi(text: string): string {
	// biome-ignore lint/suspicious/noControlCharactersInRegex: matching the ESC byte is the point.
	return text.replace(/\x1b\[[0-9;]*[A-Za-z]/g, "");
}

/** A line with nothing visible once colour escapes and whitespace are gone. */
export function isBlankLine(text: string): boolean {
	return stripAnsi(text).trim() === "";
}

const LOGFMT_PAIR = /(?:^|\s)([A-Za-z_][\w.-]*)=("(?:[^"\\]|\\.)*"|[^\s"]*)/g;

function parseLogfmt(text: string): Record<string, unknown> | null {
	const attrs: Record<string, unknown> = {};
	let pairs = 0;
	LOGFMT_PAIR.lastIndex = 0;
	for (let m = LOGFMT_PAIR.exec(text); m; m = LOGFMT_PAIR.exec(text)) {
		const key = m[1] as string;
		let value = m[2] as string;
		if (value.startsWith('"') && value.endsWith('"') && value.length >= 2) {
			value = value.slice(1, -1).replace(/\\(.)/g, "$1");
		}
		attrs[key] = value;
		pairs += 1;
	}
	return pairs >= 2 ? attrs : null;
}

function parseJson(text: string): Record<string, unknown> | null {
	const trimmed = text.trim();
	if (!trimmed.startsWith("{") || !trimmed.endsWith("}")) return null;
	try {
		const value: unknown = JSON.parse(trimmed);
		if (typeof value === "object" && value !== null && !Array.isArray(value)) {
			return value as Record<string, unknown>;
		}
	} catch {
		// Not JSON after all: plain text.
	}
	return null;
}

/** An attribute by path: an exact key first (`http.status` in logfmt), then nested objects. */
export function getAttr(attrs: Record<string, unknown>, path: string): unknown {
	if (Object.hasOwn(attrs, path)) return attrs[path];
	let current: unknown = attrs;
	for (const step of path.split(".")) {
		if (
			typeof current !== "object" ||
			current === null ||
			Array.isArray(current) ||
			!Object.hasOwn(current, step)
		) {
			return undefined;
		}
		current = (current as Record<string, unknown>)[step];
	}
	return current;
}

const MAX_DEPTH = 3;
const MAX_FLAT = 64;
const MAX_VALUE_LENGTH = 200;

function flatten(
	value: unknown,
	path: string,
	depth: number,
	out: [string, Scalar][],
) {
	if (out.length >= MAX_FLAT) return;
	if (typeof value === "string") {
		if (value.length <= MAX_VALUE_LENGTH) out.push([path, value]);
	} else if (typeof value === "number" || typeof value === "boolean") {
		out.push([path, value]);
	} else if (
		typeof value === "object" &&
		value !== null &&
		!Array.isArray(value) &&
		depth < MAX_DEPTH
	) {
		for (const [key, child] of Object.entries(value)) {
			flatten(child, path === "" ? key : `${path}.${key}`, depth + 1, out);
		}
	}
}

const LEVEL_KEYS = [
	"level",
	"severity",
	"lvl",
	"levelname",
	"loglevel",
	"log.level",
];
const TIME_KEYS = ["time", "ts", "timestamp", "@timestamp", "t"];
const STATUS_KEYS = [
	"status",
	"status_code",
	"statusCode",
	"http.status_code",
	"http_status",
	"response.status",
];
const REPLICA_KEYS = ["replica", "replica_index", "replicaIndex"];
const DEPLOYMENT_KEYS = ["deployment", "deployment_id", "deploymentId"];

function firstAttr(attrs: Record<string, unknown>, keys: string[]): unknown {
	for (const key of keys) {
		const value = getAttr(attrs, key);
		if (value !== undefined && value !== null && value !== "") return value;
	}
	return undefined;
}

// pino and bunyan write numbers: 10 trace, 20 debug, 30 info, 40 warn, 50 error, 60 fatal.
function levelOfValue(value: unknown): LogLevel | undefined {
	if (typeof value === "string") return levelFromName(value);
	if (typeof value === "number") {
		if (value <= 20) return "debug";
		if (value <= 30) return "info";
		if (value <= 40) return "warn";
		return "error";
	}
	return undefined;
}

function statusOfValue(value: unknown): number | undefined {
	const n = typeof value === "string" ? Number(value) : value;
	return typeof n === "number" && Number.isInteger(n) && n >= 100 && n <= 599
		? n
		: undefined;
}

const METHODS = "GET|HEAD|POST|PUT|PATCH|DELETE|OPTIONS|CONNECT|TRACE";
// nginx, traefik, caddy, uvicorn: ... "GET /path HTTP/1.1" 200 1234
const COMBINED = new RegExp(
	`"(${METHODS})\\s+(\\S+)(?:\\s+HTTP/[\\d.]+)?"\\s+(\\d{3})\\b`,
);
// gin: | 200 |  1.2ms | 127.0.0.1 | GET "/path"
const GIN = new RegExp(
	`\\|\\s*(\\d{3})\\s*\\|[^|]*\\|[^|]*\\|\\s*(${METHODS})\\s+"?([^"\\s]+)`,
);
// GET /path 200 3ms, optionally after a timestamp word
const BARE = new RegExp(
	`^\\s*(?:\\S+\\s+)?(${METHODS})\\s+(\\S+)\\s+(\\d{3})\\b`,
);

function accessLine(
	text: string,
): { method: string; path: string; status: number } | null {
	const combined = COMBINED.exec(text);
	if (combined) {
		return {
			method: combined[1] as string,
			path: combined[2] as string,
			status: Number(combined[3]),
		};
	}
	const gin = GIN.exec(text);
	if (gin) {
		return {
			method: gin[2] as string,
			path: gin[3] as string,
			status: Number(gin[1]),
		};
	}
	const bare = BARE.exec(text);
	if (bare) {
		return {
			method: bare[1] as string,
			path: bare[2] as string,
			status: Number(bare[3]),
		};
	}
	return null;
}

function asText(value: unknown): string | undefined {
	if (typeof value === "string") return value;
	if (typeof value === "number") return String(value);
	return undefined;
}

export function parseLine(input: LogInput): ParsedLine {
	const text = stripAnsi(input.line);
	const json = parseJson(text);
	const logfmt = json ? null : parseLogfmt(text);
	const attrs = json ?? logfmt ?? {};

	const span = parseLevel(text);
	const level = levelOfValue(firstAttr(attrs, LEVEL_KEYS)) ?? span?.level;

	let status = statusOfValue(firstAttr(attrs, STATUS_KEYS));
	if (status === undefined) {
		const access = accessLine(text);
		if (access && access.status >= 100 && access.status <= 599) {
			status = access.status;
			attrs.status ??= access.status;
			attrs.method ??= access.method;
			attrs.path ??= access.path;
		}
	}

	const ts = input.ts ?? asText(firstAttr(attrs, TIME_KEYS));
	const replica = asText(input.replica ?? firstAttr(attrs, REPLICA_KEYS));
	const deployment =
		input.deploymentId ?? asText(firstAttr(attrs, DEPLOYMENT_KEYS));
	const stream = input.stream ?? asText(getAttr(attrs, "stream"));

	const flat: [string, Scalar][] = [];
	flatten(attrs, "", 0, flat);

	const parsed: ParsedLine = {
		text,
		lower: text.toLowerCase(),
		attrs,
		flat,
		format: json ? "json" : logfmt ? "logfmt" : "text",
	};
	if (ts) parsed.ts = ts;
	if (level) parsed.level = level;
	if (span && span.level === level) {
		parsed.levelSpan = { start: span.start, end: span.end };
	}
	if (stream) parsed.stream = stream;
	if (input.serviceName) parsed.service = input.serviceName;
	if (replica) parsed.replica = replica;
	if (status !== undefined) parsed.status = status;
	if (deployment) parsed.deployment = deployment;
	return parsed;
}

const cache = new WeakMap<object, ParsedLine>();

/** `parseLine`, remembered per line object: the buffer and the viewer share one parse. */
export function parsedFor<T extends LogInput & object>(input: T): ParsedLine {
	let parsed = cache.get(input);
	if (!parsed) {
		parsed = parseLine(input);
		cache.set(input, parsed);
	}
	return parsed;
}

/** The built-in facet of a line as a string. */
export function facetValue(
	line: ParsedLine,
	key: FacetKey,
): string | undefined {
	if (key === "status") {
		return line.status === undefined ? undefined : String(line.status);
	}
	return line[key];
}
