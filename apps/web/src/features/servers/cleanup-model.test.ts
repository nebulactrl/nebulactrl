import { describe, expect, test } from "bun:test";
import type { CleanupRun, CleanupStep } from "#/api/gen/types.gen";
import {
	type CleanupDraft,
	cleanupBody,
	cleanupDraftChanged,
	cronProblem,
	runSummary,
	stepSummary,
	toCleanupDraft,
	triggerLabel,
	validateCleanupDraft,
} from "./cleanup-model";

const run = (over: Partial<CleanupRun>): CleanupRun => ({
	id: "r1",
	mode: "standard",
	trigger: "scheduled",
	status: "succeeded",
	reclaimedBytes: 0,
	items: 0,
	createdAt: "2026-10-27T04:00:00Z",
	finishedAt: "2026-10-27T04:01:00Z",
	steps: [],
	...over,
});

describe("runSummary", () => {
	test("a finished run says what it freed", () => {
		expect(runSummary(run({ reclaimedBytes: 3 * 1024 ** 3, items: 4 }))).toBe(
			"Freed 3 GiB · 4 items removed",
		);
		expect(runSummary(run({ items: 1 }))).toBe("Freed 0 B · 1 item removed");
	});
	test("a running run is running", () => {
		expect(runSummary(run({ status: "pending" }))).toBe("Running");
	});
	test("a failed run keeps what it freed", () => {
		expect(runSummary(run({ status: "failed" }))).toBe("Failed");
		expect(
			runSummary(
				run({ status: "failed", reclaimedBytes: 1024 ** 2, items: 2 }),
			),
		).toBe("Failed · Freed 1 MiB · 2 items removed");
	});
});

describe("triggerLabel", () => {
	test("names who started it", () => {
		expect(triggerLabel(run({ trigger: "scheduled" }))).toBe("Scheduled");
		expect(triggerLabel(run({ trigger: "emergency" }))).toBe("Disk alert");
		expect(triggerLabel(run({ trigger: "manual" }))).toBe("Run by hand");
		expect(triggerLabel(run({ trigger: "manual", mode: "volumes" }))).toBe(
			"Unused volumes",
		);
	});
});

describe("stepSummary", () => {
	const step = (over: Partial<CleanupStep>): CleanupStep => ({
		name: "images_unused",
		items: 0,
		reclaimedBytes: 0,
		protected: 0,
		removed: [],
		...over,
	});
	test("counts, bytes and what was kept", () => {
		expect(
			stepSummary(step({ items: 2, reclaimedBytes: 2048, protected: 5 })),
		).toBe("2 items removed · 2 KiB · 5 kept for releases");
	});
	test("a skipped step says why", () => {
		expect(stepSummary(step({ skipped: "Kept." }))).toBe("Kept.");
	});
});

const draft = (over: Partial<CleanupDraft> = {}): CleanupDraft => ({
	cron: "0 4 * * *",
	unusedImageMinAgeHours: "72",
	buildCacheMinAgeDays: "7",
	diskTriggerPercent: "",
	...over,
});

describe("cleanup schedule draft", () => {
	test("reads the view, an unset disk trigger stays empty", () => {
		expect(
			toCleanupDraft({
				cron: "0 */6 * * *",
				unusedImageMinAgeHours: 24,
				buildCacheMinAgeDays: 2,
				diskTriggerPercent: null,
			}),
		).toEqual(
			draft({
				cron: "0 */6 * * *",
				unusedImageMinAgeHours: "24",
				buildCacheMinAgeDays: "2",
			}),
		);
		expect(
			toCleanupDraft({
				cron: "0 4 * * *",
				unusedImageMinAgeHours: 72,
				buildCacheMinAgeDays: 7,
				diskTriggerPercent: 85,
			}).diskTriggerPercent,
		).toBe("85");
	});

	test("cron shape", () => {
		expect(cronProblem("0 4 * * *")).toBeUndefined();
		expect(cronProblem("  */15  *  * * mon-fri ")).toBeUndefined();
		expect(cronProblem("@daily")).toBeUndefined();
		expect(cronProblem("")).toBeDefined();
		expect(cronProblem("0 4 * *")).toBeDefined();
		expect(cronProblem("@every 5m")).toBeDefined();
		expect(cronProblem("0 4 * * $")).toBeDefined();
	});

	test("ranges match the control plane", () => {
		expect(validateCleanupDraft(draft())).toEqual({});
		expect(validateCleanupDraft(draft({ diskTriggerPercent: "50" }))).toEqual(
			{},
		);
		const errors = validateCleanupDraft(
			draft({
				unusedImageMinAgeHours: "0",
				buildCacheMinAgeDays: "400",
				diskTriggerPercent: "40",
			}),
		);
		expect(Object.keys(errors).sort()).toEqual([
			"buildCacheMinAgeDays",
			"diskTriggerPercent",
			"unusedImageMinAgeHours",
		]);
		expect(
			validateCleanupDraft(draft({ unusedImageMinAgeHours: "1.5" }))
				.unusedImageMinAgeHours,
		).toBeDefined();
	});

	test("the body carries every setting; an empty trigger is 0", () => {
		expect(
			cleanupBody(
				{ enabled: true, removeStoppedUnmanaged: false },
				draft({ cron: " 0  3 * * 0 ", diskTriggerPercent: "70" }),
			),
		).toEqual({
			enabled: true,
			removeStoppedUnmanaged: false,
			cron: "0 3 * * 0",
			unusedImageMinAgeHours: 72,
			buildCacheMinAgeDays: 7,
			diskTriggerPercent: 70,
		});
		expect(
			cleanupBody({ enabled: false, removeStoppedUnmanaged: true }, draft())
				.diskTriggerPercent,
		).toBe(0);
	});

	test("whitespace alone is not a change", () => {
		expect(cleanupDraftChanged(draft(), draft({ cron: " 0 4  * * * " }))).toBe(
			false,
		);
		expect(
			cleanupDraftChanged(draft(), draft({ diskTriggerPercent: "80" })),
		).toBe(true);
	});
});
