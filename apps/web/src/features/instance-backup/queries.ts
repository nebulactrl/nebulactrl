import type { QueryClient } from "@tanstack/react-query";
import { queryOptions } from "@tanstack/react-query";
import { getInstanceBackupOptions } from "#/api/gen/@tanstack/react-query.gen";
import { invalidateOperations } from "#/api/invalidate";

/** Refetch cadence while a backup runs (the event feed is the primary signal). */
export const RUNNING_POLL_MS = 4000;

/** The control-plane backup settings and its newest run. Instance admin only. */
export const instanceBackupQuery = () =>
	queryOptions({
		...getInstanceBackupOptions(),
		staleTime: 30_000,
		retry: false,
		refetchInterval: (query) =>
			query.state.data?.lastRun?.status === "running" ? RUNNING_POLL_MS : false,
	});

const INSTANCE_BACKUP_OPERATIONS = [
	"getInstanceBackup",
	"listInstanceBackupRuns",
] as const;

export function invalidateInstanceBackup(
	queryClient: QueryClient,
): Promise<void> {
	return invalidateOperations(queryClient, INSTANCE_BACKUP_OPERATIONS);
}
