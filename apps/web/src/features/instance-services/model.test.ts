import { describe, expect, test } from "bun:test";
import { ApiError } from "#/api/client-config";
import type { SliceView } from "#/api/gen/types.gen";
import {
	bytesToGib,
	canEditQuota,
	GIB,
	gibText,
	parseQuotaGib,
	purgeIn,
	quotaSource,
	serviceStatus,
	servicesLoadFailure,
	sizeText,
	sliceStatus,
	teamRows,
	totals,
	usageNote,
	usedPercent,
} from "./model";

function slice(over: Partial<SliceView> = {}): SliceView {
	return {
		id: "s1",
		teamId: "t1",
		teamName: "Acme",
		kind: "s3",
		usedBytes: 0,
		quotaBytes: null,
		effectiveQuotaBytes: 50 * GIB,
		state: "ready",
		message: "",
		deletedAt: null,
		purgeAt: null,
		...over,
	};
}

describe("quota text", () => {
	test("GiB convert to bytes and back", () => {
		expect(bytesToGib(50 * GIB)).toBe(50);
		expect(gibText(50 * GIB)).toBe("50");
		expect(gibText(Math.round(1.5 * GIB))).toBe("1.5");
	});

	test("the minimum is 1 GiB, like the control plane's", () => {
		expect(parseQuotaGib("1")).toEqual({ ok: true, bytes: GIB });
		expect(parseQuotaGib(" 50 ")).toEqual({ ok: true, bytes: 50 * GIB });
		expect(parseQuotaGib("12.5")).toEqual({ ok: true, bytes: 12.5 * GIB });
		expect(parseQuotaGib("0.5")).toMatchObject({
			ok: false,
			message: expect.stringContaining("1 GiB"),
		});
		expect(parseQuotaGib("0")).toMatchObject({ ok: false });
	});

	test("anything that is not a plain number is refused", () => {
		for (const text of [
			"",
			"  ",
			"abc",
			"-5",
			"5 GiB",
			"1e3",
			"1.234",
			"1,5",
		]) {
			expect(parseQuotaGib(text).ok).toBe(false);
		}
		expect(parseQuotaGib("99999999999").ok).toBe(false);
	});

	test("sizes read in binary units without a trailing .0", () => {
		expect(sizeText(0)).toBe("0 B");
		expect(sizeText(50 * GIB)).toBe("50 GiB");
		expect(sizeText(Math.round(12.4 * GIB))).toBe("12.4 GiB");
		expect(sizeText(512 * 1024 ** 2)).toBe("512 MiB");
	});
});

describe("usedPercent", () => {
	test("is a whole percentage and can pass 100", () => {
		expect(usedPercent(25 * GIB, 50 * GIB)).toBe(50);
		expect(usedPercent(60 * GIB, 50 * GIB)).toBe(120);
		expect(usedPercent(0, 50 * GIB)).toBe(0);
	});

	test("a missing quota is 0, not a division by zero", () => {
		expect(usedPercent(5, 0)).toBe(0);
	});
});

describe("states", () => {
	test("a service and a slice each have an icon and plain words", () => {
		expect(serviceStatus("ready")).toEqual({ kind: "live", label: "Ready" });
		expect(serviceStatus("failed").label).toBe("Not working");
		expect(sliceStatus("full")).toEqual({ kind: "warning", label: "Full" });
		expect(sliceStatus("provisioning").label).toBe("Being set up");
	});
});

