// Autocomplete for the search box: what could be typed at the caret. Pure, so
// the ranking is tested without a DOM.
//
//   at a fresh term     recent queries (empty box only), facet keys, the top @attributes, operators
//   in a word           facet keys or @attributes that match what is typed, operators, recent queries
//   after `key:`        the values seen in the buffer for that key, most frequent first
//   inside `key:( )`    the same values
//
// Each suggestion carries the range it replaces, so accepting one is a plain
// string splice.

import { type AttrStat, type FacetStats, formatValue } from "./facets";
import {
	FACET_KEYS,
	type FacetKey,
	isFacetKey,
	type Token,
	tokenize,
} from "./query";

export type SuggestionKind = "key" | "attr" | "value" | "operator" | "recent";

export interface Suggestion {
	kind: SuggestionKind;
	/** What the list shows. */
	label: string;
	/** Replaces `[start, end)` of the input. */
	insert: string;
	start: number;
	end: number;
	/** Secondary text: a count, or what the thing is. */
	detail?: string;
	count?: number;
}

export type SuggestContext = Pick<FacetStats, "facets" | "attrs"> & {
	recent: readonly string[];
};

const MAX_SUGGESTIONS = 10;
const MAX_RECENT_SHOWN = 5;
const MAX_ATTRS_AT_START = 3;

/** 0 exact, 1 prefix, 2 prefix of a word inside it, 3 contains, null no match. */
function matchScore(label: string, partial: string): number | null {
	if (partial === "") return 0;
	const a = label.toLowerCase();
	const p = partial.toLowerCase();
	if (a === p) return 0;
	if (a.startsWith(p)) return 1;
	const at = a.indexOf(p);
	if (at === -1) return null;
	return /[._\-/:]/.test(a[at - 1] ?? "") ? 2 : 3;
}

interface Ranked extends Suggestion {
	score: number;
}

function rank(items: Ranked[]): Suggestion[] {
	items.sort(
		(a, b) =>
			a.score - b.score ||
			(b.count ?? 0) - (a.count ?? 0) ||
			a.label.localeCompare(b.label),
	);
	return items.map(({ score: _score, ...item }) => item);
}

interface Field {
	key: string;
	attr: boolean;
}

/** The `key:( … )` the caret is inside, if any. */
function groupAt(tokens: readonly Token[], caret: number): Field | null {
	const stack: (Field | null)[] = [];
	for (let i = 0; i < tokens.length; i++) {
		const token = tokens[i] as Token;
		if (token.end > caret) break;
		if (token.kind === "lparen") {
			const prev = tokens[i - 1];
			if (token.group && prev?.kind === "key") {
				stack.push({ key: prev.text, attr: prev.attr === true });
			} else {
				stack.push(stack[stack.length - 1] ?? null);
			}
		} else if (token.kind === "rparen") {
			stack.pop();
		}
	}
	return stack[stack.length - 1] ?? null;
}

function valuesOf(
	ctx: SuggestContext,
	field: Field,
): ReadonlyMap<string, number> | null {
	if (field.attr) return ctx.attrs.get(field.key)?.values ?? null;
	return isFacetKey(field.key) ? ctx.facets[field.key] : null;
}

function valueSuggestions(
	input: string,
	ctx: SuggestContext,
	field: Field,
	partial: string,
	start: number,
	end: number,
): Suggestion[] {
	const out: Ranked[] = [];
	const tail = input[end];
	const space = tail === undefined ? " " : "";
	for (const [value, count] of valuesOf(ctx, field) ?? []) {
		const score = matchScore(value, partial);
		if (score === null) continue;
		out.push({
			kind: "value",
			label: value,
			insert: formatValue(value) + space,
			start,
			end,
			detail: String(count),
			count,
			score,
		});
	}
	const items = rank(out);
	if (partial !== "") return items.slice(0, MAX_SUGGESTIONS);

	// Nothing typed yet: comparisons for a number-like field come last.
	const stat: AttrStat | undefined = field.attr
		? ctx.attrs.get(field.key)
		: undefined;
	const numeric =
		field.key === "status" ||
		field.key === "replica" ||
		(stat !== undefined && stat.count > 0 && stat.numeric * 2 >= stat.count);
	if (numeric) {
		for (const op of [">", ">=", "<", "<="]) {
			items.push({
				kind: "operator",
				label: op,
				insert: op,
				start,
				end,
				detail: "compare",
			});
		}
		if (field.key === "status") {
			items.push({
				kind: "operator",
				label: "[400 TO 499]",
				insert: `[400 TO 499]${space}`,
				start,
				end,
				detail: "range",
			});
		}
	}
	return items.slice(0, MAX_SUGGESTIONS);
}

