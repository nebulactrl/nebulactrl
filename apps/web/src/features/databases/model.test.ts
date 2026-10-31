import { describe, expect, test } from "bun:test";
import type { UpgradePlan } from "#/api/gen/types.gen";
import {
	canConfirmUpgrade,
	canRotate,
	databasePollInterval,
	primaryExport,
	REFRESH_TIMEOUT_MS,
	ROTATION_CONFIRM,
	refreshBlocker,
	refreshStatus,
	rotationLine,
	upgradeBanner,
} from "./model";

const plan = (patch: Partial<UpgradePlan> = {}): UpgradePlan => ({
	blockers: null,
	downtime: "a few minutes",
	engine: "postgres",
	executable: true,
	from: "17",
	to: "18",
	kind: "upgrade",
	method: "pg_upgrade",
	steps: ["Stop the database"],
	warnings: null,
	...patch,
});

describe("refreshStatus", () => {
	const request = { at: 1_000, baseline: "2026-01-01T00:00:00Z" };
	test("is idle with no request", () => {
		expect(refreshStatus(null, "x", 5_000)).toBe("idle");
	});
	test("is pending until the sample's timestamp changes", () => {
		expect(refreshStatus(request, request.baseline, 2_000)).toBe("pending");
		expect(refreshStatus(request, "2026-01-01T00:00:09Z", 2_000)).toBe("idle");
	});
	test("is unanswered after the timeout", () => {
		expect(
			refreshStatus(request, request.baseline, 1_000 + REFRESH_TIMEOUT_MS),
		).toBe("unanswered");
	});
	test("a first sample ends a refresh that had none", () => {
		const first = { at: 0, baseline: undefined };
		expect(refreshStatus(first, undefined, 10)).toBe("pending");
		expect(refreshStatus(first, "2026-01-01T00:00:00Z", 10)).toBe("idle");
	});
});

describe("refreshBlocker", () => {
	test("needs a deployed, running database", () => {
		expect(
			refreshBlocker({ desiredState: "running" }, { deployed: false }),
		).toMatch(/Not deployed/);
		expect(
			refreshBlocker({ desiredState: "stopped" }, { deployed: true }),
		).toMatch(/Stopped/);
		expect(
			refreshBlocker({ desiredState: "running" }, { deployed: true }),
		).toBeNull();
	});
});

describe("databasePollInterval", () => {
	test("polls while something is in flight", () => {
		expect(databasePollInterval(undefined, false, 3000)).toBe(false);
		expect(databasePollInterval(undefined, true, 3000)).toBe(3000);
		expect(
			databasePollInterval(
				{
					rotation: {
						id: "r",
						mode: "exec",
						status: "pending",
						createdAt: "",
					},
				},
				false,
				3000,
			),
		).toBe(3000);
		expect(
			databasePollInterval(
				{
					rotation: {
						id: "r",
						mode: "exec",
						status: "applied",
						createdAt: "",
					},
				},
				false,
				3000,
			),
		).toBe(false);
	});
});

describe("primaryExport", () => {
	test("prefers the primary, else the first", () => {
		const a = {
			key: "A",
			description: "",
			primary: false,
			reference: "",
			secret: true,
		};
		const b = { ...a, key: "B", primary: true };
		expect(primaryExport({ exports: [a, b] })?.key).toBe("B");
		expect(primaryExport({ exports: [a] })?.key).toBe("A");
		expect(primaryExport({ exports: null })).toBeUndefined();
	});
});

describe("rotation", () => {
	test("the confirm label follows the mode", () => {
		expect(ROTATION_CONFIRM.exec).toBe("Rotate password now");
		expect(ROTATION_CONFIRM.redeploy).toBe("Stage new password");
		expect(ROTATION_CONFIRM.variable).toBe("Stage new password");
	});
	test("blockers lock the rotation", () => {
		const base = {
			mode: "exec" as const,
			passwordKey: "P",
			consumers: null,
			databaseRestart: false,
			warnings: null,
		};
		expect(canRotate(undefined)).toBe(false);
		expect(canRotate({ ...base, blockers: null })).toBe(true);
		expect(canRotate({ ...base, blockers: ["not deployed"] })).toBe(false);
	});
	test("the last rotation reads as pending, failed (with its error) or applied", () => {
		expect(rotationLine(undefined)).toBeNull();
		const rotation = { id: "r", mode: "exec", createdAt: "" };
		expect(rotationLine({ ...rotation, status: "pending" })?.tone).toBe(
			"pending",
		);
		expect(
			rotationLine({ ...rotation, status: "failed", error: "auth failed" })
				?.text,
		).toContain("auth failed");
		expect(rotationLine({ ...rotation, status: "applied" })?.tone).toBe(
			"applied",
		);
	});
});

describe("canConfirmUpgrade", () => {
	test("needs an executable upgrade plan and the slug typed", () => {
		expect(canConfirmUpgrade(undefined, "db", "db")).toBe(false);
		expect(canConfirmUpgrade(plan(), "", "db")).toBe(false);
		expect(canConfirmUpgrade(plan(), "DB", "db")).toBe(false);
		expect(canConfirmUpgrade(plan(), "db", "db")).toBe(true);
	});
	test("blockers, manual methods and downgrades never confirm", () => {
		expect(canConfirmUpgrade(plan({ blockers: ["busy"] }), "db", "db")).toBe(
			false,
		);
		expect(
			canConfirmUpgrade(
				plan({ executable: false, method: "dump_restore" }),
				"db",
				"db",
			),
		).toBe(false);
		expect(canConfirmUpgrade(plan({ kind: "downgrade" }), "db", "db")).toBe(
			false,
		);
	});
});

describe("upgradeBanner", () => {
	const upgrade = {
		id: "u",
		createdAt: "",
		fromVersion: "17",
		toVersion: "18",
		method: "pg_upgrade",
		oldVolume: "data",
		newVolume: "data-18",
	};
	test("ready with a staged change tells you to deploy and names the kept volume", () => {
		const banner = upgradeBanner(
			{ ...upgrade, status: "ready" },
			"PostgreSQL",
			true,
		);
		expect(banner?.tone).toBe("ready");
		expect(banner?.text).toBe(
			"Deploy to start PostgreSQL 18; the previous volume “data” is kept.",
		);
	});
	test("ready with nothing staged is done", () => {
		expect(
			upgradeBanner({ ...upgrade, status: "ready" }, "PostgreSQL", false)?.tone,
		).toBe("done");
	});
	test("pending and failed", () => {
		expect(
			upgradeBanner({ ...upgrade, status: "pending" }, "PostgreSQL", false)
				?.tone,
		).toBe("pending");
		const failed = upgradeBanner(
			{ ...upgrade, status: "failed", error: "disk full" },
			"PostgreSQL",
			false,
		);
		expect(failed?.tone).toBe("failed");
		expect(failed?.text).toContain("disk full");
		expect(upgradeBanner(undefined, "PostgreSQL", false)).toBeNull();
	});
});
