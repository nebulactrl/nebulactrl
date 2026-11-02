import { describe, expect, test } from "bun:test";
import { QueryClient } from "@tanstack/react-query";
import {
	getInstanceBackupOptions,
	getInstanceUpgradeOptions,
	listInstanceBackupRunsOptions,
} from "#/api/gen/@tanstack/react-query.gen";
import { backupView, runView } from "./instance-backup.fixture";
import {
	instanceBackupQuery,
	invalidateInstanceBackup,
	RUNNING_POLL_MS,
} from "./queries";

function pollFor(data: unknown) {
	const { refetchInterval } = instanceBackupQuery();
	if (typeof refetchInterval !== "function") {
		throw new Error("refetchInterval should depend on the last run");
	}
	return refetchInterval({ state: { data } } as never);
}

describe("instanceBackupQuery refetch interval", () => {
	test("polls every RUNNING_POLL_MS while the last run is running", () => {
		expect(RUNNING_POLL_MS).toBe(4000);
		const data = backupView({ lastRun: runView({ status: "running" }) });
		expect(pollFor(data)).toBe(RUNNING_POLL_MS);
	});

	test.each([
		["succeeded", backupView({ lastRun: runView({ status: "succeeded" }) })],
		["failed", backupView({ lastRun: runView({ status: "failed" }) })],
		["no run yet", backupView()],
		["nothing loaded", undefined],
	])("does not poll when %s", (_name, data) => {
		expect(pollFor(data)).toBe(false);
	});

	test("does not retry a failed load (a non-admin gets a 403 at once)", () => {
		expect(instanceBackupQuery().retry).toBe(false);
	});
});

describe("invalidateInstanceBackup", () => {
	test("marks the settings and the run history stale, and nothing else", async () => {
		const queryClient = new QueryClient();
		const settingsKey = getInstanceBackupOptions().queryKey;
		const runsKey = listInstanceBackupRunsOptions({
			query: { limit: 20 },
		}).queryKey;
		const upgradeKey = getInstanceUpgradeOptions().queryKey;
		queryClient.setQueryData(settingsKey, backupView());
		queryClient.setQueryData(runsKey, { items: [] });
		queryClient.setQueryData(upgradeKey, {
			agentAutoUpgrade: true,
			behind: 0,
			controlPlaneVersion: "1.0.0",
			servers: [],
			upgradable: 0,
		});

		await invalidateInstanceBackup(queryClient);

		const stale = (key: readonly unknown[]) =>
			queryClient.getQueryState(key)?.isInvalidated;
		expect(stale(settingsKey)).toBe(true);
		expect(stale(runsKey)).toBe(true);
		expect(stale(upgradeKey)).toBe(false);
	});
});
