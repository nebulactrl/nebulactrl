import {
	type InfiniteData,
	keepPreviousData,
	useInfiniteQuery,
} from "@tanstack/react-query";
import { useCallback, useMemo, useState } from "react";
import {
	getEnvironmentLogHistoryInfiniteQueryKey,
	getServiceLogHistoryInfiniteQueryKey,
} from "#/api/gen/@tanstack/react-query.gen";
import {
	getEnvironmentLogHistory,
	getServiceLogHistory,
} from "#/api/gen/sdk.gen";
import type {
	LogHistoryPage,
	LogHistoryUnavailable,
} from "#/api/gen/types.gen";
import type { LogLine } from "#/api/sse";
import {
	type Continuation,
	continuation,
	epochOf,
	mergePages,
	type RangeSelection,
	reachesNow,
	resolveRange,
} from "./model";

/** What one request asks for; the server's own default is 500, its maximum 2,000. */
const PAGE_LIMIT = 500;

export interface HistoryScope {
	kind: "service" | "environment";
	id: string;
	/** Compose stacks only: one component's lines of a service. */
	component?: string | undefined;
	/** Services with processes only: one process' lines of a service. */
	process?: string | undefined;
}

export interface UseHistoryLogsOptions {
	scope: HistoryScope;
	range: RangeSelection;
	/** The settled search query. It is sent to the server, which evaluates it. */
	query: string;
}

export interface HistoryLogs {
	/** Every line loaded, oldest first. */
	lines: LogLine[];
	/** Which result the lines belong to; changes with a new search, range or refresh. */
	epoch: number;
	/** No page yet and nothing to show in its place. */
	loading: boolean;
	/** The lines are those of the previous search while the new one loads. */
	stale: boolean;
	/** Any request in flight, including a refresh. */
	fetching: boolean;
	error: unknown;
	/** False: history capture is switched off on that server. */
	capturing: boolean | undefined;
	/** The oldest line the server still has. */
	oldest: string | undefined;
	/** Services whose history could not be read (environment view). */
	unavailable: readonly LogHistoryUnavailable[];
	/** Parts of the query the server ignored. */
	queryErrors: readonly string[];
	continuation: Continuation;
	/** The next older page is loading. */
	loadingOlder: boolean;
	loadOlder(): void;
	/** Ask again: from a new "now" for a range that reaches it, else the same range. */
	refresh(): void;
}

const NO_LINES: LogLine[] = [];
const NO_ITEMS: readonly never[] = [];

type Pages = InfiniteData<LogHistoryPage, string | undefined>;

/** The request's query with the component or process the scope narrows to. */
function withScope<T extends object>(
	query: T,
	scope: Pick<HistoryScope, "component" | "process">,
): T & { component?: string; process?: string } {
	return {
		...query,
		...(scope.component ? { component: scope.component } : {}),
		...(scope.process ? { process: scope.process } : {}),
	};
}

/**
 * The stored runtime log of a service or an environment, newest page first
 * with older pages on demand. The request carries everything that changes the
 * result (scope, range, query), so a new search is a new query. Nothing here
 * refetches by itself: stored history does not change under the reader, and a
 * range that reaches "now" is pinned to the moment it was asked for until
 * Refresh asks again.
 */
export function useHistoryLogs({
	scope,
	range,
	query,
}: UseHistoryLogsOptions): HistoryLogs {
	// The moment "now" means for this range. A new range starts from a new now.
	const rangeSignature = JSON.stringify(range);
	const [anchor, setAnchor] = useState(() => ({
		signature: rangeSignature,
		at: Date.now(),
	}));
	let anchorAt = anchor.at;
	if (anchor.signature !== rangeSignature) {
		const next = { signature: rangeSignature, at: Date.now() };
		setAnchor(next);
		anchorAt = next.at;
	}
	const resolved = useMemo(
		() => resolveRange(range, anchorAt),
		[range, anchorAt],
	);

	const search = query.trim();
	const params = {
		...(resolved.from ? { from: resolved.from } : {}),
		to: resolved.to,
		...(search === "" ? {} : { q: search }),
		limit: PAGE_LIMIT,
	};
	const queryKey =
		scope.kind === "service"
			? getServiceLogHistoryInfiniteQueryKey({
					path: { serviceId: scope.id },
					query: withScope(params, scope),
				})
			: getEnvironmentLogHistoryInfiniteQueryKey({
					path: { environmentId: scope.id },
					query: params,
				});

	const result = useInfiniteQuery<
		LogHistoryPage,
		Error,
		Pages,
		readonly unknown[],
		string | undefined
	>({
		queryKey,
		queryFn: async ({ pageParam, signal }) => {
			const page = {
				...params,
				...(pageParam === undefined ? {} : { cursor: pageParam }),
			};
			if (scope.kind === "service") {
				const { data } = await getServiceLogHistory({
					path: { serviceId: scope.id },
					query: withScope(page, scope),
					signal,
					throwOnError: true,
				});
				return data;
			}
			const { data } = await getEnvironmentLogHistory({
				path: { environmentId: scope.id },
				query: page,
				signal,
				throwOnError: true,
			});
			return data;
		},
		initialPageParam: undefined,
		getNextPageParam: (last) => last.nextCursor,
		placeholderData: keepPreviousData,
		// Stored lines do not change, and the app polls every query by default.
		refetchInterval: false,
		staleTime: Number.POSITIVE_INFINITY,
		gcTime: 60_000,
		// A 503 (server offline) or 409 (old agent) will not change by asking again.
		retry: false,
	});

	const pages = result.data?.pages;
	const newest = pages?.[0];
	const epoch = newest ? epochOf(newest) : 0;
	const lines = useMemo(
		() => (pages && pages.length > 0 ? mergePages(pages, epoch) : NO_LINES),
		[pages, epoch],
	);
	const next = useMemo(
		() =>
			pages ? continuation(pages, lines.length) : { kind: "end" as const },
		[pages, lines.length],
	);

	const { fetchNextPage, refetch } = result;
	const loadOlder = useCallback(() => {
		void fetchNextPage();
	}, [fetchNextPage]);
	const refresh = useCallback(() => {
		if (reachesNow(range)) {
			setAnchor({ signature: rangeSignature, at: Date.now() });
		} else {
			void refetch();
		}
	}, [range, rangeSignature, refetch]);

	return {
		lines,
		epoch,
		loading: result.isPending && !result.isError,
		stale: result.isPlaceholderData,
		fetching: result.isFetching,
		error: result.error,
		capturing: newest?.capturing,
		oldest: newest?.oldest,
		unavailable: newest?.unavailable ?? NO_ITEMS,
		queryErrors: newest?.queryErrors ?? NO_ITEMS,
		continuation: next,
		loadingOlder: result.isFetchingNextPage,
		loadOlder,
		refresh,
	};
}