describe("teamRows", () => {
	test("one row per team, live teams by name, then deleted ones by purge date", () => {
		const rows = teamRows([
			slice({ id: "a", teamId: "tz", teamName: "Zed" }),
			slice({
				id: "b",
				teamId: "td2",
				teamName: "Gone later",
				deletedAt: "2026-10-20T00:00:00Z",
				purgeAt: "2026-11-19T00:00:00Z",
			}),
			slice({ id: "c", teamId: "ta", teamName: "Alpha" }),
			slice({
				id: "d",
				teamId: "td1",
				teamName: "Gone first",
				deletedAt: "2026-10-01T00:00:00Z",
				purgeAt: "2026-10-31T00:00:00Z",
			}),
		]);
		expect(rows.map((row) => row.teamName)).toEqual([
			"Alpha",
			"Zed",
			"Gone first",
			"Gone later",
		]);
		expect(rows.map((row) => row.deleted)).toEqual([false, false, true, true]);
	});

	test("the quota comes from the backup slice and the state is the worst of all", () => {
		const [row] = teamRows([
			slice({ id: "s3", kind: "s3", usedBytes: 10 * GIB }),
			slice({ id: "reg", kind: "registry", state: "failed", message: "down" }),
			slice({ id: "log", kind: "loki", state: "provisioning" }),
		]);
		expect(row?.backup?.id).toBe("s3");
		expect(row?.state).toBe("failed");
		expect(row?.message).toBe("Image registry: down");
	});

	test("full outranks being set up", () => {
		const [row] = teamRows([
			slice({ kind: "s3", state: "full" }),
			slice({ id: "log", kind: "loki", state: "provisioning" }),
		]);
		expect(row?.state).toBe("full");
	});

	test("a team without a backup slice yet has no quota to edit", () => {
		const [row] = teamRows([slice({ kind: "registry" })]);
		expect(row?.backup).toBeUndefined();
		expect(row && canEditQuota(row)).toBe(false);
	});

	test("only a live team with a backup slice can have its quota edited", () => {
		const [live] = teamRows([slice()]);
		const [gone] = teamRows([
			slice({
				deletedAt: "2026-10-01T00:00:00Z",
				purgeAt: "2026-10-31T00:00:00Z",
			}),
		]);
		expect(live && canEditQuota(live)).toBe(true);
		expect(gone && canEditQuota(gone)).toBe(false);
	});

	test("the purge date is the earliest of the team's slices", () => {
		const [row] = teamRows([
			slice({
				deletedAt: "2026-10-01T00:00:00Z",
				purgeAt: "2026-10-31T00:00:00Z",
			}),
			slice({
				id: "r",
				kind: "registry",
				deletedAt: "2026-10-01T00:00:00Z",
				purgeAt: "2026-10-30T00:00:00Z",
			}),
		]);
		expect(row?.purgeAt).toBe("2026-10-30T00:00:00Z");
	});
});

describe("quota source and warnings", () => {
	test("a null quota is the default", () => {
		expect(quotaSource(slice({ quotaBytes: null }))).toBe("Default");
		expect(quotaSource(slice({ quotaBytes: 10 * GIB }))).toBe(
			"Set for this team",
		);
		expect(quotaSource(undefined)).toBe("");
	});

	test("the note appears at 80% and says new backups fail at 100%", () => {
		const note = (usedGib: number, state: SliceView["state"] = "ready") => {
			const [row] = teamRows([slice({ usedBytes: usedGib * GIB, state })]);
			return row ? usageNote(row) : "unreachable";
		};
		expect(note(10)).toBeNull();
		expect(note(40)).toMatch(/warned at 80%/);
		expect(note(50)).toMatch(/New backups fail/);
		expect(note(20, "full")).toMatch(/New backups fail/);
	});

	test("a deleted team has no usage warning", () => {
		const [row] = teamRows([
			slice({
				usedBytes: 60 * GIB,
				deletedAt: "2026-10-01T00:00:00Z",
				purgeAt: "2026-10-31T00:00:00Z",
			}),
		]);
		expect(row && usageNote(row)).toBeNull();
	});
});

describe("purgeIn", () => {
	const now = Date.parse("2026-10-30T12:00:00Z");

	test("counts whole days, rounding up", () => {
		expect(purgeIn("2026-11-01T00:00:00Z", now)).toBe("in 2 days");
		expect(purgeIn("2026-10-31T00:00:00Z", now)).toBe("in 1 day");
	});

	test("a date that has passed is today; none is empty", () => {
		expect(purgeIn("2026-10-29T00:00:00Z", now)).toBe("today");
		expect(purgeIn(null, now)).toBe("");
	});
});

describe("totals", () => {
	test("sums the live and deleted teams' backups", () => {
		const view = {
			services: [],
			defaultQuotaBytes: 50 * GIB,
			slices: [
				slice({ teamId: "a", usedBytes: 3 * GIB }),
				slice({ id: "b", teamId: "b", usedBytes: 2 * GIB }),
				slice({
					id: "c",
					teamId: "c",
					usedBytes: GIB,
					deletedAt: "2026-10-01T00:00:00Z",
					purgeAt: "2026-10-31T00:00:00Z",
				}),
			],
		};
		expect(totals(view)).toEqual({ teams: 2, deleted: 1, usedBytes: 6 * GIB });
	});
});

describe("servicesLoadFailure", () => {
	test("an answer from the control plane is shown with its status", () => {
		expect(
			servicesLoadFailure(new ApiError(404, "not_found", "Not Found")),
		).toBe("The control plane answered 404: Not Found. Nothing was changed.");
	});

	test("no answer at all says the control plane did not answer", () => {
		expect(servicesLoadFailure(new TypeError("Failed to fetch"))).toBe(
			"The control plane did not answer, so nothing was changed.",
		);
	});
});
