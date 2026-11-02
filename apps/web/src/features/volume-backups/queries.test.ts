import { describe, expect, test } from "bun:test";
import { QueryClient, QueryObserver } from "@tanstack/react-query";
import { invalidateVolumeBackups } from "./queries";

const SHOWN_BY_VOLUME_BACKUPS = [
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
];

/** A cached query keyed the way the generated options key them: `[{ _id, ... }]`. */
function seed(client: QueryClient, id: string): void {
	client.setQueryData([{ _id: id, path: { serviceId: "svc-1" } }], { id });
}

function isStale(client: QueryClient, id: string): boolean {
	const query = client.getQueryCache().find({
		queryKey: [{ _id: id, path: { serviceId: "svc-1" } }],
	});
	if (!query) throw new Error(`${id} was not seeded`);
	return query.state.isInvalidated;
}

describe("invalidateVolumeBackups", () => {
	test("marks every query the volume backups UI shows as stale", async () => {
		const client = new QueryClient();
		for (const id of SHOWN_BY_VOLUME_BACKUPS) seed(client, id);
		await invalidateVolumeBackups(client);
		for (const id of SHOWN_BY_VOLUME_BACKUPS)
			expect(isStale(client, id)).toBe(true);
	});

	test("leaves unrelated queries alone", async () => {
		const client = new QueryClient();
		seed(client, "listVolumeBackups");
		seed(client, "listDomains");
		seed(client, "getProject");
		client.setQueryData(["plain-key"], 1);
		await invalidateVolumeBackups(client);
		expect(isStale(client, "listVolumeBackups")).toBe(true);
		expect(isStale(client, "listDomains")).toBe(false);
		expect(isStale(client, "getProject")).toBe(false);
		expect(client.getQueryState(["plain-key"])?.isInvalidated).toBe(false);
	});

	test("refetches a mounted query and resolves once it has finished", async () => {
		const client = new QueryClient();
		let calls = 0;
		const watch = new QueryObserver(client, {
			queryKey: [{ _id: "listVolumeBackups" }],
			queryFn: async () => ++calls,
		});
		const stop = watch.subscribe(() => {});
		await watch.refetch();
		expect(calls).toBe(1);
		await invalidateVolumeBackups(client);
		expect(calls).toBe(2);
		expect(watch.getCurrentResult().data).toBe(2);
		stop();
	});
});
