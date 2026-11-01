// The log search query language: a tokenizer and a parser that never throws.
//
//   free text           AND by default, case-insensitive substring
//   "exact phrase"      a quoted phrase, `\"` and `\\` escape
//   key:value           a built-in facet (service, level, stream, replica, status, deployment)
//   @path.to.field:v    an attribute of a JSON or logfmt line
//   -term, NOT term     negation
//   a OR b, (a b)       alternatives and grouping, AND is implicit
//   value*              `*` wildcard inside a value or a word
//   @n:>500 @n:<=3      numeric comparison
//   @n:[400 TO 499]     numeric range, `{` `}` exclude a bound, `*` is open
//   key:(a OR b)        several values for one key
//
// Every character of the input belongs to a token with a position, so the
// search box can colour it and point at what is wrong. Mistakes become
// `errors`; the parser drops the broken piece and keeps the rest, so a query
// being typed still filters by the parts that are valid.

export const FACET_KEYS = [
	"level",
	"service",
	"stream",
	"replica",
	"status",
	"deployment",
] as const;
export type FacetKey = (typeof FACET_KEYS)[number];

export function isFacetKey(key: string): key is FacetKey {
	return (FACET_KEYS as readonly string[]).includes(key);
}

type TokenKind =
	| "lparen"
	| "rparen"
	| "or"
	| "and"
	| "not"
	| "minus"
	| "text"
	| "phrase"
	| "key"
	| "value"
	| "range";

type CompareOp = ">" | ">=" | "<" | "<=";

interface RangeToken {
	lo: string;
	hi: string;
	loInc: boolean;
	hiInc: boolean;
	/** The closing bracket is there. */
	closed: boolean;
	/** The inside reads `<lo> TO <hi>`. */
	ok: boolean;
}

export interface Token {
	kind: TokenKind;
	/** Offsets into the input; `end` is exclusive. */
	start: number;
	end: number;
	/** Decoded: a key without `@` and `:`, a value or phrase without quotes or escapes. */
	text: string;
	/** `text` split on unescaped `*`; more than one part means a wildcard. */
	parts: string[];
	/** key: written as `@key`. */
	attr?: boolean;
	/** phrase, value: written in quotes. */
	quoted?: boolean;
	/** phrase, value: the closing quote is missing. */
	unterminated?: boolean;
	/** value: a leading comparison operator; it is the first `op.length` characters. */
	op?: CompareOp;
	range?: RangeToken;
	/** lparen: opens the value group of `key:(`. */
	group?: boolean;
}

const KEY_START = /@?[A-Za-z_][\w.-]*:/y;

function isSpace(char: string | undefined): boolean {
	return char === " " || char === "\t" || char === "\n" || char === "\r";
}

interface Word {
	end: number;
	text: string;
	parts: string[];
}

/** Reads up to whitespace or a parenthesis. `\x` is a literal x, so `\*` is not a wildcard. */
function readWord(input: string, from: number): Word {
	const parts: string[] = [];
	let current = "";
	let i = from;
	while (i < input.length) {
		const char = input[i] as string;
		if (isSpace(char) || char === "(" || char === ")") break;
		if (char === "\\" && i + 1 < input.length) {
			current += input[i + 1];
			i += 2;
			continue;
		}
		if (char === "*") {
			parts.push(current);
			current = "";
		} else {
			current += char;
		}
		i += 1;
	}
	parts.push(current);
	return { end: i, text: parts.join("*"), parts };
}

interface Quoted {
	end: number;
	text: string;
	unterminated: boolean;
}

/** Reads a quoted string starting at the opening quote. */
function readQuoted(input: string, from: number): Quoted {
	let text = "";
	let i = from + 1;
	while (i < input.length) {
		const char = input[i] as string;
		if (char === "\\" && i + 1 < input.length) {
			text += input[i + 1];
			i += 2;
			continue;
		}
		if (char === '"') return { end: i + 1, text, unterminated: false };
		text += char;
		i += 1;
	}
	return { end: i, text, unterminated: true };
}