const OPERATORS: { word: string; detail: string }[] = [
	{ word: "OR", detail: "either" },
	{ word: "AND", detail: "both" },
	{ word: "NOT", detail: "exclude" },
];

/** Candidates at the caret, best first, at most ten. */
export function suggest(
	input: string,
	caret: number,
	ctx: SuggestContext,
): Suggestion[] {
	const tokens = tokenize(input);
	const index = tokens.findIndex((t) => t.start < caret && caret <= t.end);
	const cur = index === -1 ? undefined : tokens[index];
	const group = groupAt(tokens, caret);

	if (cur) {
		switch (cur.kind) {
			case "key": {
				if (caret < cur.end) {
					return wordSuggestions(
						input,
						tokens,
						ctx,
						input.slice(cur.start, caret),
						cur,
						index,
					);
				}
				const value = tokens[index + 1];
				const adjacent = value?.kind === "value" && value.start === cur.end;
				return valueSuggestions(
					input,
					ctx,
					{ key: cur.text, attr: cur.attr === true },
					"",
					adjacent && value ? value.start : caret,
					adjacent && value ? value.end : caret,
				);
			}
			case "value": {
				if (cur.op) return [];
				const prev = tokens[index - 1];
				const field: Field | null =
					prev?.kind === "key" && prev.end === cur.start
						? { key: prev.text, attr: prev.attr === true }
						: group;
				if (!field) return [];
				const open = cur.quoted ? 1 : 0;
				const partial = input.slice(cur.start + open, caret);
				return valueSuggestions(input, ctx, field, partial, cur.start, cur.end);
			}
			case "text":
				return wordSuggestions(
					input,
					tokens,
					ctx,
					input.slice(cur.start, caret),
					cur,
					index,
				);
			case "minus":
				return freshTerm(input, tokens, ctx, group, caret, index + 1);
			case "lparen":
				return freshTerm(input, tokens, ctx, group, caret, index + 1);
			default:
				// A closed phrase, a range, `)` or an operator word: nothing to complete.
				return [];
		}
	}
	return freshTerm(
		input,
		tokens,
		ctx,
		group,
		caret,
		tokens.findIndex((t) => t.start >= caret),
	);
}

/** The caret sits where a new term can start. */
function freshTerm(
	input: string,
	tokens: readonly Token[],
	ctx: SuggestContext,
	group: Field | null,
	caret: number,
	nextIndex: number,
): Suggestion[] {
	if (group) return valueSuggestions(input, ctx, group, "", caret, caret);
	const out: Suggestion[] = [];
	const empty = input.trim() === "";
	if (empty) {
		for (const query of ctx.recent.slice(0, MAX_RECENT_SHOWN)) {
			out.push({
				kind: "recent",
				label: query,
				insert: query,
				start: 0,
				end: input.length,
			});
		}
	}
	// A view whose counts have not arrived (or matched nothing) must not leave
	// the list without keys: those without values follow after the rest.
	for (const key of FACET_KEYS) {
		const size = ctx.facets[key].size;
		if (size > 0) out.push(keySuggestion(key, size, caret, caret));
	}
	const attrs = [...ctx.attrs].sort(
		(a, b) => b[1].count - a[1].count || a[0].localeCompare(b[0]),
	);
	for (const [path, stat] of attrs.slice(0, MAX_ATTRS_AT_START)) {
		out.push({
			kind: "attr",
			label: `@${path}:`,
			insert: `@${path}:`,
			start: caret,
			end: caret,
			detail: `${stat.count}`,
			count: stat.count,
		});
	}
	const before = tokens[nextIndex === -1 ? tokens.length - 1 : nextIndex - 1];
	out.push(...operatorSuggestions(before, "", caret, caret));
	// Behind everything else, so a crowded list drops them first.
	for (const key of FACET_KEYS) {
		if (ctx.facets[key].size > 0) continue;
		out.push(keySuggestion(key, 0, caret, caret));
	}
	return out.slice(0, MAX_SUGGESTIONS);
}

