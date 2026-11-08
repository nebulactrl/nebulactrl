import { useQuery } from "@tanstack/react-query";
import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { getEnvironmentLogHistoryOptions } from "#/api/gen/@tanstack/react-query.gen";
import { getEnvironmentLogHistory } from "#/api/gen/sdk.gen";
import type { LogHistoryEntry, LogHistoryPage } from "#/api/gen/types.gen";
import { type LogLine, type SseStatus, useLogStream } from "#/api/sse";
import { type RangeSelection, toLogLine } from "#/features/logs/history/model";
import { useHistoryLogs } from "#/features/logs/history/use-history-logs";
import { type LogSource, sourcesParam } from "./sources";

/** The live view keeps this many lines; the oldest go first. */
export const LIVE_BUFFER = 5000;
/** How often the live view asks for new request lines. */
export const REQUEST_POLL_MS = 2000;
/** What the first poll looks back over. */
const FIRST_POLL_LOOKBACK_MS = 60_000;
/** Lines one request asks for. */
const POLL_PAGE_LIMIT = 500;
/** Pages one poll follows when more request lines arrived than one holds. */
const MAX_PAGES_PER_POLL = 10;

// --- Polling the request log ----------------------------------------------------

/**
 * How far before the newest line seen a poll looks again. A request line is
 * stamped when its response ends, but a host hands its lines to the server in
 * batches, and the hosts of an environment do not batch together: a line can
 * reach the store after later ones have been shown.
 */
const REQUEST_OVERLAP_MS = 30_000;

export interface RequestPollState {
	/** Where the first poll began, in epoch milliseconds. */
	floor: number | undefined;
	/** The newest line time seen, in epoch milliseconds. */
	newest: number | undefined;
	/** The lines seen within the overlap window (service, time, line), each with its time. */
	seen: Map<string, number>;
}

export function newRequestPollState(): RequestPollState {
	return { floor: undefined, newest: undefined, seen: new Map() };
}

export interface RequestPollerOptions {
	/** One page of request lines at or after `from`, oldest first. */
	fetchPage(args: {
		from: string;
		cursor: string | undefined;
		signal: AbortSignal;
	}): Promise<LogHistoryPage>;
	/** The lines of a page that were not seen before (maybe none), and the page itself. */
	onEntries(entries: LogHistoryEntry[], page: LogHistoryPage): void;
	onError(error: unknown): void;
	/**
	 * What the poll has seen. A poller started with the state of an earlier one
	 * goes on where it stopped (a resumed pause) and does not show lines again.
	 */
	state?: RequestPollState | undefined;
	/** The clock, in epoch milliseconds. */
	now?: () => number;
	intervalMs?: number;
}

function entryKey(entry: LogHistoryEntry): string {
	return `${entry.serviceId}\u0000${entry.ts}\u0000${entry.line}`;
}

/**
 * Asks for request lines at every turn from `REQUEST_OVERLAP_MS` before the
 * newest one seen, at once and then every `intervalMs` while the tab is
 * visible (a tab that becomes visible asks again right away). Lines the
 * overlap brings back are dropped by (service, time, line), which are kept
 * for as long as a poll can still return them; lines that arrived late come
 * through. Returns the function that stops it and abandons a request in flight.
 */
