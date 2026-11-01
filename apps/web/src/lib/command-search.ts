export type SearchableCommand = {
	/** Needed only for recency weighting. */
	id?: string;
	label: string;
	group?: string;
	hint?: string;
	/** Words that find the command without being drawn: "healthcheck" for a Processes section. */
	keywords?: readonly string[];
};

export interface SearchOptions {
	/** Command ids, most recently used first; recent commands win ties. */
	recent?: readonly string[];
}

export interface FuzzyMatch {
	/** Higher is better; always above zero for a match. */
	score: number;
	/** UTF-16 indexes of the haystack characters the needle landed on. */
	positions: number[];
}

// The scoring is fzy's: a match earns a bonus for where it lands (the start
// of a word, after a separator, a capital in a camelCase run) and another
// for each character that directly follows the one before it, and it pays a
// small price for every character skipped. The constants are fzy's too, so
// "dpl" finds "Deployments" and ranks it above "Add a pool" without any
// tuning of ours.
const GAP_LEADING = -0.005;
const GAP_TRAILING = -0.005;
const GAP_INNER = -0.01;
const CONSECUTIVE = 1;
const BONUS_SLASH = 0.9;
const BONUS_WORD = 0.8;
const BONUS_CAPITAL = 0.7;
const BONUS_DOT = 0.6;

/** A needle that is only a substring, with nothing better to say for it. */
const SUBSTRING_FLOOR = 0.1;
/** Past these the matrix is not worth building; a literal substring still counts. */
const MAX_NEEDLE = 32;
const MAX_HAYSTACK = 160;

/** What each field is worth next to the label. */
const WEIGHT = { label: 1, keywords: 0.8, hint: 0.5, group: 0.3 } as const;
/** Added when the whole query opens the label, and when it is the label. */
const PREFIX_BONUS = 1;
const EXACT_BONUS = 2;
/**
 * What the most recently used command gains; each older one gains half as
 * much as the one before. Worth about one extra word-start, so recency
 * settles ties between similar matches and never beats a better match.
 */
const RECENCY_BONUS = 0.75;

const WORD_CHAR = /[\p{L}\p{N}]/u;

function bonusAt(haystack: string, index: number): number {
	if (index === 0) return BONUS_SLASH;
	const prev = haystack.charAt(index - 1);
	if (prev === "/") return BONUS_SLASH;
	if (prev === ".") return BONUS_DOT;
	if (!WORD_CHAR.test(prev)) return BONUS_WORD;
	const here = haystack.charAt(index);
	if (
		prev === prev.toLowerCase() &&
		here !== here.toLowerCase() &&
		here === here.toUpperCase()
	)
		return BONUS_CAPITAL;
	return 0;
}

function substringMatch(needle: string, haystack: string): FuzzyMatch | null {
	const at = haystack.toLowerCase().indexOf(needle.toLowerCase());
	if (at < 0) return null;
	return {
		score: SUBSTRING_FLOOR,
		positions: Array.from({ length: needle.length }, (_, i) => at + i),
	};
}

/**
 * Whether `needle` is a subsequence of `haystack` (ignoring case), and how
 * well it lands. Null when it is not, or when it is so scattered that it
 * would be noise: a match must score above zero, which a lone character
 * deep inside a word never does — unless the needle is a plain substring,
 * which always counts.
 */
export function fuzzyMatch(
	needle: string,
	haystack: string,
): FuzzyMatch | null {
	const n = needle.length;
	if (n === 0) return null;
	const hay =
		haystack.length > MAX_HAYSTACK ? haystack.slice(0, MAX_HAYSTACK) : haystack;
	const m = hay.length;
	if (n > m) return null;
	if (n > MAX_NEEDLE) return substringMatch(needle, hay);

	const ne = Array.from({ length: n }, (_, i) =>
		needle.charAt(i).toLowerCase(),
	);
	const ha = Array.from({ length: m }, (_, j) => hay.charAt(j).toLowerCase());
	const bonus = Array.from({ length: m }, (_, j) => bonusAt(hay, j));

	// d[i][j]: the best score with needle[i] matched on haystack[j].
	// best[i][j]: the best score for needle[..=i] within haystack[..=j].
	const d = new Float64Array(n * m).fill(Number.NEGATIVE_INFINITY);
	const best = new Float64Array(n * m).fill(Number.NEGATIVE_INFINITY);
	for (let i = 0; i < n; i++) {
		let previous = Number.NEGATIVE_INFINITY;
		const gap = i === n - 1 ? GAP_TRAILING : GAP_INNER;
		for (let j = 0; j < m; j++) {
			const at = i * m + j;
			if (ne[i] === ha[j]) {
				let score = Number.NEGATIVE_INFINITY;
				if (i === 0) {
					score = j * GAP_LEADING + (bonus[j] ?? 0);
				} else if (j > 0) {
					score = Math.max(
						(best[(i - 1) * m + j - 1] ?? Number.NEGATIVE_INFINITY) +
							(bonus[j] ?? 0),
						(d[(i - 1) * m + j - 1] ?? Number.NEGATIVE_INFINITY) + CONSECUTIVE,
					);
				}
				d[at] = score;
				previous = Math.max(score, previous + gap);
			} else {
				previous += gap;
			}
			best[at] = previous;
		}
	}

	const total = best[n * m - 1] ?? Number.NEGATIVE_INFINITY;
	if (total === Number.NEGATIVE_INFINITY) return null;
	if (total <= 0) return substringMatch(needle, hay);

	// Walk back from the end to name the characters the best score used.
	const positions = new Array<number>(n).fill(0);
	let required = false;
	let j = m - 1;
	for (let i = n - 1; i >= 0; i--) {
		for (; j >= 0; j--) {
			const at = i * m + j;
			const here = d[at] ?? Number.NEGATIVE_INFINITY;
			if (
				here !== Number.NEGATIVE_INFINITY &&
				(required || here === best[at])
			) {
				required =
					i > 0 &&
					j > 0 &&
					best[at] ===
						(d[(i - 1) * m + j - 1] ?? Number.NEGATIVE_INFINITY) + CONSECUTIVE;
				positions[i] = j;
				j--;
				break;
			}
		}
	}
	return { score: total, positions };
}

