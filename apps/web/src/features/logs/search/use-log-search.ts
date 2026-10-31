import { useNavigate, useSearch } from "@tanstack/react-router";
import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { BUFFER_CAP, type BufferLine, LogBuffer } from "./buffer";
import { compileQuery, type HighlightTerm, highlightTerms } from "./match";
import { type ParsedQuery, parseQuery } from "./query";
import { loadRecent, pushRecent, saveRecent } from "./recent";

/** How long typing rests before the query is applied to the buffer. */
export const SEARCH_DEBOUNCE_MS = 120;
const ANNOUNCE_DELAY_MS = 500;

export interface UseLogSearchOptions<L extends BufferLine> {
	/** The stream's lines; the buffer keeps the newest 5,000. */
	lines: readonly L[];
	/** Leave blank lines out of the buffer and the counts. */
	skipBlank?: boolean;
	/** How many lines the buffer keeps (default 5,000, what a live tail needs). */
	cap?: number;
	/**
	 * Bump it when lines were placed before ones already seen (the stream's
	 * `epoch`): the buffer takes the lines afresh, not only the new ones.
	 */
	epoch?: number;
}

export interface LogSearch<L extends BufferLine> {
	/** The text in the box, updated on every keystroke. */
	text: string;
	setText(next: string): void;
	/** `text` parsed, for the coloured overlay. */
	parsed: ParsedQuery;
	/** The query the results are for; trails `text` by the debounce. */
	settled: ParsedQuery;
	terms: readonly HighlightTerm[];
	visible: readonly L[];
	total: number;
	/** Changes whenever the facet counts do. */
	statsVersion: number;
	stats: LogBuffer<L>["stats"];
	/** A sentence for a screen reader, set once the query settles. */
	announcement: string;
	recent: readonly string[];
	/** Remember the query as a recent one (Enter, or leaving the box). */
	commit(): void;
	clear(): void;
}

/** The query in the address: `?q=`. Empty is dropped from the URL. */
function useQueryParam(): [string, (next: string) => void] {
	const search = useSearch({ strict: false }) as { q?: unknown };
	const navigate = useNavigate();
	const value = typeof search.q === "string" ? search.q : "";
	const write = useCallback(
		(next: string) => {
			void navigate({
				to: ".",
				search: (previous: Record<string, unknown>) => ({
					...previous,
					q: next === "" ? undefined : next,
				}),
				replace: true,
			} as never);
		},
		[navigate],
	);
	return [value, write];
}

/**
 * Search over a log stream: the query text with its URL and debounce, the
 * incremental buffer with its matcher and facet counts. The query is parsed
 * once per keystroke (the box colours it), applied to the buffer once it
 * settles, and then only new lines are matched.
 */
export function useLogSearch<L extends BufferLine>({
	lines,
	skipBlank = false,
	cap = BUFFER_CAP,
	epoch = 0,
}: UseLogSearchOptions<L>): LogSearch<L> {
	const [urlQuery, writeUrl] = useQueryParam();
	const [text, setText] = useState(urlQuery);
	const parsed = useMemo(() => parseQuery(text), [text]);
	const [settled, setSettled] = useState(parsed);
	const [recent, setRecent] = useState<string[]>(loadRecent);
	const [announcement, setAnnouncement] = useState("");

	useEffect(() => {
		if (parsed === settled) return;
		const timer = setTimeout(() => setSettled(parsed), SEARCH_DEBOUNCE_MS);
		return () => clearTimeout(timer);
	}, [parsed, settled]);

	// The address follows the settled query. Our own writes come back as a
	// changed `?q=` (possibly after more typing) and are ignored; a change that
	// came from elsewhere (back, a link) goes into the box.
	const written = useRef(urlQuery);
	const mine = useRef(new Set<string>());
	useEffect(() => {
		if (settled.input === written.current) return;
		written.current = settled.input;
		mine.current.add(settled.input);
		writeUrl(settled.input);
	}, [settled.input, writeUrl]);
	const settledInput = useRef(settled.input);
	settledInput.current = settled.input;
	useEffect(() => {
		if (mine.current.delete(urlQuery)) return;
		if (urlQuery === settledInput.current) return;
		written.current = urlQuery;
		setText(urlQuery);
		setSettled(parseQuery(urlQuery));
	}, [urlQuery]);
	const matcher = useMemo(() => compileQuery(settled.ast), [settled.ast]);
	const terms = useMemo(() => highlightTerms(settled.ast), [settled.ast]);

	const buffer = useRef<LogBuffer<L>>(null);
	if (buffer.current === null) {
		buffer.current = new LogBuffer<L>(cap, skipBlank);
	}
	const seenEpoch = useRef(epoch);
	const view = useMemo(() => {
		const b = buffer.current as LogBuffer<L>;
		if (seenEpoch.current !== epoch) {
			seenEpoch.current = epoch;
			b.reset();
		}
		b.setMatcher(matcher);
		b.sync(lines);
		return { visible: b.visible, total: b.total, version: b.stats.version };
	}, [lines, matcher, epoch]);

	const counts = useRef({ shown: 0, total: 0 });
	counts.current = { shown: view.visible.length, total: view.total };
	useEffect(() => {
		if (settled.input.trim() === "") {
			setAnnouncement("");
			return;
		}
		const timer = setTimeout(() => {
			const { shown, total } = counts.current;
			setAnnouncement(`${shown} of ${total} lines match`);
		}, ANNOUNCE_DELAY_MS);
		return () => clearTimeout(timer);
	}, [settled]);

	const commit = useCallback(() => {
		if (parsed.input.trim() === "" || parsed.errors.length > 0) return;
		setRecent((current) => {
			const next = pushRecent(current, parsed.input);
			saveRecent(next);
			return next;
		});
	}, [parsed]);

	const clear = useCallback(() => {
		setText("");
		setSettled(parseQuery(""));
	}, []);

	return {
		text,
		setText,
		parsed,
		settled,
		terms,
		visible: view.visible,
		total: view.total,
		statsVersion: view.version,
		stats: (buffer.current as LogBuffer<L>).stats,
		announcement,
		recent,
		commit,
		clear,
	};
}
