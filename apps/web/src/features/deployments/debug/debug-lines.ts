import type { LogLine } from "#/api/sse";
import { type LogLevel, parseLevel } from "#/features/logs/level";
import type { HighlightTerm } from "#/features/logs/search/match";

export const DEBUG_STREAMS = ["build", "deploy", "runtime", "debug"] as const;

export const DEBUG_LEVELS: readonly LogLevel[] = [
	"error",
	"warn",
	"info",
	"debug",
];

export interface DebugLineFilter {
	/** Streams to show; empty shows none. */
	streams: ReadonlySet<string>;
	/** Levels to show; empty shows none. */
	levels: ReadonlySet<LogLevel>;
	/** Whitespace-separated words that must all appear (any case). */
	query: string;
}

/** Every stream and level, no search: nothing is filtered out. */
export const NO_FILTER: DebugLineFilter = {
	streams: new Set<string>(DEBUG_STREAMS),
	levels: new Set<LogLevel>(DEBUG_LEVELS),
	query: "",
};

/**
 * The level a line states. A line that states none is info: the filter has to
 * put it somewhere, and the viewer still colours nothing for it.
 */
export function levelOf(line: Pick<LogLine, "line">): LogLevel {
	return parseLevel(line.line)?.level ?? "info";
}

/** The words of a search, lower case; empty for a blank query. */
export function searchWords(query: string): string[] {
	return query.toLowerCase().split(/\s+/).filter(Boolean);
}

/** Search words as the viewer marks them in each line. */
export function highlightWords(query: string): HighlightTerm[] {
	return searchWords(query).map((word) => ({ parts: [word] }));
}

/** The lines that pass every filter, in order. */
export function filterDebugLines(
	lines: readonly LogLine[],
	filter: DebugLineFilter,
): LogLine[] {
	const words = searchWords(filter.query);
	return lines.filter((line) => {
		if (line.stream !== undefined && !filter.streams.has(line.stream)) {
			return false;
		}
		if (!filter.levels.has(levelOf(line))) return false;
		if (words.length === 0) return true;
		const text = line.line.toLowerCase();
		return words.every((word) => text.includes(word));
	});
}

/** How many lines each stream holds, for the filter chips. */
export function streamCounts(
	lines: readonly LogLine[],
): Record<string, number> {
	const counts: Record<string, number> = {};
	for (const line of lines) {
		const stream = line.stream ?? "deploy";
		counts[stream] = (counts[stream] ?? 0) + 1;
	}
	return counts;
}

/** Flips one member of a set, returning a new set. */
export function toggled<T>(set: ReadonlySet<T>, value: T): Set<T> {
	const next = new Set(set);
	if (!next.delete(value)) next.add(value);
	return next;
}
