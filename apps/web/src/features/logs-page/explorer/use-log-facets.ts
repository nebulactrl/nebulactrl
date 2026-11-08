import { keepPreviousData, useQuery } from "@tanstack/react-query";
import { useCallback, useMemo } from "react";
import { getEnvironmentLogFacetsOptions } from "#/api/gen/@tanstack/react-query.gen";
import type { LogFacetsPage } from "#/api/gen/types.gen";
import {
	type RangeSelection,
	reachesNow,
	resolveRange,
} from "#/features/logs/history/model";
import { useNowAnchor } from "#/features/logs/history/use-now-anchor";
import { type LogSource, sourcesParam } from "./sources";

/** How often a live view asks again: the least between two asks, however many lines arrive. */
export const LIVE_FACETS_REFRESH_MS = 10_000;

export interface UseLogFacetsOptions {
	environmentId: string;
	sources: readonly LogSource[];
	/**
	 * The range the counts cover. Ignored while `live`.
	 */
	range: RangeSelection;
	/** The settled search query; the server counts only the lines it matches. */
	query: string;
	/** The volume histogram's bucket count; the server's default when left out. */
	buckets?: number | undefined;
	/**
	 * The live view: counts over the last 15 minutes up to now, asked again
	 * every 10 s. The request names no range, so the server's own (the last 15
	 * minutes) rolls with the clock and the query stays one query between asks.
	 * Otherwise the range is pinned to the moment it was chosen, or the view
	 * last became active.
	 */
	live?: boolean | undefined;
	/** The live view is paused: its counts stop moving, a new query still asks once. */
	paused?: boolean | undefined;
	enabled?: boolean | undefined;
}

export interface LogFacets {
	data: LogFacetsPage | undefined;
	isFetching: boolean;
	/** No counts yet and the first ask is in flight. */
	isLoading: boolean;
	error: unknown;
	/** Asks again: from a new "now" for a range that reaches it. */
	refresh(): void;
}

/**
 * The server's facet counts and volume histogram for the explorer's search:
 * level, status and the other built-in facets, the `@attributes`, and the
 * matching lines over time. They cover the whole range, not only the lines
 * loaded. The previous counts stay up while a new search or range loads.
 */
export function useLogFacets({
	environmentId,
	sources,
	range,
	query,
	buckets,
	live = false,
	paused = false,
	enabled = true,
}: UseLogFacetsOptions): LogFacets {
	// The moment "now" means for this range; a new range, or a history that was
	// waiting behind the live view, starts from a new now, so the counts and the
	// history list of one range are asked for alike.
	const rangeSignature = JSON.stringify(range);
	const [anchorAt, reanchor] = useNowAnchor(rangeSignature, enabled && !live);
	const resolved = useMemo(
		() => (live ? undefined : resolveRange(range, anchorAt)),
		[live, range, anchorAt],
	);

	const search = query.trim();
	const result = useQuery({
		...getEnvironmentLogFacetsOptions({
			path: { environmentId },
			query: {
				...(resolved?.from ? { from: resolved.from } : {}),
				...(resolved ? { to: resolved.to } : {}),
				...(search === "" ? {} : { q: search }),
				sources: sourcesParam(sources),
				...(buckets === undefined ? {} : { buckets }),
			},
		}),
		enabled,
		placeholderData: keepPreviousData,
		// Live counts move on a timer, not with the lines: every ask scans every
		// server. A pinned range's do not move, and the app polls every query by default.
		refetchInterval: live && !paused ? LIVE_FACETS_REFRESH_MS : false,
		refetchOnWindowFocus: !(live && paused),
		staleTime: live ? 0 : Number.POSITIVE_INFINITY,
		gcTime: 60_000,
		// A 503 (server offline) or 409 (old agent) will not change by asking again.
		retry: false,
	});

	const { refetch } = result;
	const refresh = useCallback(() => {
		if (!live && reachesNow(range)) {
			reanchor();
		} else {
			void refetch();
		}
	}, [live, range, reanchor, refetch]);

	return {
		data: result.data,
		isFetching: result.isFetching,
		isLoading: result.isLoading,
		error: result.error,
		refresh,
	};
}