function tokensOf(query: string): string[] {
	return query.trim().split(/\s+/).filter(Boolean);
}

/** The best weighted score of one token over a command's fields; null when none match. */
function scoreToken(token: string, item: SearchableCommand): number | null {
	let top: number | null = null;
	const consider = (text: string | undefined, weight: number) => {
		if (!text) return;
		const found = fuzzyMatch(token, text);
		if (!found) return;
		const weighted = found.score * weight;
		if (top === null || weighted > top) top = weighted;
	};
	consider(item.label, WEIGHT.label);
	for (const keyword of item.keywords ?? []) consider(keyword, WEIGHT.keywords);
	consider(item.hint, WEIGHT.hint);
	consider(item.group, WEIGHT.group);
	return top;
}

/**
 * How well a command answers a query, or null when it does not. Every
 * word of the query must match somewhere in the label, keywords, hint or
 * group; the label counts most.
 */
function scoreCommand(
	item: SearchableCommand,
	query: string,
	options: SearchOptions = {},
): number | null {
	const tokens = tokensOf(query);
	if (tokens.length === 0) return 0;
	let score = 0;
	for (const token of tokens) {
		const scored = scoreToken(token, item);
		if (scored === null) return null;
		score += scored;
	}
	const whole = query.trim().toLowerCase();
	const label = item.label.toLowerCase();
	if (label === whole) score += EXACT_BONUS;
	else if (label.startsWith(whole)) score += PREFIX_BONUS;
	const rank =
		item.id === undefined ? -1 : (options.recent?.indexOf(item.id) ?? -1);
	if (rank >= 0) score += RECENCY_BONUS * 0.5 ** rank;
	return score;
}

/** Whether `token` opens a word of `text` (a letter or digit run, or the start of it). */
function opensWord(token: string, text: string): boolean {
	const needle = token.toLowerCase();
	const hay = text.toLowerCase();
	for (
		let at = hay.indexOf(needle);
		at >= 0;
		at = hay.indexOf(needle, at + 1)
	) {
		if (at === 0 || !WORD_CHAR.test(hay.charAt(at - 1))) return true;
	}
	return false;
}

/** Whether every word of the query opens a word in the label, a keyword, the hint or the group. */
function matchesWordStarts(item: SearchableCommand, tokens: string[]): boolean {
	const fields = [item.label, item.hint, item.group, ...(item.keywords ?? [])];
	return tokens.every((token) =>
		fields.some((field) => field !== undefined && opensWord(token, field)),
	);
}

/**
 * The commands that answer a query, best first. A command whose words start
 * with the query's words ("import" finds "Import from Coolify") always beats
 * one that only holds the letters in order, and when any command answers that
 * way the letters-in-order matches are left out: they are the noise that put
 * "Dashboard access" first for "import". With no word-start answer the query
 * is read as an abbreviation ("dpl" finds "Deployments"): its characters in
 * order, not necessarily side by side, with a bonus for starting words and
 * for runs. Ties keep the list's own order. An empty query returns the list
 * as it came.
 */
export function searchCommands<T extends SearchableCommand>(
	items: readonly T[],
	query: string,
	options: SearchOptions = {},
): T[] {
	const tokens = tokensOf(query);
	if (tokens.length === 0) return [...items];
	const scored: Array<{
		item: T;
		score: number;
		order: number;
		wordStarts: boolean;
	}> = [];
	items.forEach((item, order) => {
		const score = scoreCommand(item, query, options);
		if (score !== null) {
			scored.push({
				item,
				score,
				order,
				wordStarts: matchesWordStarts(item, tokens),
			});
		}
	});
	const strict = scored.filter((entry) => entry.wordStarts);
	const kept = strict.length > 0 ? strict : scored;
	kept.sort((a, b) => b.score - a.score || a.order - b.order);
	return kept.map((entry) => entry.item);
}

/**
 * The label characters a query landed on, for drawing them emphasised;
 * empty when the query matched elsewhere (the hint or a keyword).
 */
export function labelMatchPositions(label: string, query: string): number[] {
	const hit = new Set<number>();
	for (const token of tokensOf(query)) {
		const found = fuzzyMatch(token, label);
		if (found) for (const position of found.positions) hit.add(position);
	}
	return [...hit].sort((a, b) => a - b);
}