export function startRequestPoller({
	fetchPage,
	onEntries,
	onError,
	state = newRequestPollState(),
	now = Date.now,
	intervalMs = REQUEST_POLL_MS,
}: RequestPollerOptions): () => void {
	const abort = new AbortController();
	let stopped = false;
	let busy = false;

	function unseen(items: readonly LogHistoryEntry[]): LogHistoryEntry[] {
		const fresh: LogHistoryEntry[] = [];
		for (const entry of items) {
			const ms = Date.parse(entry.ts);
			const key = entryKey(entry);
			if (state.seen.has(key)) continue;
			state.seen.set(key, ms);
			if (
				!Number.isNaN(ms) &&
				(state.newest === undefined || ms > state.newest)
			) {
				state.newest = ms;
			}
			fresh.push(entry);
		}
		return fresh;
	}

	async function poll() {
		if (stopped || busy || document.visibilityState !== "visible") return;
		busy = true;
		try {
			// The first poll looks back a minute; until a line shows up, so do the
			// later ones (and a resumed one, over the time it was paused).
			state.floor ??= now() - FIRST_POLL_LOOKBACK_MS;
			const from =
				state.newest === undefined
					? state.floor
					: state.newest - REQUEST_OVERLAP_MS;
			// What a poll from here on cannot return is not kept.
			for (const [key, ms] of state.seen) {
				if (ms < from) state.seen.delete(key);
			}
			const fromText = new Date(from).toISOString();
			let cursor: string | undefined;
			for (let pages = 0; pages < MAX_PAGES_PER_POLL; pages++) {
				const page = await fetchPage({
					from: fromText,
					cursor,
					signal: abort.signal,
				});
				if (stopped) return;
				onEntries(unseen(page.items ?? []), page);
				if (!page.nextCursor) break;
				cursor = page.nextCursor;
			}
		} catch (error) {
			if (!stopped) onError(error);
		} finally {
			busy = false;
		}
	}

	const wake = () => void poll();
	const timer = setInterval(wake, intervalMs);
	document.addEventListener("visibilitychange", wake);
	wake();
	return () => {
		stopped = true;
		abort.abort();
		clearInterval(timer);
		document.removeEventListener("visibilitychange", wake);
	};
}

interface RequestLog {
	/** Oldest first. */
	lines: readonly LogLine[];
	/** The first poll has answered (or failed). */
	loaded: boolean;
	unavailable: LogHistoryPage["unavailable"];
	error: unknown;
}

const NO_REQUESTS: RequestLog = {
	lines: [],
	loaded: false,
	unavailable: undefined,
	error: undefined,
};

/** A line's time in milliseconds; one that has none sorts first. */
function msOf(line: LogLine): number {
	const ms = Date.parse(line.ts ?? "");
	return Number.isNaN(ms) ? Number.NEGATIVE_INFINITY : ms;
}

function byTime(a: LogLine, b: LogLine): number {
	const x = msOf(a);
	const y = msOf(b);
	return x < y ? -1 : x > y ? 1 : 0;
}

/**
 * `added` placed into `lines` by time (both oldest first). A line that
 * arrives late lands among the older ones; of equal times, the line that was
 * there first stays first.
 */
export function insertByTime(
	lines: readonly LogLine[],
	added: readonly LogLine[],
): readonly LogLine[] {
	if (added.length === 0) return lines;
	const sorted = added.length > 1 ? [...added].sort(byTime) : added;
	const last = lines[lines.length - 1];
	if (!last || byTime(last, sorted[0] as LogLine) <= 0) {
		return lines.concat(sorted);
	}
	const merged: LogLine[] = [];
	let i = 0;
	let j = 0;
	while (i < lines.length || j < sorted.length) {
		const takeOld =
			j >= sorted.length ||
			(i < lines.length &&
				byTime(lines[i] as LogLine, sorted[j] as LogLine) <= 0);
		merged.push((takeOld ? lines[i++] : sorted[j++]) as LogLine);
	}
	return merged;
}

/**
 * The environment's request lines since the poll started, oldest first,
 * capped at the live buffer. `polling` is whether the poll runs; `enabled`
 * whether the lines are wanted at all: a poll that is only paused keeps its
 * lines and what it has seen, so a resume goes on from there. `attempt` starts
 * the poll again (a retry).
 */
