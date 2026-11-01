import { describe, expect, test } from "bun:test";
import type { InstanceBackupView } from "#/api/gen/types.gen";
import {
	dailyCron,
	dailyTime,
	destKindLabel,
	draftFor,
	draftToPatch,
	draftValid,
	keyAcknowledged,
	NOT_OFF_SITE_WARNING,
	notOffSite,
	retentionSummary,
	runPresentation,
	scheduleSummary,
} from "./model";

function view(over: Partial<InstanceBackupView> = {}): InstanceBackupView {
	return {
		enabled: false,
		schedule: "30 3 * * *",
		retentionCount: 14,
		keyFingerprint: "abc123abc123",
		localDirEnabled: true,
		toolsAvailable: true,
		updatedAt: "2026-10-27T00:00:00Z",
		destination: {
			kind: "local",
			pathStyle: false,
			compatMode: true,
			accessKeySet: false,
			secretKeySet: false,
			configured: true,
			offSite: false,
			where: "directory /var/lib/nebulactrl/backups",
		},
		notOffSite: false,
		...over,
	};
}

describe("daily schedule", () => {
	test("builds a cron from a time", () => {
		expect(dailyCron("03:30")).toBe("30 3 * * *");
		expect(dailyCron("0:05")).toBe("5 0 * * *");
		expect(dailyCron("23:59")).toBe("59 23 * * *");
	});
	test("refuses anything that is not a time", () => {
		for (const bad of [
			"",
			"3",
			"24:00",
			"12:60",
			"12:5",
			"ab:cd",
			"12:00:00",
		]) {
			expect(dailyCron(bad)).toBeUndefined();
		}
	});
	test("reads a daily cron back", () => {
		expect(dailyTime("30 3 * * *")).toBe("03:30");
		expect(dailyTime("0 0 * * *")).toBe("00:00");
		expect(dailyTime("0 3 * * 0")).toBeUndefined();
		expect(dailyTime("*/5 * * * *")).toBeUndefined();
		expect(dailyTime("61 3 * * *")).toBeUndefined();
	});
	test("round trips", () => {
		expect(dailyTime(dailyCron("04:15") ?? "")).toBe("04:15");
	});
});

describe("summaries", () => {
	test("schedule is stated in UTC", () => {
		expect(scheduleSummary("30 3 * * *")).toBe("Every day at 03:30 UTC");
	});
	test("retention", () => {
		expect(retentionSummary(14)).toBe("Keeps the newest 14 backups.");
		expect(retentionSummary(1)).toBe("Keeps the newest 1 backup.");
	});
	test("run status words", () => {
		expect(runPresentation("succeeded")).toEqual({
			kind: "healthy",
			label: "Succeeded",
		});
		expect(runPresentation("failed").kind).toBe("failed");
		expect(runPresentation("running").kind).toBe("building");
	});
});

describe("destination draft", () => {
	test("starts with empty keys", () => {
		const d = draftFor(
			view({
				destination: {
					...view().destination,
					kind: "s3",
					bucket: "nebula",
					accessKeySet: true,
					secretKeySet: true,
				},
			}),
		);
		expect(d.kind).toBe("s3");
		expect(d.bucket).toBe("nebula");
		expect(d.accessKey).toBe("");
		expect(d.secretKey).toBe("");
	});

	test("an S3 draft needs a bucket and keys", () => {
		const v = view();
		const d = { ...draftFor(v), kind: "s3" as const };
		expect(draftValid(d, v)).toBe(false);
		expect(draftValid({ ...d, bucket: "nebula" }, v)).toBe(false);
		expect(
			draftValid({ ...d, bucket: "nebula", accessKey: "a", secretKey: "b" }, v),
		).toBe(true);
		expect(
			draftValid({ ...d, bucket: "nebula", accessKey: "a", secretKey: "" }, v),
		).toBe(false);
	});

	test("stored keys count when the form leaves them empty", () => {
		const v = view({
			destination: {
				...view().destination,
				kind: "s3",
				bucket: "nebula",
				accessKeySet: true,
				secretKeySet: true,
			},
		});
		expect(draftValid(draftFor(v), v)).toBe(true);
	});

	test("a local draft needs NEBULACTRL_BACKUP_DIR", () => {
		expect(draftValid({ ...draftFor(view()), kind: "local" }, view())).toBe(
			true,
		);
		expect(
			draftValid(
				{ ...draftFor(view()), kind: "local" },
				view({ localDirEnabled: false }),
			),
		).toBe(false);
	});

	test("the patch leaves empty keys out", () => {
		const base = draftFor(view());
		const patch = draftToPatch({
			...base,
			kind: "s3",
			bucket: " nebula ",
			endpoint: " https://s3.example.com ",
		});
		expect(patch.bucket).toBe("nebula");
		expect(patch.endpoint).toBe("https://s3.example.com");
		expect("accessKey" in patch).toBe(false);
		expect("secretKey" in patch).toBe(false);
		const withKeys = draftToPatch({
			...base,
			kind: "s3",
			bucket: "b",
			accessKey: "AK",
			secretKey: "SK",
		});
		expect(withKeys.accessKey).toBe("AK");
		expect(withKeys.secretKey).toBe("SK");
	});

	test("a local patch carries no bucket or keys", () => {
		const patch = draftToPatch({
			...draftFor(view()),
			kind: "local",
			localDir: " nightly ",
			accessKey: "AK",
			bucket: "b",
		});
		expect(patch).toEqual({ kind: "local", localDir: "nightly", prefix: "" });
	});
});

describe("the built-in destination", () => {
	test("it needs nothing typed and its patch carries no bucket or keys", () => {
		const draft = { ...draftFor(view()), kind: "builtin" as const };
		expect(draftValid(draft, view())).toBe(true);
		expect(
			draftToPatch({ ...draft, bucket: "b", accessKey: "AK", prefix: " x " }),
		).toEqual({ kind: "builtin", prefix: "x" });
	});

	test("it warns that it is not off-site, in words", () => {
		expect(notOffSite(view())).toBe(false);
		expect(notOffSite(view({ notOffSite: true }))).toBe(true);
		expect(NOT_OFF_SITE_WARNING).toContain("same host as the control plane");
		expect(NOT_OFF_SITE_WARNING).toContain("external destination");
	});

	test("every kind has a name", () => {
		expect(destKindLabel("builtin")).toBe("Built-in storage");
		expect(destKindLabel("s3")).toBe("S3-compatible bucket");
		expect(destKindLabel("local")).toBe("Directory on this host");
	});
});

describe("key reminder", () => {
	test("shows until acknowledged", () => {
		expect(keyAcknowledged(view())).toBe(false);
		expect(
			keyAcknowledged(view({ keyAcknowledgedAt: "2026-10-27T01:00:00Z" })),
		).toBe(true);
	});
});
