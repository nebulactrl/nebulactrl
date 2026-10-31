import { type QueryClient, useQuery } from "@tanstack/react-query";
import { getDatabaseReplicationOptions } from "#/api/gen/@tanstack/react-query.gen";
import { invalidateOperations, invalidateServices } from "#/api/invalidate";
import { replicationPollInterval } from "./model";

/** How often the panel refetches while a replica is being made or a promote runs. */
export const REPLICATION_POLL_MS = 3_000;

/** Otherwise: lag moves, but nothing is waiting on it. */
export const REPLICATION_IDLE_MS = 15_000;

/**
 * The replication block of a managed Postgres service. Shared by the panel,
 * the Upgrade/Rotate guards and the canvas chips, so one request serves all.
 */
export function useReplication(serviceId: string, enabled = true) {
	return useQuery({
		...getDatabaseReplicationOptions({ path: { serviceId } }),
		enabled,
		retry: false,
		refetchInterval: (query) =>
			replicationPollInterval(
				query.state.data,
				REPLICATION_POLL_MS,
				REPLICATION_IDLE_MS,
			),
	});
}

/**
 * After a replica change: the replication views, the database views and
 * everything that shows a service (a replica is a service of its own; a
 * promote restages the consumers).
 */
export async function invalidateReplication(
	queryClient: QueryClient,
): Promise<void> {
	await Promise.all([
		invalidateOperations(queryClient, [
			"getDatabaseReplication",
			"getDatabase",
		]),
		invalidateServices(queryClient),
	]);
}
