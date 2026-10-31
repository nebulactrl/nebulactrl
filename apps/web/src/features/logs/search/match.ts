// Compiles a parsed query into a function over a parsed line. The query is
// compiled once per change; the function then runs once per line, new lines
// only. Nothing here touches the DOM or React.

import { levelFromName } from "../level";
import { facetValue, getAttr, type ParsedLine } from "./line";
import type { FacetKey, Node, ValueSpec } from "./query";

export type Matcher = (line: ParsedLine) => boolean;

/** The empty query: every line. */
export const matchAll: Matcher = () => true;

/** `*` wildcards over a whole value: `err*`, `*timeout`, `a*b`. `parts` are the pieces between the stars. */
function globMatch(parts: readonly string[], value: string): boolean {
	const first = parts[0] as string;
	if (parts.length === 1) return value === first;
	const last = parts[parts.length - 1] as string;
	if (value.length < first.length + last.length) return false;
	if (!value.startsWith(first) || !value.endsWith(last)) return false;
	let at = first.length;
	const end = value.length - last.length;
	for (let i = 1; i < parts.length - 1; i++) {
		const part = parts[i] as string;
		const found = value.indexOf(part, at);
		if (found === -1 || found + part.length > end) return false;
		at = found + part.length;
	}
	return true;
}

/** The pieces appear in order somewhere in the text. */
function containsInOrder(parts: readonly string[], text: string): boolean {
	let at = 0;
	for (const part of parts) {
		if (part === "") continue;
		const found = text.indexOf(part, at);
		if (found === -1) return false;
		at = found + part.length;
	}
	return true;
}

function scalarText(value: unknown): string | undefined {
	if (typeof value === "string") return value.toLowerCase();
	if (typeof value === "number" || typeof value === "boolean") {
		return String(value);
	}
	return undefined;
}

function numberOf(value: unknown): number | undefined {
	if (typeof value === "number") return Number.isNaN(value) ? undefined : value;
	if (typeof value === "string" && value.trim() !== "") {
		const n = Number(value);
		return Number.isNaN(n) ? undefined : n;
	}
	return undefined;
}

function valueTest(
	spec: ValueSpec,
	facet: string | undefined,
): (value: unknown) => boolean {
	if (spec.kind === "eq") {
		let parts = spec.parts.map((part) => part.toLowerCase());
		// `level:warning` and `level:WARN` both mean warn.
		if (facet === "level" && parts.length === 1) {
			parts = [levelFromName(parts[0] as string) ?? (parts[0] as string)];
		}
		return (value) => {
			const text = scalarText(value);
			return text !== undefined && globMatch(parts, text);
		};
	}
	if (spec.kind === "cmp") {
		const { op, n } = spec;
		return (value) => {
			const x = numberOf(value);
			if (x === undefined) return false;
			if (op === ">") return x > n;
			if (op === ">=") return x >= n;
			return op === "<" ? x < n : x <= n;
		};
	}
	const { lo, hi, loInc, hiInc } = spec;
	return (value) => {
		const x = numberOf(value);
		if (x === undefined) return false;
		if (lo !== null && (loInc ? x < lo : x <= lo)) return false;
		if (hi !== null && (hiInc ? x > hi : x >= hi)) return false;
		return true;
	};
}

/** A field matches when its value does; an array attribute matches when any element does. */
function anyValue(value: unknown, test: (value: unknown) => boolean): boolean {
	if (Array.isArray(value)) return value.some(test);
	if (value === undefined || value === null) return false;
	return test(value);
}

function compile(node: Node): Matcher {
	switch (node.type) {
		case "text": {
			const parts = node.parts.map((part) => part.toLowerCase());
			if (parts.length === 1) {
				const needle = parts[0] as string;
				return (line) => line.lower.includes(needle);
			}
			return (line) => containsInOrder(parts, line.lower);
		}
		case "field": {
			const facet = node.attr ? undefined : node.key;
			const test = valueTest(node.value, facet);
			if (node.attr) {
				const path = node.key;
				return (line) => anyValue(getAttr(line.attrs, path), test);
			}
			const key = node.key as FacetKey;
			return (line) => anyValue(facetValue(line, key), test);
		}
		case "and": {
			const children = node.children.map(compile);
			return (line) => children.every((child) => child(line));
		}
		case "or": {
			const children = node.children.map(compile);
			return (line) => children.some((child) => child(line));
		}
		case "not": {
			const child = compile(node.child);
			return (line) => !child(line);
		}
	}
}

/** A null query (empty or fully invalid) matches everything. */
export function compileQuery(ast: Node | null): Matcher {
	return ast ? compile(ast) : matchAll;
}

// --- Highlighting --------------------------------------------------------------

/** Pieces of text to mark in a matching line, lower case. */
export interface HighlightTerm {
	parts: string[];
}

/** The free-text and attribute-value terms a line is shown matching; negated ones are left out. */
export function highlightTerms(ast: Node | null): HighlightTerm[] {
	const out: HighlightTerm[] = [];
	const walk = (node: Node) => {
		switch (node.type) {
			case "text":
				out.push({ parts: node.parts.map((part) => part.toLowerCase()) });
				break;
			case "field":
				if (node.attr && node.value.kind === "eq") {
					out.push({
						parts: node.value.parts.map((part) => part.toLowerCase()),
					});
				}
				break;
			case "and":
			case "or":
				for (const child of node.children) walk(child);
				break;
			case "not":
				break;
		}
	};
	if (ast) walk(ast);
	return out;
}

export type HighlightRange = readonly [start: number, end: number];

const MAX_RANGES = 40;

/** Merged `[start, end)` ranges of the terms inside `lower` (the line, lower-cased). */
export function findHighlights(
	terms: readonly HighlightTerm[],
	lower: string,
): HighlightRange[] {
	const found: [number, number][] = [];
	for (const term of terms) {
		for (const part of term.parts) {
			if (part === "") continue;
			let at = lower.indexOf(part);
			while (at !== -1 && found.length < MAX_RANGES) {
				found.push([at, at + part.length]);
				at = lower.indexOf(part, at + part.length);
			}
		}
	}
	found.sort((a, b) => a[0] - b[0] || a[1] - b[1]);
	const merged: [number, number][] = [];
	for (const range of found) {
		const last = merged[merged.length - 1];
		if (last && range[0] <= last[1]) last[1] = Math.max(last[1], range[1]);
		else merged.push([range[0], range[1]]);
	}
	return merged;
}
