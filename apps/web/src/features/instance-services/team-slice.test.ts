import { describe, expect, test } from "bun:test";
import { GIB } from "./model";
import {
	type BackupSlice,
	backupStateWords,
	backupUsage,
	hasBuiltinStorage,
} from "./team-slice";

const slice = (over: Partial<BackupSlice> = {}): BackupSlice => ({
	usedBytes: 10 * GIB,
	quotaBytes: 50 * GIB,
	percent: 20,
	state: "ready",
	...over,
});

describe("backupUsage", () => {
	test("reads used of quota, with the bar under the warning", () => {
		expect(backupUsage(slice())).toEqual({
			text: "10 GiB of 50 GiB",
			percent: 20,
			barValue: 20,
			warn: false,
			note: null,
		});
	});

	test("warns from 80% and tells the team about the alerts", () => {
		const usage = backupUsage(slice({ usedBytes: 40 * GIB }));
		expect(usage.warn).toBe(true);
		expect(usage.note).toMatch(/alerted at 80% and at 100%/);
	});

	test("at the quota new backups fail; the bar holds at 100 above it", () => {
		const usage = backupUsage(slice({ usedBytes: 60 * GIB }));
		expect(usage.percent).toBe(120);
		expect(usage.barValue).toBe(100);
		expect(usage.note).toMatch(/new backups fail/);
	});

	test("a full state speaks through its own detail, not a second note", () => {
		expect(
			backupUsage(slice({ usedBytes: 50 * GIB, state: "full" })).note,
		).toBeNull();
		expect(backupStateWords("full").detail).toMatch(
			/Existing backups are kept/,
		);
	});
});

describe("states", () => {
	test("each has an icon and plain words; ready needs no detail", () => {
		expect(backupStateWords("ready")).toEqual({
			kind: "live",
			label: "Ready",
			detail: "",
		});
		expect(backupStateWords("failed").label).toBe("Not working");
		expect(backupStateWords("provisioning").label).toBe("Being set up");
	});

	test("an installation without the storage has none to show", () => {
		expect(hasBuiltinStorage(slice())).toBe(true);
		expect(hasBuiltinStorage(slice({ state: "unavailable" }))).toBe(false);
		expect(hasBuiltinStorage(undefined)).toBe(false);
	});
});
