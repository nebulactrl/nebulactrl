import type {
	InstanceBackupView,
	InstanceDestinationView,
	InstanceRunView,
} from "#/api/gen/types.gen";
import { type Overrides, withOverrides } from "#/test/fixture";

/** A configured, off-site S3 destination with stored keys. */
export function s3Destination(
	over: Overrides<InstanceDestinationView> = {},
): InstanceDestinationView {
	return withOverrides(
		{
			kind: "s3",
			endpoint: "https://s3.example.com",
			region: "eu-west-1",
			bucket: "nebula-backups",
			prefix: "prod",
			pathStyle: true,
			compatMode: true,
			accessKeySet: true,
			secretKeySet: true,
			configured: true,
			offSite: true,
			where: "s3://nebula-backups/prod",
		},
		over,
	);
}

/** The instance backup settings as the API returns them; an S3 destination by default. */
export function backupView(
	over: Overrides<InstanceBackupView> = {},
): InstanceBackupView {
	return withOverrides(
		{
			enabled: true,
			schedule: "30 3 * * *",
			retentionCount: 14,
			keyFingerprint: "ab12cd34ef56",
			keyAcknowledgedAt: "2026-10-01T00:00:00Z",
			localDirEnabled: true,
			toolsAvailable: true,
			updatedAt: "2026-10-27T00:00:00Z",
			destination: s3Destination(),
			notOffSite: false,
		},
		over,
	);
}

export function runView(over: Partial<InstanceRunView> = {}): InstanceRunView {
	return {
		id: "run-1",
		status: "succeeded",
		trigger: "schedule",
		destKind: "s3",
		destination: "s3://nebula-backups/prod/2026-10-27.dump",
		sizeBytes: 2048,
		objectDeleted: false,
		startedAt: "2026-10-27T03:30:00Z",
		...over,
	};
}

/** An RFC 9457 problem reply. */
export const problem = (status: number, detail: string) => ({
	status,
	body: { status, title: "Error", detail, code: "error" },
});
