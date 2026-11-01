import type { SearchData, SearchHit, SearchRecord } from "./types.ts";

// A small in-memory search over the build-time index: every query word must
// match (as a word prefix) somewhere in a chunk, and the title and heading
// count for much more than the body. A few hundred chunks, so it scans them
// all; no inverted index is worth its bytes here.

interface Prepared {
	record: SearchRecord;
	title: string[];
	heading: string[];
	text: string[];
	section: string[];
	titleLower: string;
	headingLower: string;
}

export interface SearchIndex {
	prepared: Prepared[];
}

const WORD = /[\p{L}\p{N}]+/gu;

export function tokenize(text: string): string[] {
	return text.toLowerCase().match(WORD) ?? [];
}

/** Build side: shrink the flat records into the shipped form. */
export function packSearch(records: readonly SearchRecord[]): SearchData {
	const pageIndex = new Map<string, number>();
	const data: SearchData = { pages: [], rows: [] };
	for (const [slug, anchor, title, heading, section, text] of records) {
		let index = pageIndex.get(slug);
		if (index === undefined) {
			index = data.pages.length;
			pageIndex.set(slug, index);
			data.pages.push([slug, title, section]);
		}
		data.rows.push([index, anchor, heading, text]);
	}
	return data;
}

export function unpackSearch(data: SearchData): SearchRecord[] {
	return data.rows.flatMap(([page, anchor, heading, text]) => {
		const entry = data.pages[page];
		return entry
			? [[entry[0], anchor, entry[1], heading, entry[2], text] as SearchRecord]
			: [];
	});
}

export function createIndex(records: readonly SearchRecord[]): SearchIndex {
	return {
		prepared: records.map((record) => ({
			record,
			title: tokenize(record[2]),
			heading: tokenize(record[3]),
			text: tokenize(record[5]),
			section: tokenize(record[4]),
			titleLower: record[2].toLowerCase(),
			headingLower: record[3].toLowerCase(),
		})),
	};
}

/** 2 for a whole-word match, 1 for a prefix match, 0 for none. */
function wordMatch(words: readonly string[], term: string): number {
	let best = 0;
	for (const word of words) {
		if (word === term) return 2;
		if (word.startsWith(term)) best = 1;
	}
	return best;
}

const W_TITLE = 12;
const W_HEADING = 8;
const W_TEXT = 2;
const W_SECTION = 0.5;

function score(
	entry: Prepared,
	terms: readonly string[],
	phrase: string,
): number {
	let total = 0;
	for (const term of terms) {
		const title = wordMatch(entry.title, term);
		const heading = wordMatch(entry.heading, term);
		const text = wordMatch(entry.text, term);
		const section = wordMatch(entry.section, term);
		if (title + heading + text + section === 0) return 0;
		total +=
			(title * W_TITLE +
				heading * W_HEADING +
				text * W_TEXT +
				section * W_SECTION) /
			2;
	}
	if (phrase.length > 2) {
		if (entry.titleLower.includes(phrase)) total += 10;
		if (entry.headingLower.includes(phrase)) total += 6;
	}
	// The lead of a page is what a reader most likely wants when the title matched.
	if (entry.record[1] === "") total += 1.5;
	// Generated reference pages repeat words from every guide; guides lead.
	if (entry.record[4] === "Reference") total *= 0.7;
	return total;
}

function snippet(record: SearchRecord, terms: readonly string[]): string {
	const text = record[5];
	if (text === "") return "";
	const lower = text.toLowerCase();
	let at = -1;
	for (const term of terms) {
		const found = lower.indexOf(term);
		if (found >= 0 && (at < 0 || found < at)) at = found;
	}
	const from = at < 0 ? 0 : Math.max(0, at - 48);
	const to = Math.min(text.length, from + 150);
	return `${from > 0 ? "…" : ""}${text.slice(from, to).trim()}${to < text.length ? "…" : ""}`;
}

export interface SearchOptions {
	limit?: number;
	/** At most this many hits from one page. */
	perPage?: number;
}

export function searchDocs(
	index: SearchIndex,
	query: string,
	{ limit = 8, perPage = 2 }: SearchOptions = {},
): SearchHit[] {
	const terms = tokenize(query);
	if (terms.length === 0) return [];
	const phrase = terms.join(" ");
	const scored: SearchHit[] = [];
	for (const entry of index.prepared) {
		const value = score(entry, terms, phrase);
		if (value <= 0) continue;
		const [slug, anchor, pageTitle, heading, section] = entry.record;
		scored.push({
			slug,
			anchor,
			pageTitle,
			heading,
			section,
			snippet: snippet(entry.record, terms),
			score: value,
		});
	}
	scored.sort(
		(a, b) => b.score - a.score || a.pageTitle.localeCompare(b.pageTitle),
	);
	const taken = new Map<string, number>();
	const hits: SearchHit[] = [];
	for (const hit of scored) {
		const count = taken.get(hit.slug) ?? 0;
		if (count >= perPage) continue;
		taken.set(hit.slug, count + 1);
		hits.push(hit);
		if (hits.length >= limit) break;
	}
	return hits;
}