function readRange(input: string, from: number): Token {
	const loInc = input[from] === "[";
	let close = -1;
	for (let i = from + 1; i < input.length; i++) {
		if (input[i] === "]" || input[i] === "}") {
			close = i;
			break;
		}
	}
	const closed = close !== -1;
	const inner = input.slice(from + 1, closed ? close : input.length);
	const m = /^\s*(\S+)\s+TO\s+(\S+)\s*$/i.exec(inner);
	const end = closed ? close + 1 : input.length;
	return {
		kind: "range",
		start: from,
		end,
		text: input.slice(from, end),
		parts: [input.slice(from, end)],
		range: {
			lo: m?.[1] ?? "",
			hi: m?.[2] ?? "",
			loInc,
			hiInc: closed ? input[close] === "]" : true,
			closed,
			ok: m !== null,
		},
	};
}

/** A value after `key:` or inside `key:( … )`: quoted, a range, `>5`, or a word. */
function readValue(input: string, from: number): Token {
	const char = input[from];
	if (char === '"') {
		const quoted = readQuoted(input, from);
		return {
			kind: "value",
			start: from,
			end: quoted.end,
			text: quoted.text,
			parts: [quoted.text],
			quoted: true,
			unterminated: quoted.unterminated,
		};
	}
	if (char === "[" || char === "{") return readRange(input, from);
	let opEnd = from;
	let op: CompareOp | undefined;
	if (char === ">" || char === "<") {
		opEnd = from + (input[from + 1] === "=" ? 2 : 1);
		op = input.slice(from, opEnd) as CompareOp;
	}
	const word = readWord(input, opEnd);
	const token: Token = {
		kind: "value",
		start: from,
		end: word.end,
		text: word.text,
		parts: word.parts,
	};
	if (op) token.op = op;
	return token;
}

/** Splits the input into tokens with positions. Never throws; whitespace is skipped. */
export function tokenize(input: string): Token[] {
	const out: Token[] = [];
	// true: inside `key:( … )`, where words are values of that key.
	const groups: boolean[] = [];
	let i = 0;
	while (i < input.length) {
		const char = input[i] as string;
		if (isSpace(char)) {
			i += 1;
			continue;
		}
		const inGroup = groups[groups.length - 1] === true;
		if (char === "(") {
			out.push({
				kind: "lparen",
				start: i,
				end: i + 1,
				text: "(",
				parts: ["("],
			});
			groups.push(inGroup);
			i += 1;
			continue;
		}
		if (char === ")") {
			out.push({
				kind: "rparen",
				start: i,
				end: i + 1,
				text: ")",
				parts: [")"],
			});
			groups.pop();
			i += 1;
			continue;
		}
		const next = input[i + 1];
		if (
			char === "-" &&
			next !== undefined &&
			!isSpace(next) &&
			next !== ")" &&
			next !== "-"
		) {
			out.push({
				kind: "minus",
				start: i,
				end: i + 1,
				text: "-",
				parts: ["-"],
			});
			i += 1;
			continue;
		}
		if (inGroup) {
			const word = readValue(input, i);
			const raw = input.slice(word.start, word.end);
			const keyword = KEYWORDS[raw];
			if (keyword && !word.quoted) {
				out.push({
					kind: keyword,
					start: i,
					end: word.end,
					text: raw,
					parts: [raw],
				});
			} else {
				out.push(word);
			}
			i = word.end;
			continue;
		}
		if (char === '"') {
			const quoted = readQuoted(input, i);
			out.push({
				kind: "phrase",
				start: i,
				end: quoted.end,
				text: quoted.text,
				parts: [quoted.text],
				quoted: true,
				unterminated: quoted.unterminated,
			});
			i = quoted.end;
			continue;
		}
		KEY_START.lastIndex = i;
		const key = KEY_START.exec(input);
		if (key && !input.startsWith("//", i + key[0].length)) {
			const at = key[0].startsWith("@");
			const name = key[0].slice(at ? 1 : 0, -1);
			const colon = i + key[0].length;
			out.push({
				kind: "key",
				start: i,
				end: colon,
				text: name,
				parts: [name],
				attr: at,
			});
			i = colon;
			const after = input[i];
			if (after === undefined || isSpace(after) || after === ")") continue;
			if (after === "(") {
				out.push({
					kind: "lparen",
					start: i,
					end: i + 1,
					text: "(",
					parts: ["("],
					group: true,
				});
				groups.push(true);
				i += 1;
				continue;
			}
			const value = readValue(input, i);
			out.push(value);
			i = value.end;
			continue;
		}
		const word = readWord(input, i);
		const raw = input.slice(i, word.end);
		const keyword = KEYWORDS[raw];
		out.push(
			keyword
				? { kind: keyword, start: i, end: word.end, text: raw, parts: [raw] }
				: {
						kind: "text",
						start: i,
						end: word.end,
						text: word.text,
						parts: word.parts,
					},
		);
		i = word.end;
	}
	return out;
}

