import type {
	BackupView,
	DestinationView,
	ScheduleView,
} from "#/api/gen/types.gen";

/** Test-only builders for the backups panel and dialogs. */

export function destination(
	overrides: Partial<DestinationView> = {},
): DestinationView {
	return {
		accessKeySet: true,
		compatMode: false,
		createdAt: "2026-01-01T00:00:00Z",
		id: "d1",
		inUseBySchedules: 0,
		kind: "s3",
		name: "Offsite bucket",
		offSite: true,
		pathStyle: false,
		secretKeySet: true,
		storedBackups: 0,
		...overrides,
	};
}

export function backup(overrides: Partial<BackupView> = {}): BackupView {
	return {
		createdAt: "2026-01-02T03:00:00Z",
		destinationId: "d1",
		destinationKind: "s3",
		destinationName: "Offsite bucket",
		engine: "postgres",
		id: "b1",
		locked: false,
		objectDeleted: false,
		restorable: true,
		serviceId: "s1",
		sizeBytes: 2048,
		status: "succeeded",
		trigger: "manual",
		...overrides,
	};
}

export function schedule(overrides: Partial<ScheduleView> = {}): ScheduleView {
	return {
		createdAt: "2026-01-01T00:00:00Z",
		cron: "0 3 * * *",
		destinationId: "d1",
		destinationName: "Offsite bucket",
		enabled: true,
		id: "sch1",
		retentionCount: 7,
		retentionDays: 30,
		serviceId: "s1",
		timezone: "UTC",
		...overrides,
	};
}
