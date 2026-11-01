import { describe, expect, test } from "bun:test";
import type {
	PatchRun,
	PatchView,
	ServerPatchSummary,
} from "#/api/gen/types.gen";
import {
	busyText,
	needsAcknowledgement,
	needsAttention,
	patchHint,
	productionList,
	rebootWarnings,
	runStatus,
	runSummary,
	tabBadge,
	updatesStatus,
	updatesText,
} from "./patching-model";

const summary = (
	over: Partial<ServerPatchSummary> = {},
): ServerPatchSummary => ({
	known: true,
	supported: true,
	updates: 0,
	security: 0,
	rebootRequired: false,
	checkedAt: null,
	busy: "",
	...over,
});

const view = (over: Partial<PatchView> = {}): PatchView => ({
	online: true,
	agentSupported: true,
	known: true,
	supported: true,
	manager: "apt",
	updates: 3,
	security: 1,
	packages: [],
	rebootRequired: false,
	rebootPackages: [],
	checkedAt: null,
	stale: false,
	busy: "",
	controlPlane: false,
	productionServices: [],
	runs: [],
	...over,
});

const run = (over: Partial<PatchRun> = {}): PatchRun => ({
	id: "r1",
	action: "check",
	trigger: "manual",
	status: "succeeded",
	securityOnly: false,
	updatesBefore: null,
	updatesAfter: null,
	exitCode: null,
	createdAt: "2026-10-28T10:00:00Z",
	rebootingSince: null,
	finishedAt: null,
	...over,
});

describe("updatesText", () => {
	test("says N updates (M security)", () => {
		expect(updatesText(summary({ updates: 7, security: 2 }))).toBe(
			"7 updates (2 security)",
		);
		expect(updatesText(summary({ updates: 1, security: 1 }))).toBe(
			"1 update (1 security)",
		);
	});
	test("up to date, unchecked and unsupported hosts", () => {
		expect(updatesText(summary())).toBe("Up to date");
		expect(updatesText(summary({ known: false }))).toBe(
			"Updates not checked yet",
		);
		expect(updatesText(summary({ supported: false }))).toBe(
			"Updates not managed here",
		);
	});
});

describe("status and attention", () => {
	test("security updates warn, plain updates are quiet, none is healthy", () => {
		expect(updatesStatus(summary({ updates: 2, security: 1 }))).toBe("warning");
		expect(updatesStatus(summary({ updates: 2 }))).toBe("building");
		expect(updatesStatus(summary())).toBe("healthy");
		expect(updatesStatus(summary({ known: false }))).toBe("unknown");
	});
	test("a host that needs nothing stays quiet", () => {
		expect(needsAttention(summary())).toBe(false);
		expect(needsAttention(summary({ updates: 1 }))).toBe(true);
		expect(needsAttention(summary({ rebootRequired: true }))).toBe(true);
		expect(needsAttention(summary({ known: false, updates: 4 }))).toBe(false);
	});
	test("the tab badge prefers the reboot", () => {
		expect(tabBadge(summary({ security: 2, rebootRequired: true }))).toBe(
			"Reboot",
		);
		expect(tabBadge(summary({ security: 2 }))).toBe("2 security");
		expect(tabBadge(summary({ updates: 5 }))).toBeNull();
		expect(
			tabBadge(summary({ known: false, rebootRequired: true })),
		).toBeNull();
	});
	test("busy words", () => {
		expect(busyText("reboot")).toBe("Rebooting");
		expect(busyText("")).toBeNull();
	});
});

describe("patchHint", () => {
	const all = { canCheck: true, instanceAdmin: true };
	test("buttons work on an online, current, admin-driven server", () => {
		for (const action of ["check", "apply", "reboot"] as const) {
			expect(patchHint(view(), "online", { ...all, action })).toBeNull();
		}
	});
	test("an Owner who is not the instance admin can check but not change the host", () => {
		const o = { canCheck: true, instanceAdmin: false };
		expect(patchHint(view(), "online", { ...o, action: "check" })).toBeNull();
		expect(patchHint(view(), "online", { ...o, action: "apply" })).toContain(
			"instance admin",
		);
		expect(patchHint(view(), "online", { ...o, action: "reboot" })).toContain(
			"instance admin",
		);
	});
	test("offline, revoked, old agent and runs in flight explain themselves", () => {
		expect(patchHint(view(), "offline", { ...all, action: "apply" })).toContain(
			"offline",
		);
		expect(patchHint(view(), "revoked", { ...all, action: "check" })).toContain(
			"revoked",
		);
		expect(
			patchHint(view({ agentSupported: false }), "online", {
				...all,
				action: "check",
			}),
		).toContain("too old");
		expect(
			patchHint(view({ busy: "reboot" }), "online", {
				...all,
				action: "apply",
			}),
		).toContain("Rebooting");
	});
	test("an upgrade needs a check and a package manager first", () => {
		expect(
			patchHint(view({ known: false }), "online", { ...all, action: "apply" }),
		).toContain("Check for updates first");
		expect(
			patchHint(view({ supported: false }), "online", {
				...all,
				action: "apply",
			}),
		).toContain("no supported package manager");
	});
});

describe("runs", () => {
	test("status words", () => {
		expect(runStatus(run({ status: "rebooting" })).label).toContain(
			"come back",
		);
		expect(runStatus(run({ status: "failed" })).kind).toBe("failed");
		expect(runStatus(run({ status: "succeeded" })).kind).toBe("healthy");
	});
	test("summaries", () => {
		expect(
			runSummary(run({ action: "apply", updatesBefore: 5, updatesAfter: 0 })),
		).toBe("5 updates before, 0 left");
		expect(runSummary(run({ action: "check", updatesAfter: 0 }))).toBe(
			"Up to date",
		);
		expect(runSummary(run({ action: "check", updatesAfter: 2 }))).toBe(
			"2 updates pending",
		);
		expect(runSummary(run({ status: "failed", error: "dpkg error" }))).toBe(
			"dpkg error",
		);
		expect(
			runSummary(
				run({
					action: "reboot",
					status: "rebooting",
					message: "Reboot scheduled (systemd).",
				}),
			),
		).toBe("Reboot scheduled (systemd).");
	});
});

describe("production warnings", () => {
	const prod = [
		{ name: "api", project: "shop" },
		{ name: "web", project: "shop" },
		{ name: "db", project: "shop" },
	];
	test("acknowledgement is needed for production and for the control plane", () => {
		expect(needsAcknowledgement(view())).toBe(false);
		expect(
			needsAcknowledgement(
				view({
					productionServices: [
						{
							serviceId: "s",
							name: "api",
							environmentId: "e",
							environment: "production",
							projectId: "p",
							project: "shop",
						},
					],
				}),
			),
		).toBe(true);
		expect(needsAcknowledgement(view({ controlPlane: true }))).toBe(true);
	});
	test("the list is cut with a count", () => {
		expect(productionList(prod)).toBe("shop/api, shop/web, shop/db");
		expect(productionList(prod, 2)).toBe("shop/api, shop/web and 1 more");
	});
	test("a reboot says services go down and names production", () => {
		const w = rebootWarnings(
			view({
				controlPlane: true,
				productionServices: [
					{
						serviceId: "s",
						name: "api",
						environmentId: "e",
						environment: "production",
						projectId: "p",
						project: "shop",
					},
				],
			}),
			3,
		);
		expect(w[0]).toContain("3 services");
		expect(w.join(" ")).toContain("Production is affected: shop/api");
		expect(w.join(" ")).toContain("NebulaCtrl itself");
		expect(w.join(" ")).toContain("20 minutes");
	});
});