const KEYWORDS: Record<string, "or" | "and" | "not" | undefined> = {
	OR: "or",
	AND: "and",
	NOT: "not",
};

// --- AST ----------------------------------------------------------------------

export type ValueSpec =
	| { kind: "eq"; text: string; parts: string[] }
	| { kind: "cmp"; op: CompareOp; n: number }
	| {
			kind: "range";
			lo: number | null;
			hi: number | null;
			loInc: boolean;
			hiInc: boolean;
	  };

interface Span {
	start: number;
	end: number;
}

export type Node =
	| (Span & { type: "text"; text: string; parts: string[]; phrase: boolean })
	| (Span & { type: "field"; key: string; attr: boolean; value: ValueSpec })
	| (Span & { type: "and" | "or"; children: Node[] })
	| (Span & { type: "not"; child: Node });

interface ParseError extends Span {
	message: string;
}

export interface ParsedQuery {
	input: string;
	tokens: Token[];
	/** The valid part of the query; null when there is nothing to match on. */
	ast: Node | null;
	errors: ParseError[];
}

interface FieldContext {
	key: string;
	attr: boolean;
}

class Parser {
	private i = 0;
	readonly errors: ParseError[] = [];

	constructor(
		private readonly input: string,
		private readonly tokens: readonly Token[],
	) {}

	eof(): boolean {
		return this.i >= this.tokens.length;
	}

	/** A `)` with no `(`: report it and move on. */
	skipStray() {
		const token = this.tokens[this.i] as Token;
		this.i += 1;
		this.error(token, "Unmatched closing parenthesis");
	}

	private peek(): Token | undefined {
		return this.tokens[this.i];
	}

	private error(at: Span, message: string) {
		this.errors.push({ start: at.start, end: at.end, message });
	}

	/** Alternatives: the loosest binding. Stops at `)` or the end. */
	parseOr(ctx: FieldContext | null): Node | null {
		const parts: Node[] = [];
		while (this.peek()?.kind === "or") {
			this.error(this.tokens[this.i] as Token, "OR needs a term before it");
			this.i += 1;
		}
		const first = this.parseAnd(ctx);
		if (first) parts.push(first);
		while (this.peek()?.kind === "or") {
			const or = this.tokens[this.i] as Token;
			this.i += 1;
			const next = this.parseAnd(ctx);
			if (next) parts.push(next);
			else this.error(or, "OR needs a term after it");
		}
		return combine("or", parts);
	}