function keySuggestion(
	key: FacetKey,
	size: number,
	start: number,
	end: number,
): Suggestion {
	return {
		kind: "key",
		label: `${key}:`,
		insert: `${key}:`,
		start,
		end,
		detail:
			size === 0
				? "no values yet"
				: `${size} ${size === 1 ? "value" : "values"}`,
		count: size,
	};
}

function operatorSuggestions(
	before: Token | undefined,
	partial: string,
	start: number,
	end: number,
): Suggestion[] {
	const joins =
		before !== undefined &&
		before.kind !== "or" &&
		before.kind !== "and" &&
		before.kind !== "not" &&
		before.kind !== "minus" &&
		before.kind !== "lparen";
	const out: Suggestion[] = [];
	for (const { word, detail } of OPERATORS) {
		if (!joins && word !== "NOT") continue;
		// Operators are upper case; a typed `or` is more likely the word "or".
		if (
			partial !== "" &&
			!(partial === partial.toUpperCase() && word.startsWith(partial))
		) {
			continue;
		}
		out.push({
			kind: "operator",
			label: word,
			insert: `${word} `,
			start,
			end,
			detail,
		});
	}
	return out;
}

/** The caret is inside a bare word or a key name: complete it as a key, an attribute or an operator. */
function wordSuggestions(
	input: string,
	tokens: readonly Token[],
	ctx: SuggestContext,
	partial: string,
	cur: Token,
	index: number,
): Suggestion[] {
	const start = cur.start;
	const end = cur.end;
	if (partial.startsWith("@")) {
		const typed = partial.slice(1);
		const out: Ranked[] = [];
		for (const [path, stat] of ctx.attrs) {
			const score = matchScore(path, typed);
			if (score === null) continue;
			out.push({
				kind: "attr",
				label: `@${path}:`,
				insert: `@${path}:`,
				start,
				end,
				detail: `${stat.count}`,
				count: stat.count,
				score,
			});
		}
		return rank(out).slice(0, MAX_SUGGESTIONS);
	}

	const keys: Ranked[] = [];
	for (const key of FACET_KEYS) {
		const score = matchScore(key, partial);
		if (score === null) continue;
		const size = ctx.facets[key].size;
		keys.push({
			kind: "key",
			label: `${key}:`,
			insert: `${key}:`,
			start,
			end,
			detail:
				size === 0
					? "no values yet"
					: `${size} ${size === 1 ? "value" : "values"}`,
			count: size,
			// A facet with nothing in the buffer ranks behind one that has values.
			score: score + (size === 0 ? 0.5 : 0),
		});
	}
	const attrs: Ranked[] = [];
	for (const [path, stat] of ctx.attrs) {
		const score = matchScore(path, partial);
		if (score === null || score > 1) continue;
		attrs.push({
			kind: "attr",
			label: `@${path}:`,
			insert: `@${path}:`,
			start,
			end,
			detail: `${stat.count}`,
			count: stat.count,
			score: score + 1,
		});
	}
	const recent: Suggestion[] = [];
	if (tokens.length === 1 && partial !== "") {
		for (const query of ctx.recent) {
			if (matchScore(query, partial) === null) continue;
			recent.push({
				kind: "recent",
				label: query,
				insert: query,
				start: 0,
				end: input.length,
			});
			if (recent.length >= MAX_RECENT_SHOWN) break;
		}
	}
	// An operator is an exact-prefix match: it outranks a key that merely contains the letters.
	const ops: Ranked[] = operatorSuggestions(
		tokens[index - 1],
		partial,
		start,
		end,
	).map((op) => ({ ...op, score: 1 }));
	return [...rank([...keys, ...attrs, ...ops]), ...recent].slice(
		0,
		MAX_SUGGESTIONS,
	);
}

/** The input and caret after accepting a suggestion. */
export function applySuggestion(
	input: string,
	suggestion: Suggestion,
): { value: string; caret: number } {
	const value =
		input.slice(0, suggestion.start) +
		suggestion.insert +
		input.slice(suggestion.end);
	return { value, caret: suggestion.start + suggestion.insert.length };
}

// --- The query without the clause being edited -----------------------------------

/** A term of the query: the tokens `first..last` (inclusive) and the text they span. */
interface Term {
	first: number;
	last: number;
	start: number;
	end: number;
}

