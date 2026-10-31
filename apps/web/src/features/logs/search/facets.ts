// Facet counts over the log buffer, kept incrementally (a line is counted when
// it enters the buffer and uncounted when it leaves), and the helpers that turn
// an include/exclude click into an edit of the query text.

import { facetValue, type ParsedLine } from "./line";
import { FACET_KEYS, type FacetKey, tokenize } from "./query";

const MAX_ATTR_PATHS = 200;
const MAX_ATTR_VALUES = 100;
const MAX_VALUE_LENGTH = 80;

/** What the buffer holds under one `@attribute` path. */
export interface AttrStat {
	/** Lines that have it. */
	count: number;
	/** Lines where it reads as a number. */
	numeric: number;
	/** Distinct values with counts, capped at 100 distinct. */
	values: Map<string, number>;
}

function bump(map: Map<string, number>, key: string, by: 1 | -1) {
	const next = (map.get(key) ?? 0) + by;
	if (next <= 0) map.delete(key);
	else map.set(key, next);
}

function isNumeric(value: string | number | boolean): boolean {
	if (typeof value === "number") return true;
	return (
		typeof value === "string" && value !== "" && !Number.isNaN(Number(value))
	);
}

export class FacetStats {
	readonly facets: Record<FacetKey, Map<string, number>> = {
		level: new Map(),
		service: new Map(),
		stream: new Map(),
		replica: new Map(),
		status: new Map(),
		deployment: new Map(),
	};
	readonly attrs = new Map<string, AttrStat>();
	lines = 0;
	/** Bumped on every change, so a memo can depend on it. */
	version = 0;

	add(line: ParsedLine) {
		this.lines += 1;
		this.version += 1;
		for (const key of FACET_KEYS) {
			const value = facetValue(line, key);
			if (value !== undefined) bump(this.facets[key], value, 1);
		}
		for (const [path, value] of line.flat) {
			let stat = this.attrs.get(path);
			if (!stat) {
				if (this.attrs.size >= MAX_ATTR_PATHS) continue;
				stat = { count: 0, numeric: 0, values: new Map() };
				this.attrs.set(path, stat);
			}
			stat.count += 1;
			if (isNumeric(value)) stat.numeric += 1;
			const text = String(value);
			if (
				text.length <= MAX_VALUE_LENGTH &&
				(stat.values.has(text) || stat.values.size < MAX_ATTR_VALUES)
			) {
				bump(stat.values, text, 1);
			}
		}
	}

	remove(line: ParsedLine) {
		this.lines -= 1;
		this.version += 1;
		for (const key of FACET_KEYS) {
			const value = facetValue(line, key);
			if (value !== undefined) bump(this.facets[key], value, -1);
		}
		for (const [path, value] of line.flat) {
			const stat = this.attrs.get(path);
			if (!stat) continue;
			stat.count -= 1;
			if (isNumeric(value)) stat.numeric -= 1;
			const text = String(value);
			if (stat.values.has(text)) bump(stat.values, text, -1);
			if (stat.count <= 0) this.attrs.delete(path);
		}
	}

	reset() {
		for (const key of FACET_KEYS) this.facets[key].clear();
		this.attrs.clear();
		this.lines = 0;
		this.version += 1;
	}
}

export interface FacetValue {
	value: string;
	count: number;
}

/** Values by count, most frequent first; ties alphabetical (status codes numerically). */
export function topValues(
	map: ReadonlyMap<string, number>,
	limit = Number.POSITIVE_INFINITY,
): FacetValue[] {
	const out = [...map].map(([value, count]) => ({ value, count }));
	out.sort(
		(a, b) =>
			b.count - a.count ||
			a.value.localeCompare(b.value, undefined, { numeric: true }),
	);
	return out.length > limit ? out.slice(0, limit) : out;
}

// --- Editing the query from a facet click -------------------------------------

export type FacetMode = "include" | "exclude";

const WORD_VALUE = /^[A-Za-z0-9_./:@][A-Za-z0-9_./:@-]*$/;
const KEYWORD_VALUE = /^(?:OR|AND|NOT)$/;

/** A value as it is written in a query: quoted when it holds spaces or characters with a meaning. */
export function formatValue(value: string): string {
	if (WORD_VALUE.test(value) && !KEYWORD_VALUE.test(value)) return value;
	return `"${value.replace(/[\\"]/g, "\\$&")}"`;
}

interface FoundTerm {
	/** The term including its `-` or `NOT`, as offsets into the query. */
	start: number;
	end: number;
	negated: boolean;
}

function findTerm(query: string, key: string, value: string): FoundTerm | null {
	const tokens = tokenize(query);
	const wanted = value.toLowerCase();
	let depth = 0;
	for (let i = 0; i < tokens.length; i++) {
		const token = tokens[i];
		if (!token) continue;
		if (token.kind === "lparen") depth += 1;
		else if (token.kind === "rparen") depth -= 1;
		if (token.kind !== "key" || depth !== 0) continue;
		if (query.slice(token.start, token.end - 1) !== key) continue;
		const next = tokens[i + 1];
		if (
			next?.kind !== "value" ||
			next.start !== token.end ||
			next.op ||
			next.text.toLowerCase() !== wanted
		) {
			continue;
		}
		const before = tokens[i - 1];
		const negated =
			before !== undefined &&
			((before.kind === "minus" && before.end === token.start) ||
				before.kind === "not");
		const neighbour = negated ? tokens[i - 2] : before;
		const after = tokens[i + 2];
		if (neighbour?.kind === "or" || after?.kind === "or") continue;
		return {
			start: negated && before ? before.start : token.start,
			end: next.end,
			negated,
		};
	}
	return null;
}

/** Whether the query already includes or excludes `key:value` at its top level. */
export function facetTermState(
	query: string,
	key: string,
	value: string,
): FacetMode | null {
	const found = findTerm(query, key, value);
	if (!found) return null;
	return found.negated ? "exclude" : "include";
}

function cut(query: string, start: number, end: number): string {
	const head = query.slice(0, start).trimEnd();
	const tail = query.slice(end).trimStart();
	return head === "" || tail === "" ? `${head}${tail}` : `${head} ${tail}`;
}

/**
 * Include or exclude `key:value`. Clicking the active choice removes it,
 * clicking the other one swaps it, otherwise the term is appended (AND).
 */
export function toggleFacetTerm(
	query: string,
	key: string,
	value: string,
	mode: FacetMode,
): string {
	const found = findTerm(query, key, value);
	let rest = query;
	if (found) {
		rest = cut(query, found.start, found.end);
		if ((found.negated ? "exclude" : "include") === mode) return rest;
	}
	const term = `${mode === "exclude" ? "-" : ""}${key}:${formatValue(value)}`;
	let base = rest.trimEnd();
	// AND binds tighter than OR: `a OR b` + a term would only narrow `b`.
	if (hasTopLevelOr(base)) base = `(${base})`;
	return base === "" ? term : `${base} ${term}`;
}

function hasTopLevelOr(query: string): boolean {
	let depth = 0;
	for (const token of tokenize(query)) {
		if (token.kind === "lparen") depth += 1;
		else if (token.kind === "rparen") depth -= 1;
		else if (token.kind === "or" && depth === 0) return true;
	}
	return false;
}
