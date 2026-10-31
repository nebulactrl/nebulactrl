import { describe, expect, test } from "bun:test";
import type {
	HardeningFix,
	HardeningRemaining,
	HardeningRun,
} from "#/api/gen/types.gen";
import {
	actionHint,
	fixActions,
	fixStatus,
	groupRemaining,
	runSummary,
	safeFixesNote,
	safeFixIds,
	sinceFirst,
	weeklyAuditLocked,
	weeklyAuditNote,
} from "./security-model";

const fix = (
	id: string,
	state: HardeningFix["state"],
	def = true,
): HardeningFix => ({
	id,
	state,
	default: def,
	title: id,
	why: "",
	risk: "",
	lynisTests: [],
	manual: [],
	rebootRequired: false,
});

const finding = (o: Partial<HardeningRemaining>): HardeningRemaining => ({
	testId: "KRNL-6000",
	severity: "suggestion",
	title: "x",
	link: "",
	manual: [],
	...o,
});

const run = (o: Partial<HardeningRun>): HardeningRun => ({
	id: "r",
	action: "audit",
	trigger: "manual",
	status: "succeeded",
	createdAt: "2026-10-27T10:00:00Z",
	finishedAt: null,
	fixIds: [],
	hardeningIndex: null,
	indexBefore: null,
	outcomes: [],
	...o,
});

describe("security model", () => {
	test("movement since the first audit", () => {
		expect(sinceFirst({ hardeningIndex: 71, firstIndex: 62 })).toBe(
			"+9 since the first audit (62)",
		);
		expect(sinceFirst({ hardeningIndex: 60, firstIndex: 62 })).toBe(
			"−2 since the first audit (62)",
		);
		expect(sinceFirst({ hardeningIndex: 62, firstIndex: 62 })).toBeNull();
		expect(sinceFirst({ hardeningIndex: null, firstIndex: null })).toBeNull();
	});

	test("every fix state has words as well as an icon", () => {
		for (const s of [
			"applied",
			"compliant",
			"pending",
			"unavailable",
			"unknown",
		] as const) {
			expect(fixStatus({ state: s }).label.length).toBeGreaterThan(3);
		}
		expect(fixStatus({ state: "pending" }).kind).toBe("warning");
		expect(fixActions({ state: "applied" })).toEqual({
			canApply: false,
			canUndo: true,
		});
		expect(fixActions({ state: "pending" })).toEqual({
			canApply: true,
			canUndo: false,
		});
		expect(fixActions({ state: "compliant" })).toEqual({
			canApply: false,
			canUndo: false,
		});
		expect(fixActions({ state: "unavailable" })).toEqual({
			canApply: false,
			canUndo: false,
		});
	});

	test("the safe set is the default fixes that would change something", () => {
		const fixes = [
			fix("a", "pending"),
			fix("b", "applied"),
			fix("c", "compliant"),
			fix("d", "unavailable"),
			fix("e", "pending", false),
			fix("f", "unknown"),
		];
		expect(safeFixIds(fixes)).toEqual(["a", "f"]);
	});

	test("why the buttons are off", () => {
		const ok = { agentSupported: true, running: false };
		expect(actionHint(ok, "online", true)).toBeNull();
		expect(actionHint(ok, "online", false)).toContain("Owners and Admins");
		expect(
			actionHint({ ...ok, agentSupported: false }, "online", true),
		).toContain("too old");
		expect(actionHint(ok, "offline", true)).toContain("offline");
		expect(actionHint(ok, "revoked", true)).toContain("revoked");
		expect(actionHint({ ...ok, running: true }, "online", true)).toContain(
			"in progress",
		);
	});

	test("remaining findings are grouped by who acts", () => {
		const g = groupRemaining([
			finding({ title: "suggestion" }),
			finding({ title: "warning", severity: "warning" }),
			finding({ title: "fix", fixId: "sysctl-kernel", fixState: "pending" }),
			finding({
				title: "no fix here",
				fixId: "auditd",
				fixState: "unavailable",
			}),
			finding({ title: "never", never: "Docker needs it" }),
		]);
		expect(g.fixable.map((i) => i.title)).toEqual(["fix"]);
		expect(g.never.map((i) => i.title)).toEqual(["never"]);
		expect(g.manual.map((i) => i.title)).toEqual([
			"warning",
			"suggestion",
			"no fix here",
		]);
	});

	test("run summaries", () => {
		expect(runSummary(run({ status: "pending" }))).toBe("Running");
		expect(runSummary(run({ status: "failed" }))).toBe("Failed");
		expect(runSummary(run({ status: "skipped" }))).toBe(
			"Lynis is not installed",
		);
		expect(runSummary(run({ hardeningIndex: 71 }))).toBe("index 71");
		expect(
			runSummary(
				run({
					action: "apply",
					indexBefore: 62,
					hardeningIndex: 71,
					outcomes: [
						{ id: "a", outcome: "applied" },
						{ id: "b", outcome: "applied" },
						{ id: "c", outcome: "skipped" },
					],
				}),
			),
		).toBe("2 fixes applied · index 62 → 71");
		expect(
			runSummary(
				run({
					action: "rollback",
					outcomes: [{ id: "a", outcome: "rolled_back" }],
				}),
			),
		).toBe("1 fix undone");
	});
});

describe("weekly audit", () => {
	test("cannot be turned on without Lynis, but can be turned off", () => {
		expect(weeklyAuditLocked({ weekly: false, lynisInstalled: false })).toBe(
			true,
		);
		expect(weeklyAuditLocked({ weekly: true, lynisInstalled: false })).toBe(
			false,
		);
		expect(weeklyAuditLocked({ weekly: false, lynisInstalled: true })).toBe(
			false,
		);
	});

	test("the line under the switch says why, or leaves room for the date", () => {
		const view = { weekly: true, lynisInstalled: true, nextAuditAt: null };
		expect(weeklyAuditNote({ ...view, lynisInstalled: false })).toContain(
			"every weekly audit fails",
		);
		expect(
			weeklyAuditNote({ ...view, weekly: false, lynisInstalled: false }),
		).toContain("Needs Lynis");
		expect(weeklyAuditNote({ ...view, weekly: false })).toContain("Off.");
		expect(weeklyAuditNote(view)).toContain("No audit is scheduled");
		expect(
			weeklyAuditNote({ ...view, nextAuditAt: "2026-11-02T04:00:00Z" }),
		).toBeNull();
	});
});

describe("safe fixes note", () => {
	test("separates the batch from the per-row Apply", () => {
		const note = safeFixesNote([
			fix("a", "pending"),
			fix("b", "pending"),
			fix("c", "pending", false),
			fix("d", "applied"),
		]);
		expect(note).toContain("(2)");
		expect(note).toContain("only that fix");
		expect(note).toContain("One fix is marked On request");
	});

	test("with nothing left in the batch it says so", () => {
		expect(safeFixesNote([fix("a", "applied")])).toContain(
			"Every recommended fix is applied",
		);
	});
});
