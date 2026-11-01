import { describe, expect, test } from "bun:test";
import {
	backupDays,
	backupPresentation,
	backupSuccessShare,
	canRestore,
	describeCron,
	describeRetention,
	destinationWhere,
	inFlight,
	presetFor,
	restorePresentation,
} from "./model";

describe("describeCron", () => {
	test("macros and common shapes read as words", () => {
		expect(describeCron("@hourly")).toBe("Every hour");
		expect(describeCron("@daily")).toBe("Every day at 00:00");
		expect(describeCron("0 3 * * *")).toBe("Every day at 03:00");
		expect(describeCron("30 14 * * 1")).toBe("Every Monday at 14:30");
		expect(describeCron("0 3 * * 7")).toBe("Every Sunday at 03:00");
		expect(describeCron("*/15 * * * *")).toBe("Every 15 minutes");
		expect(describeCron("5 * * * *")).toBe("Every hour at minute 5");
		expect(describeCron("0 4 15 * *")).toBe(
			"On day 15 of every month at 04:00",
		);
	});

	test("anything unusual is shown as written, never mis-described", () => {
		expect(describeCron("0 3 * * mon-fri")).toBe("0 3 * * mon-fri");
		expect(describeCron("0 0,12 * * *")).toBe("0 0,12 * * *");
		expect(describeCron("nope")).toBe("nope");
	});
});

test("describeRetention", () => {
	expect(describeRetention(7, 0)).toBe("Keep the newest 7");
	expect(describeRetention(7, 30)).toBe(
		"Keep the newest 7, delete after 30 days",
	);
	expect(describeRetention(0, 1)).toBe("Delete after 1 day");
	expect(describeRetention(0, 0)).toBe("Keep everything");
});

test("presetFor matches presets exactly", () => {
	expect(presetFor("@hourly")).toBe("hourly");
	expect(presetFor("0 3 * * *")).toBe("daily");
	expect(presetFor("0 3 * * 0")).toBe("weekly");
	expect(presetFor("0 5 * * *")).toBe("custom");
});

test("statuses always carry a text label next to the tone", () => {
	for (const s of [
		"queued",
		"running",
		"succeeded",
		"failed",
		"skipped",
		"expired",
	]) {
		expect(backupPresentation(s).label.length).toBeGreaterThan(0);
	}
	expect(backupPresentation("running").pulse).toBe(true);
	expect(backupPresentation("succeeded").pulse).toBe(false);
	expect(backupPresentation("weird").label).toBe("weird");
	expect(restorePresentation("failed").tone).toBe("bad");
	expect(inFlight("queued")).toBe(true);
	expect(inFlight("restoring")).toBe(true);
	expect(inFlight("succeeded")).toBe(false);
	expect(inFlight("skipped")).toBe(false);
});

test("only engines the agent can load are restorable", () => {
	expect(canRestore("postgres")).toBe(true);
	expect(canRestore("mongodb")).toBe(true);
	expect(canRestore("valkey")).toBe(false);
	expect(canRestore("clickhouse")).toBe(false);
});

test("destinationWhere", () => {
	expect(destinationWhere({ kind: "local", localDir: "nightly" })).toBe(
		"Agent backup directory/nightly",
	);
	expect(destinationWhere({ kind: "local" })).toBe("Agent backup directory");
	expect(
		destinationWhere({
			kind: "s3",
			endpoint: "https://s3.example.com",
			bucket: "b",
			prefix: "nebula",
		}),
	).toBe("s3.example.com · b/nebula");
	expect(destinationWhere({ kind: "s3", bucket: "b" })).toBe("AWS S3 · b");
	expect(destinationWhere({ kind: "builtin" })).toBe("Built-in backup storage");
});

describe("backupDays", () => {
	const now = new Date("2026-10-26T10:00:00Z");
	const run = (status: string, createdAt: string) => ({ status, createdAt });

	test("one entry per UTC day, oldest first, ending today", () => {
		const days = backupDays([], 3, now);
		expect(days.map((d) => d.date)).toEqual([
			"2026-10-24",
			"2026-10-25",
			"2026-10-26",
		]);
		expect(days.every((d) => d.status === "unknown")).toBe(true);
	});

	test("failed beats skipped beats succeeded; in-flight runs have no verdict", () => {
		const days = backupDays(
			[
				run("succeeded", "2026-10-26T01:00:00Z"),
				run("failed", "2026-10-26T02:00:00Z"),
				run("succeeded", "2026-10-25T01:00:00Z"),
				run("skipped", "2026-10-25T02:00:00Z"),
				run("running", "2026-10-24T09:00:00Z"),
			],
			3,
			now,
		);
		expect(days.map((d) => d.status)).toEqual(["unknown", "degraded", "down"]);
		expect(days[2]?.detail).toBe("1 succeeded, 1 failed");
		expect(days[1]?.detail).toBe("1 succeeded, 1 skipped");
	});

	test("an undelivered (expired) run reads as skipped", () => {
		expect(
			backupDays([run("expired", "2026-10-26T00:10:00Z")], 1, now)[0]?.status,
		).toBe("degraded");
	});

	test("days are cut at UTC midnight, not local time", () => {
		const days = backupDays([run("succeeded", "2026-10-25T23:59:59Z")], 2, now);
		expect(days.map((d) => d.status)).toEqual(["up", "unknown"]);
	});

	test("a truncated list does not claim older days had no backup", () => {
		const days = backupDays(
			[run("succeeded", "2026-10-25T08:00:00Z")],
			3,
			now,
			true,
		);
		expect(days[0]).toEqual({
			date: "2026-10-24",
			status: "unknown",
			detail: "Older runs are not loaded",
		});
		expect(days[2]?.detail).toBeUndefined();
	});
});

describe("backupSuccessShare", () => {
	const now = new Date("2026-10-26T10:00:00Z");
	test("succeeded over finished runs in the window", () => {
		const runs = [
			{ status: "succeeded", createdAt: "2026-10-26T01:00:00Z" },
			{ status: "succeeded", createdAt: "2026-10-26T02:00:00Z" },
			{ status: "succeeded", createdAt: "2026-10-26T03:00:00Z" },
			{ status: "failed", createdAt: "2026-10-26T04:00:00Z" },
			{ status: "running", createdAt: "2026-10-26T05:00:00Z" },
			{ status: "failed", createdAt: "2026-01-01T00:00:00Z" },
		];
		expect(backupSuccessShare(runs, backupDays(runs, 30, now))).toBe(0.75);
	});
	test("undefined with no verdict", () => {
		const runs = [{ status: "queued", createdAt: "2026-10-26T01:00:00Z" }];
		expect(backupSuccessShare(runs, backupDays(runs, 30, now))).toBeUndefined();
	});
});
