import type { QueryClient } from "@tanstack/react-query";
import { invalidateOperations } from "#/api/invalidate";

/** Everything the backups UI shows; refetched after any backup mutation. */
export const BACKUP_OPERATIONS = [
	"listBackupDestinations",
	"listBackupSchedules",
	"listBackups",
	"getBackup",
	"listBackupRestores",
	"getBackupRestore",
	"listDumpRestores",
	"getDumpRestore",
	"listServices",
	"getService",
] as const;

export function invalidateBackups(queryClient: QueryClient): Promise<void> {
	return invalidateOperations(queryClient, BACKUP_OPERATIONS);
}

/** Refetch cadence while a run is in flight (the event feed is the primary signal). */
export const IN_FLIGHT_POLL_MS = 4000;