function useRequestLines(
	environmentId: string,
	enabled: boolean,
	polling: boolean,
	attempt: number,
): { log: RequestLog; clear(): void } {
	const [log, setLog] = useState<{ environmentId: string } & RequestLog>({
		environmentId,
		...NO_REQUESTS,
	});
	const pollState = useRef<RequestPollState>(newRequestPollState());
	const counter = useRef(0);

	// Another environment, or the lines no longer wanted: start over.
	// biome-ignore lint/correctness/useExhaustiveDependencies: `enabled` is the trigger
	useEffect(() => {
		pollState.current = newRequestPollState();
		setLog({ environmentId, ...NO_REQUESTS });
	}, [environmentId, enabled]);

	// biome-ignore lint/correctness/useExhaustiveDependencies: `attempt` is the retry trigger
	useEffect(() => {
		if (!polling) return;
		return startRequestPoller({
			state: pollState.current,
			fetchPage: async ({ from, cursor, signal }) => {
				const { data } = await getEnvironmentLogHistory({
					path: { environmentId },
					query: {
						sources: "requests",
						order: "asc",
						from,
						limit: POLL_PAGE_LIMIT,
						...(cursor === undefined ? {} : { cursor }),
					},
					signal,
					throwOnError: true,
				});
				return data;
			},
			onEntries: (entries, page) =>
				setLog((current) => {
					const unavailable = page.unavailable?.length
						? page.unavailable
						: undefined;
					if (
						entries.length === 0 &&
						current.loaded &&
						current.error === undefined &&
						current.unavailable === undefined &&
						unavailable === undefined
					) {
						return current;
					}
					const added = entries.map((entry) =>
						toLogLine(entry, ++counter.current),
					);
					const lines = insertByTime(current.lines, added);
					return {
						environmentId,
						lines:
							lines.length > LIVE_BUFFER ? lines.slice(-LIVE_BUFFER) : lines,
						loaded: true,
						unavailable,
						error: undefined,
					};
				}),
			onError: (error) =>
				setLog((current) => ({ ...current, loaded: true, error })),
		});
	}, [environmentId, polling, attempt]);

	// The poll's seen lines outlive the clear, so cleared lines stay gone.
	const clear = useCallback(
		() =>
			setLog((current) =>
				current.lines.length === 0 ? current : { ...current, lines: [] },
			),
		[],
	);

	// A render before the effect resets the state still belongs to the last environment.
	return {
		log: enabled && log.environmentId === environmentId ? log : NO_REQUESTS,
		clear,
	};
}

// --- Merging the two live sources -----------------------------------------------

/** A line's place in time; one with no time of its own stays behind the line before it. */
function timeKeys(lines: readonly LogLine[]): number[] {
	let carry = Number.NEGATIVE_INFINITY;
	return lines.map((line) => {
		const ms = line.ts === undefined ? Number.NaN : Date.parse(line.ts);
		if (!Number.isNaN(ms)) carry = ms;
		return carry;
	});
}

/** The gap below which two ids are no longer told apart. */
const MIN_ID_GAP = 1e-9;

/**
 * The live runtime lines and the polled request lines as one list, oldest
 * first (equal times: runtime first), numbered for `LogView` and the search
 * buffer. The two sources number their lines themselves, so each line is
 * copied once with an id of the merge's own. A line keeps its copy and its id
 * while it stays, so an open line detail and the scroll position survive: a
 * request line that lands between runtime lines takes an id between its
 * neighbours, as `mergeByTime` does. `epoch` changes when the list is
 * replaced (a stream was cleared or restarted, another key): whatever indexed
 * the lines by position starts over then. `reorder` changes when lines landed
 * among the ones shown or went from the middle while the rest stayed: an index
 * takes the difference only. Lines added at the end and dropped from the
 * front change neither.
 */
export class LiveMerger {
	epoch = 0;
	/** Changes when lines landed among the ones shown, or went from the middle. */
	reorder = 0;
	/** How many lines were numbered so far: new ones, wherever they landed. */
	arrived = 0;
	private key = "";
	/** The highest id handed out. */
	private top = 0;
	private placed = new WeakMap<LogLine, LogLine>();
	private shown: readonly LogLine[] = [];

	/** Forgets every line; the next merge numbers them afresh under a new epoch. */
	private restart() {
		this.epoch += 1;
		this.top = 0;
		this.placed = new WeakMap();
		this.shown = [];
	}

	/** Copies of `kept` with ids that keep the list ascending; undefined when two ids ran out of room between them. */
	private number(kept: readonly LogLine[]): LogLine[] | undefined {
		const lines: LogLine[] = [];
		let low = 0;
		let i = 0;
		while (i < kept.length) {
			const known = this.placed.get(kept[i] as LogLine);
			if (known) {
				lines.push(known);
				low = known.id;
				i += 1;
				continue;
			}
			let end = i;
			while (end < kept.length && !this.placed.has(kept[end] as LogLine)) {
				end += 1;
			}
			const next = kept[end];
			const high = next ? this.placed.get(next)?.id : undefined;
			const step = high === undefined ? 1 : (high - low) / (end - i + 1);
			if (step < MIN_ID_GAP) return undefined;
			for (let k = i; k < end; k++) {
				const id = high === undefined ? ++this.top : low + step * (k - i + 1);
				const copy = { ...(kept[k] as LogLine), id };
				this.placed.set(kept[k] as LogLine, copy);
				this.arrived += 1;
				lines.push(copy);
			}
			low = lines[lines.length - 1]?.id ?? low;
			i = end;
		}
		return lines;
	}