/** The `)` that closes the `(` at `open`; the last token when it is never closed. */
function closingParen(tokens: readonly Token[], open: number): number {
	let depth = 0;
	for (let j = open; j < tokens.length; j++) {
		const kind = (tokens[j] as Token).kind;
		if (kind === "lparen") depth += 1;
		else if (kind === "rparen") {
			depth -= 1;
			if (depth === 0) return j;
		}
	}
	return tokens.length - 1;
}

/**
 * The term that starts at token `i`, or null for a parenthesis (which groups
 * terms but is none). A term is a word, a phrase, `key:value`, `key:(…)`, a
 * lone `key:`, an operator word, or any of those behind a `-` or `NOT`.
 */
function termFrom(tokens: readonly Token[], i: number): Term | null {
	const token = tokens[i] as Token;
	let last = i;
	switch (token.kind) {
		case "key": {
			const next = tokens[i + 1];
			if (next && next.start === token.end) {
				if (next.kind === "value" || next.kind === "range") last = i + 1;
				else if (next.kind === "lparen" && next.group) {
					last = closingParen(tokens, i + 1);
				}
			}
			break;
		}
		case "minus":
		case "not": {
			const next = tokens[i + 1];
			const attached = token.kind === "not" || next?.start === token.end;
			if (
				attached &&
				next &&
				(next.kind === "text" || next.kind === "phrase" || next.kind === "key")
			) {
				last = (termFrom(tokens, i + 1) as Term).last;
			}
			break;
		}
		case "text":
		case "phrase":
		case "value":
		case "range":
		case "or":
		case "and":
			break;
		default:
			return null;
	}
	return {
		first: i,
		last,
		start: token.start,
		end: (tokens[last] as Token).end,
	};
}

function termsOf(tokens: readonly Token[]): Term[] {
	const terms: Term[] = [];
	for (let i = 0; i < tokens.length; ) {
		const term = termFrom(tokens, i);
		if (term) {
			terms.push(term);
			i = term.last + 1;
		} else {
			i += 1;
		}
	}
	return terms;
}

/**
 * The query with the term the caret is in or touching taken out (its `-` or
 * `NOT`, and the `OR` or `AND` that joined it to its neighbour, with it), the
 * seam closed up. This is what a value suggestion counts over: `level:error
 * @me|` suggests attributes of the error lines, not of lines that match the
 * half-typed `@me`. A caret touching no term leaves the query as it is.
 */
export function withoutClauseAt(input: string, caret: number): string {
	const tokens = tokenize(input);
	const terms = termsOf(tokens);
	// A caret inside or at the end of a term wins over one at the start of the next.
	// After `a OR ` (space and all) the dangling operator is what is being edited.
	const term =
		terms.find((t) => t.start < caret && caret <= t.end) ??
		terms.find((t) => t.start <= caret && caret <= t.end) ??
		terms.findLast(
			(t) =>
				t.first === t.last &&
				t.end <= caret &&
				input.slice(t.end, caret).trim() === "" &&
				["or", "and", "not", "minus"].includes((tokens[t.first] as Token).kind),
		);
	if (!term) return input.trim();
	let { start, end } = term;
	const own = (tokens[term.first] as Token).kind;
	if (own !== "or" && own !== "and") {
		const before = tokens[term.first - 1];
		const after = tokens[term.last + 1];
		if (before && (before.kind === "or" || before.kind === "and")) {
			start = before.start;
		} else if (after && (after.kind === "or" || after.kind === "and")) {
			end = after.end;
		}
	}
	const head = input.slice(0, start).trimEnd();
	const tail = input.slice(end).trimStart();
	return head === "" || tail === "" ? `${head}${tail}` : `${head} ${tail}`;
}

/**
 * Whether the query ends in a term that is still being typed: a `key:` with
 * no value, an `@word` that is no key yet, an unfinished phrase, or a
 * dangling `-`, `NOT`, `OR` or `AND`. Such a query matches nothing for want
 * of its last word, so a view keeps the counts of the last whole query.
 */
export function hasIncompleteTail(input: string): boolean {
	const tokens = tokenize(input);
	const term = termsOf(tokens).at(-1);
	if (!term) return false;
	const tail = tokens[term.last] as Token;
	switch (tail.kind) {
		case "key":
		case "minus":
		case "not":
		case "or":
		case "and":
			return true;
		case "text":
			// A `-` with nothing after it is a word to the tokenizer, not a minus.
			return tail.text.startsWith("@") || tail.text === "-";
		case "phrase":
		case "value":
			return tail.unterminated === true;
		default:
			return false;
	}
}
