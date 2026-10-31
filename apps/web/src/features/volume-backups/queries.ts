import type { QueryClient } from "@tanstack/react-query";
import { invalidateOperations } from "#/api/invalidate";

/** Everything the volume backups UI shows; refetched after any mutation. */
export const VOLUME_BACKUP_OPERATIONS = [
	"getVolumeBackupConfig",
	"listVolumeBackups",
	"getVolumeBackup",
	"listVolumeRestores",
	"listRetiredVolumes",
	"listBackupDestinations",
	"listVolumes",
	"listPendingChanges",
	"listServices",
	"getService",
] as const;

export function invalidateVolumeBackups(
	queryClient: QueryClient,
): Promise<void> {
	return invalidateOperations(queryClient, VOLUME_BACKUP_OPERATIONS);
}

/** Refetch cadence while a run is in flight (the event feed is the primary signal). */
export const IN_FLIGHT_POLL_MS = 4000;

/** The newest runs the panel loads. */
export const VOLUME_BACKUP_LIMIT = 50;