	private parseAnd(ctx: FieldContext | null): Node | null {
		const items: Node[] = [];
		for (;;) {
			const token = this.peek();
			if (!token || token.kind === "or" || token.kind === "rparen") break;
			if (token.kind === "and") {
				this.i += 1;
				const next = this.peek();
				if (
					items.length === 0 ||
					!next ||
					next.kind === "or" ||
					next.kind === "rparen"
				) {
					this.error(token, "AND needs a term on both sides");
				}
				continue;
			}
			const node = this.parseUnary(ctx);
			if (node) items.push(node);
		}
		return combine("and", items);
	}

	private parseUnary(ctx: FieldContext | null): Node | null {
		const token = this.tokens[this.i] as Token;
		if (token.kind === "not" || token.kind === "minus") {
			this.i += 1;
			const next = this.peek();
			if (
				!next ||
				next.kind === "or" ||
				next.kind === "and" ||
				next.kind === "rparen"
			) {
				this.error(token, "Nothing to negate");
				return null;
			}
			const child = this.parseUnary(ctx);
			if (!child) return null;
			return { type: "not", child, start: token.start, end: child.end };
		}
		return this.parsePrimary(ctx);
	}

	private parsePrimary(ctx: FieldContext | null): Node | null {
		const token = this.tokens[this.i] as Token;
		this.i += 1;
		switch (token.kind) {
			case "lparen": {
				if (this.peek()?.kind === "rparen") {
					this.i += 1;
					this.error(token, "Empty parentheses");
					return null;
				}
				const inner = this.parseOr(ctx);
				if (this.peek()?.kind === "rparen") this.i += 1;
				else this.error(token, "Missing closing parenthesis");
				return inner;
			}
			case "text":
			case "phrase": {
				if (token.unterminated) this.error(token, "Missing closing quote");
				return {
					type: "text",
					text: token.text,
					parts: token.parts,
					phrase: token.kind === "phrase",
					start: token.start,
					end: token.end,
				};
			}
			case "key":
				return this.parseKey(token);
			case "value":
			case "range":
				if (ctx) return this.field(ctx.key, ctx.attr, token, token);
				return {
					type: "text",
					text: token.text,
					parts: token.parts,
					phrase: false,
					start: token.start,
					end: token.end,
				};
			default:
				// Operators are consumed by their parents; a stray one is skipped.
				this.error(token, `Unexpected ${token.text}`);
				return null;
		}
	}

	private parseKey(key: Token): Node | null {
		const known = key.attr === true || isFacetKey(key.text);
		const raw = this.input.slice(key.start, key.end);
		const next = this.peek();
		if (next?.kind === "lparen" && next.group && next.start === key.end) {
			this.i += 1;
			if (this.peek()?.kind === "rparen") {
				this.i += 1;
				this.error(next, "Empty parentheses");
				return null;
			}
			if (!known) this.error(key, unknownFacet(key.text));
			const inner = this.parseOr({
				key: key.text,
				attr: key.attr === true || !known,
			});
			if (this.peek()?.kind === "rparen") this.i += 1;
			else this.error(next, "Missing closing parenthesis");
			return inner;
		}
		if (
			next &&
			(next.kind === "value" || next.kind === "range") &&
			next.start === key.end
		) {
			this.i += 1;
			if (!known) {
				// `localhost:8080`, `http:x`: prose, not a facet. Search it as text.
				this.error(key, `${unknownFacet(key.text)}; searching it as text`);
				const text = this.input.slice(key.start, next.end);
				return {
					type: "text",
					text,
					parts: [text],
					phrase: true,
					start: key.start,
					end: next.end,
				};
			}
			return this.field(key.text, key.attr === true, next, {
				start: key.start,
				end: next.end,
			});
		}
		if (!known) {
			// "Error:" at the end of a sentence is text.
			return {
				type: "text",
				text: raw,
				parts: [raw],
				phrase: true,
				start: key.start,
				end: key.end,
			};
		}
		this.error(key, `Add a value after ${raw}`);
		return null;
	}

	private field(
		key: string,
		attr: boolean,
		value: Token,
		span: Span,
	): Node | null {
		const spec = this.valueSpec(value);
		if (!spec) return null;
		return {
			type: "field",
			key,
			attr,
			value: spec,
			start: span.start,
			end: span.end,
		};
	}