	/** `key` names what the lines are of (environment, sources): another key starts over. */
	merge(
		key: string,
		runtime: readonly LogLine[],
		requests: readonly LogLine[],
	): readonly LogLine[] {
		if (key !== this.key) {
			this.key = key;
			this.restart();
		}
		const ordered: LogLine[] = [];
		const runtimeKeys = timeKeys(runtime);
		const requestKeys = timeKeys(requests);
		let r = 0;
		let q = 0;
		while (r < runtime.length || q < requests.length) {
			const takeRuntime =
				q >= requests.length ||
				(r < runtime.length &&
					(runtimeKeys[r] as number) <= (requestKeys[q] as number));
			ordered.push((takeRuntime ? runtime[r++] : requests[q++]) as LogLine);
		}
		const kept =
			ordered.length > LIVE_BUFFER ? ordered.slice(-LIVE_BUFFER) : ordered;

		// Intact: what carried over is the end of what was shown, in order, with
		// every new line after it.
		let retained = 0;
		let firstRetained: LogLine | undefined;
		let lastId = 0;
		let sawNew = false;
		let ascending = true;
		let intact = true;
		for (const source of kept) {
			const copy = this.placed.get(source);
			if (!copy) {
				sawNew = true;
				continue;
			}
			if (sawNew) intact = false;
			if (copy.id <= lastId) ascending = false;
			lastId = copy.id;
			firstRetained ??= copy;
			retained += 1;
		}
		if (
			this.shown.length > 0 &&
			(retained === 0 ||
				firstRetained !== this.shown[this.shown.length - retained])
		) {
			intact = false;
		}
		// Retained lines out of order cannot keep their ids. Nothing retained is
		// a replaced list; otherwise the lines that stayed keep their ids and
		// their place in whatever indexed them.
		if (!ascending) this.restart();
		else if (!intact) {
			if (retained === 0) this.epoch += 1;
			else this.reorder += 1;
		}

		let lines = this.number(kept);
		if (!lines) {
			this.restart();
			lines = this.number(kept) as LogLine[];
		}
		this.shown = lines;
		return lines;
	}
}

// --- The hook -------------------------------------------------------------------

export interface ExplorerLogs {
	/** Oldest first, merged by time across sources. */
	lines: readonly LogLine[];
	mode: "live" | "history";
	loading: boolean;
	error: unknown;
	/** History: a page stopped early. */
	partial: boolean;
	unavailable: readonly { serverId: string; serverName?: string }[];
	/** History: older lines are on the server. */
	hasOlder: boolean;
	fetchOlder(): void;
	/** Changes when the line set is replaced (pass it to `useLogSearch`). */
	epoch: number;
	/** Changes when lines landed among the ones shown (pass it to `useLogSearch`). */
	reorder: number;
	/**
	 * Live: the state of the feed (the runtime stream, or the request poll when
	 * the runtime is not searched). History: always "ended".
	 */
	status: SseStatus;
	/** Live: the stream and the poll are closed and the lines shown are the ones at Pause. */
	paused: boolean;
	setPaused(next: boolean): void;
	/** Live: empties the list; lines already seen do not come back, not after Pause and Resume either. */
	clear(): void;
	/**
	 * Asks again. History: from a new "now" for a range that reaches it (also
	 * after a failure). Live: starts the request poll again.
	 */
	refresh(): void;
	/**
	 * Whether the runtime log is being stored on the servers asked: false means
	 * stored counts and searches are empty and the lines shown are all there is.
	 * Undefined until known, or when the runtime is not one of the sources.
	 */
	capturing: boolean | undefined;
	/** Live: lines that arrived since `setAtBottom(true)` was last called. Zero in history. */
	newCount: number;
	/** Tell the hook whether the reader is at the end of the list. */
	setAtBottom(atBottom: boolean): void;
}

