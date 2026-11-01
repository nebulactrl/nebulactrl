import type { QueryClient } from "@tanstack/react-query";
import { listLogDrainsOptions } from "#/api/gen/@tanstack/react-query.gen";
import { invalidateOperations } from "#/api/invalidate";

/** Agents report a drain's health every 30 s; the list is re-read twice as often. */
const DRAIN_REFRESH_MS = 15_000;

/** The drain list, with its per-server health, refreshed on a timer as well as by events. */
export const logDrainsQuery = () => ({
	...listLogDrainsOptions(),
	refetchInterval: DRAIN_REFRESH_MS,
});

/** Everything the drains UI shows; refetched after any drain mutation. */
const LOG_DRAIN_OPERATIONS = ["listLogDrains", "getLogDrain"] as const;

export function invalidateLogDrains(queryClient: QueryClient): Promise<void> {
	return invalidateOperations(queryClient, LOG_DRAIN_OPERATIONS);
}