	private valueSpec(token: Token): ValueSpec | null {
		if (token.kind === "range") {
			const range = token.range as RangeToken;
			if (!range.ok) {
				this.error(token, "Write a range as [400 TO 499]");
				return null;
			}
			if (!range.closed) {
				this.error(token, "Missing closing ]");
				return null;
			}
			const lo = bound(range.lo);
			const hi = bound(range.hi);
			if (lo === undefined || hi === undefined) {
				this.error(token, "Range bounds are numbers or *");
				return null;
			}
			return { kind: "range", lo, hi, loInc: range.loInc, hiInc: range.hiInc };
		}
		if (token.unterminated) this.error(token, "Missing closing quote");
		if (token.op) {
			const n = Number(token.text);
			if (token.text === "" || Number.isNaN(n)) {
				this.error(token, `Expected a number after ${token.op}`);
				return null;
			}
			return { kind: "cmp", op: token.op, n };
		}
		return { kind: "eq", text: token.text, parts: token.parts };
	}
}

function unknownFacet(key: string): string {
	return `Unknown facet "${key}". Write @${key} for a log attribute`;
}

function bound(text: string): number | null | undefined {
	if (text === "*") return null;
	const n = Number(text);
	return Number.isNaN(n) ? undefined : n;
}

function combine(type: "and" | "or", parts: Node[]): Node | null {
	if (parts.length === 0) return null;
	if (parts.length === 1) return parts[0] as Node;
	return {
		type,
		children: parts,
		start: (parts[0] as Node).start,
		end: (parts[parts.length - 1] as Node).end,
	};
}

/** Parses a query. Always returns: mistakes land in `errors` and the broken piece is dropped. */
export function parseQuery(input: string): ParsedQuery {
	const tokens = tokenize(input);
	const parser = new Parser(input, tokens);
	const parts: Node[] = [];
	// Only a stray `)` stops parseOr early; report it and carry on.
	while (!parser.eof()) {
		const node = parser.parseOr(null);
		if (node) parts.push(node);
		if (!parser.eof()) parser.skipStray();
	}
	return { input, tokens, ast: combine("and", parts), errors: parser.errors };
}

// --- Highlighting --------------------------------------------------------------

export type SegmentKind =
	| "plain"
	| "text"
	| "phrase"
	| "key"
	| "attr"
	| "value"
	| "operator"
	| "invalid";

export interface Segment {
	start: number;
	end: number;
	kind: SegmentKind;
}

const KIND_OF: Record<TokenKind, SegmentKind> = {
	lparen: "operator",
	rparen: "operator",
	or: "operator",
	and: "operator",
	not: "operator",
	minus: "operator",
	text: "text",
	phrase: "phrase",
	key: "key",
	value: "value",
	range: "value",
};

/** Covers the whole input with runs of one kind, for the coloured overlay of the search box. */
export function highlightSegments(parsed: ParsedQuery): Segment[] {
	const { input } = parsed;
	const kinds: SegmentKind[] = new Array(input.length).fill("plain");
	for (const token of parsed.tokens) {
		const kind =
			token.kind === "key" && token.attr ? "attr" : KIND_OF[token.kind];
		const opLen = token.op?.length ?? 0;
		for (let i = token.start; i < token.end; i++) {
			kinds[i] = i < token.start + opLen ? "operator" : kind;
		}
	}
	for (const error of parsed.errors) {
		for (let i = error.start; i < error.end; i++) kinds[i] = "invalid";
	}
	const out: Segment[] = [];
	for (let i = 0; i < kinds.length; i++) {
		const kind = kinds[i] as SegmentKind;
		const last = out[out.length - 1];
		if (last && last.kind === kind) last.end = i + 1;
		else out.push({ start: i, end: i + 1, kind });
	}
	return out;
}