export interface UseExplorerLogsOptions {
	environmentId: string;
	sources: readonly LogSource[];
	mode: "live" | "history";
	range: RangeSelection;
	/** The settled query text: history sends it as `q`, live filters in the browser. */
	query: string;
}

const NO_LINES: readonly LogLine[] = [];
const NO_UNAVAILABLE: ExplorerLogs["unavailable"] = [];

/** How long a capture answer is good for. */
const CAPTURING_STALE_MS = 5 * 60_000;

/**
 * Whether the runtime log is stored, from the newest stored line's page. A
 * page that asks for the requests as well answers for either, so this asks
 * for the runtime alone.
 */
function useRuntimeCapturing(
	environmentId: string,
	enabled: boolean,
): boolean | undefined {
	const { data } = useQuery({
		...getEnvironmentLogHistoryOptions({
			path: { environmentId },
			query: { limit: 1 },
		}),
		enabled,
		refetchInterval: false,
		refetchOnWindowFocus: false,
		staleTime: CAPTURING_STALE_MS,
		gcTime: CAPTURING_STALE_MS,
		retry: false,
	});
	return data?.capturing;
}

/** The time of the newest line that has one, in epoch milliseconds. */
function newestMs(lines: readonly LogLine[]): number | undefined {
	for (let i = lines.length - 1; i >= 0; i--) {
		const ts = (lines[i] as LogLine).ts;
		const ms = ts === undefined ? Number.NaN : Date.parse(ts);
		if (!Number.isNaN(ms)) return ms;
	}
	return undefined;
}

/** The lines after `cutoff` (epoch milliseconds); all of them when there is none. */
function afterCutoff(
	lines: readonly LogLine[],
	cutoff: number | undefined,
): readonly LogLine[] {
	const first = lines[0];
	if (cutoff === undefined || !first) return lines;
	// Lines are in time order: when the first is past the cutoff, all are.
	if (first.ts !== undefined && Date.parse(first.ts) > cutoff) return lines;
	const at = timeKeys(lines).findIndex((ms) => ms > cutoff);
	return at === -1 ? NO_LINES : lines.slice(at);
}

/**
 * An environment's log, live or from the stored history, of the runtime
 * output, the requests, or both. Live: the runtime stream plus the request
 * lines polled every 2 s while the tab is visible, merged by time in one
 * 5,000-line buffer. History: the stored lines of the range and query, older
 * pages on demand. Only the active mode asks the server for anything.
 */
