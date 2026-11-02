import type {
	BackupSliceView,
	DestinationView,
	Server,
} from "#/api/gen/types.gen";
import { make } from "#/test/factories";

/** A destination as the control plane lists it; override what a test cares about. */
export function destination(
	over: Partial<DestinationView> & { id: string; name: string },
): DestinationView {
	return {
		accessKeySet: true,
		compatMode: true,
		createdAt: "2026-01-01T00:00:00Z",
		inUseBySchedules: 0,
		kind: "s3",
		offSite: true,
		pathStyle: false,
		secretKeySet: true,
		storedBackups: 0,
		...over,
	};
}

/** An S3 destination on R2 with a bucket and a prefix. */
export const R2: DestinationView = destination({
	id: "dest-r2",
	name: "R2 nightly",
	endpoint: "https://acct.r2.cloudflarestorage.com",
	bucket: "backups",
	prefix: "prod",
	region: "auto",
	pathStyle: true,
});

/** A directory on the agent's host. */
export const LOCAL: DestinationView = destination({
	id: "dest-local",
	name: "Server disk",
	kind: "local",
	localDir: "nightly",
	offSite: false,
});

/** A server as `GET /servers` lists it: only its identity and status are set, the rest is the default scene's. */
export function server(
	id: string,
	name: string,
	status: Server["status"] = "online",
): Server {
	return make("Server", { id, name, status });
}

/** The team's built-in storage slice, as GET /team-slice answers. */
export function teamSlice(state: BackupSliceView["state"]) {
	return make("TeamSliceView", {
		backup: {
			state,
			usedBytes: 1024 * 1024,
			quotaBytes: 1024 * 1024 * 10,
			percent: 10,
		},
	});
}
