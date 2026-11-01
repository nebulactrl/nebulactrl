import { useNavigate, useSearch } from "@tanstack/react-router";
import { useCallback, useMemo } from "react";
import { z } from "zod";
import { parseRange, type RangeSelection, rangeParams } from "./model";

/**
 * The address state of the log views, next to `?q=`: `?logs=history` is the
 * stored history and `?logs=central` the team's central logs (live is the
 * absence of it), `?range=1h` a preset and
 * `?from=&to=` a custom range. Routes that host a log view spread this into
 * their `validateSearch`; a malformed value is dropped.
 */
export const logHistorySearch = {
	logs: z.enum(["history", "central"]).optional().catch(undefined),
	range: z.string().optional().catch(undefined),
	from: z.string().optional().catch(undefined),
	to: z.string().optional().catch(undefined),
};

export type LogsMode = "live" | "history" | "central";

type Writer = (patch: Record<string, string | undefined>) => void;

function useSearchWriter(): Writer {
	const navigate = useNavigate();
	return useCallback(
		(patch) => {
			void navigate({
				to: ".",
				search: (previous: Record<string, unknown>) => ({
					...previous,
					...patch,
				}),
				replace: true,
			} as never);
		},
		[navigate],
	);
}

function text(value: unknown): string | undefined {
	return typeof value === "string" && value !== "" ? value : undefined;
}

/** Live, History or Central logs, from `?logs=`. Switching keeps the search text. */
export function useLogsMode(): [LogsMode, (next: LogsMode) => void] {
	const search = useSearch({ strict: false }) as { logs?: unknown };
	const write = useSearchWriter();
	const mode: LogsMode =
		search.logs === "history" || search.logs === "central"
			? search.logs
			: "live";
	const set = useCallback(
		(next: LogsMode) => write({ logs: next === "live" ? undefined : next }),
		[write],
	);
	return [mode, set];
}

/** The time range, from `?range=` or `?from=&to=`. */
export function useHistoryRange(): [
	RangeSelection,
	(next: RangeSelection) => void,
] {
	const search = useSearch({ strict: false }) as {
		range?: unknown;
		from?: unknown;
		to?: unknown;
	};
	const write = useSearchWriter();
	const range = text(search.range);
	const from = text(search.from);
	const to = text(search.to);
	const selection = useMemo(
		() => parseRange({ range, from, to }),
		[range, from, to],
	);
	const set = useCallback(
		(next: RangeSelection) => write(rangeParams(next)),
		[write],
	);
	return [selection, set];
}