export function useExplorerLogs({
	environmentId,
	sources,
	mode,
	range,
	query,
}: UseExplorerLogsOptions): ExplorerLogs {
	const live = mode === "live";
	const withRuntime = sources.includes("runtime");
	const withRequests = sources.includes("requests");
	const sourceKey = sourcesParam(sources);
	// Pause closes the stream and the poll; the lines stay on screen until Resume.
	const [paused, setPausedState] = useState(false);
	useEffect(() => {
		if (!live) setPausedState(false);
	}, [live]);
	const streaming = live && !paused;
	const [attempt, setAttempt] = useState(0);
	const retry = useCallback(() => setAttempt((n) => n + 1), []);

	const stream = useLogStream({
		kind: "environment",
		id: environmentId,
		enabled: streaming && withRuntime,
		maxLines: LIVE_BUFFER,
	});
	const { log: requests, clear: clearRequests } = useRequestLines(
		environmentId,
		live && withRequests,
		streaming && withRequests,
		attempt,
	);
	const history = useHistoryLogs({
		scope: { kind: "environment", id: environmentId },
		range,
		query,
		sources,
		enabled: !live,
	});
	// A history of the runtime alone says it itself; anything else asks.
	const historyAnswers = !live && withRuntime && !withRequests;
	const probed = useRuntimeCapturing(
		environmentId,
		withRuntime && !historyAnswers,
	);
	let capturing: boolean | undefined;
	if (withRuntime) capturing = historyAnswers ? history.capturing : probed;

	// Mutated while rendering, but a merge of the same lines again changes nothing.
	const [merger] = useState(() => new LiveMerger());
	// What the live list showed last, and what a paused one keeps showing.
	const lastShown = useRef<readonly LogLine[]>(NO_LINES);
	const [frozen, setFrozen] = useState<readonly LogLine[]>(NO_LINES);
	const setPaused = useCallback((next: boolean) => {
		if (next) setFrozen(lastShown.current);
		setPausedState(next);
	}, []);
	// Lines at or before this time were cleared and stay gone, even when the
	// stream replays them after a Resume or the poll looks back over them.
	const [cleared, setCleared] = useState<{
		environmentId: string;
		ms: number;
	}>();
	useEffect(() => {
		if (!live) setCleared(undefined);
	}, [live]);
	const cutoff =
		cleared?.environmentId === environmentId ? cleared.ms : undefined;
	const liveLines = useMemo(() => {
		if (!live) return NO_LINES;
		if (paused) return frozen;
		const merged = merger.merge(
			`${environmentId}|${sourceKey}`,
			afterCutoff(withRuntime ? stream.lines : NO_LINES, cutoff),
			afterCutoff(requests.lines, cutoff),
		);
		lastShown.current = merged;
		return merged;
	}, [
		merger,
		live,
		paused,
		frozen,
		environmentId,
		sourceKey,
		withRuntime,
		stream.lines,
		requests.lines,
		cutoff,
	]);

	// Clear empties what is shown from now on: lines already seen do not come back.
	const { clear: clearStream } = stream;
	const clear = useCallback(() => {
		const ms = newestMs(paused ? frozen : lastShown.current);
		if (ms !== undefined) {
			setCleared((current) => ({
				environmentId,
				ms:
					current?.environmentId === environmentId
						? Math.max(current.ms, ms)
						: ms,
			}));
		}
		if (paused) {
			setFrozen(NO_LINES);
			return;
		}
		clearStream();
		clearRequests();
	}, [environmentId, paused, frozen, clearStream, clearRequests]);

	// One counter over both modes: it moves when the mode, the environment or
	// the sources change, and whenever the active list itself says it was replaced.
	const sequence = useRef({ key: "", value: 0 });
	const source = live ? merger.epoch : history.epoch;
	const epochKey = `${mode}|${environmentId}|${sourceKey}|${source}`;
	if (sequence.current.key !== epochKey) {
		sequence.current = { key: epochKey, value: sequence.current.value + 1 };
	}
	const epoch = sequence.current.value;

	// Lines that arrived while the reader was away from the end of the list.
	const [atBottom, setAtBottom] = useState(true);
	const baseline = useRef({ epoch, arrived: merger.arrived });
	if (atBottom || baseline.current.epoch !== epoch) {
		baseline.current = { epoch, arrived: merger.arrived };
	}
	const newCount =
		live && !atBottom
			? Math.min(merger.arrived - baseline.current.arrived, liveLines.length)
			: 0;

	if (!live) {
		return {
			lines: history.lines,
			mode,
			loading: history.loading,
			error: history.error,
			partial: history.continuation.kind === "partial",
			unavailable: history.unavailable,
			hasOlder:
				history.continuation.kind === "more" ||
				history.continuation.kind === "partial",
			fetchOlder: history.loadOlder,
			epoch,
			reorder: 0,
			status: "ended",
			paused: false,
			setPaused,
			clear: noop,
			refresh: history.refresh,
			capturing,
			newCount,
			setAtBottom,
		};
	}
	const runtimePending =
		streaming &&
		withRuntime &&
		(stream.status === "connecting" || stream.status === "waiting");
	const requestsPending = streaming && withRequests && !requests.loaded;
	let status = stream.status;
	if (!withRuntime) {
		status =
			requests.error !== undefined
				? "paused"
				: requests.loaded
					? "live"
					: "connecting";
	}
	return {
		lines: liveLines,
		mode,
		loading: liveLines.length === 0 && (runtimePending || requestsPending),
		error: requests.error,
		partial: false,
		unavailable: requests.unavailable ?? NO_UNAVAILABLE,
		hasOlder: false,
		fetchOlder: noop,
		epoch,
		reorder: merger.reorder,
		status,
		paused,
		setPaused,
		clear,
		refresh: retry,
		capturing,
		newCount,
		setAtBottom,
	};
}

function noop() {
	// Nothing to do: the history has no older page to load and nothing to clear.
}
