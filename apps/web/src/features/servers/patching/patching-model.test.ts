import { describe, expect, test } from "bun:test";
import type {
	PatchRun,
	PatchView,
	ServerPatchSummary,
} from "#/api/gen/types.gen";
import {
	type AutoDraft,
	applySuggestion,
	autoBody,
	autoDraftOf,
	autoErrorField,
	autoIssues,
	autoKey,
	autoRunStatusText,
	busyText,
	enableAuto,
	failuresText,
	hasAutoIssues,
	nativeUpdatesText,
	needsAcknowledgement,
	needsAttention,
	nextWindowText,
	patchHint,
	productionList,
	rebootWarnings,
	runStatus,
	runSummary,
	startWarning,
	tabBadge,
	updatesStatus,
	updatesText,
	windowLengthLabel,
	windowLengths,
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
	auto: {
		builtinHolds: ["docker-ce", "containerd.io"],
		consecutiveFailures: 0,
		disabledReason: null,
		enabled: false,
		holdPackages: [],
		lastScheduledRun: null,
		nativeAutoUpdates: "",
		nextWindowStart: null,
		reboot: "never",
		requiresAcknowledgement: false,
		scope: "security",
		securityOnlySupported: true,
		suggestedStart: null,
		timezone: "UTC",
		windowMinutes: 120,
		windowStart: "03:00",
	},
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
		expect(updatesStatus(summary({ updates: 2 }))).toBe("pending");
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

describe("automatic updates draft", () => {
	const saved = view().auto;
	const draft = (over: Partial<AutoDraft> = {}): AutoDraft => ({
		...autoDraftOf(saved),
		...over,
	});

	test("the saved settings become the draft, security-only falls back to all", () => {
		expect(autoDraftOf(saved)).toEqual({
			enabled: false,
			scope: "security",
			windowStart: "03:00",
			windowMinutes: 120,
			timezone: "UTC",
			reboot: "never",
			holdPackages: [],
		});
		expect(autoDraftOf({ ...saved, securityOnlySupported: false }).scope).toBe(
			"all",
		);
	});

	test("the body trims names, drops blanks and repeats, and acknowledges only when told", () => {
		const body = autoBody(
			draft({
				enabled: true,
				timezone: " ",
				windowStart: " 04:30 ",
				holdPackages: [" openssl ", "", "curl", "openssl"],
			}),
		);
		expect(body).toEqual({
			enabled: true,
			scope: "security",
			windowStart: "04:30",
			windowMinutes: 120,
			timezone: "UTC",
			reboot: "never",
			holdPackages: ["openssl", "curl"],
		});
		expect(autoBody(draft(), true).acknowledgeProduction).toBe(true);
	});

	test("an empty row is not a change", () => {
		expect(autoKey(draft({ holdPackages: [""] }))).toBe(autoKey(draft()));
		expect(autoKey(draft({ windowMinutes: 180 }))).not.toBe(autoKey(draft()));
	});

	test("enabling a default-zone server takes the reader's zone once", () => {
		expect(enableAuto(draft(), saved, "Europe/Berlin")).toMatchObject({
			enabled: true,
			timezone: "Europe/Berlin",
		});
		// A zone the reader already chose stays.
		expect(
			enableAuto(draft({ timezone: "Asia/Tokyo" }), saved, "Europe/Berlin")
				.timezone,
		).toBe("Asia/Tokyo");
		// A saved zone stays.
		const berlin = { ...saved, timezone: "America/Chicago" };
		expect(
			enableAuto(autoDraftOf(berlin), berlin, "Europe/Berlin").timezone,
		).toBe("America/Chicago");
		// A zone the runtime does not know is not used.
		expect(enableAuto(draft(), saved, "Mars/Base").timezone).toBe("UTC");
	});

	test("the suggestion brings the zone it was computed in", () => {
		const withSuggestion = { suggestedStart: "03:12", timezone: "UTC" };
		expect(
			applySuggestion(draft({ timezone: "Europe/Berlin" }), withSuggestion),
		).toMatchObject({ windowStart: "03:12", timezone: "UTC" });
		const none = draft();
		expect(
			applySuggestion(none, { suggestedStart: null, timezone: "UTC" }),
		).toBe(none);
	});

	test("window lengths keep an unusual saved length", () => {
		expect(windowLengths(120)).toEqual([60, 120, 180, 240]);
		expect(windowLengths(90)).toEqual([60, 90, 120, 180, 240]);
		expect(windowLengthLabel(120)).toBe("2h");
		expect(windowLengthLabel(90)).toBe("90m");
	});

	test("issues name the field", () => {
		expect(hasAutoIssues(autoIssues(draft()))).toBe(false);
		expect(autoIssues(draft({ windowStart: "25:00" })).windowStart).toContain(
			"HH:MM",
		);
		expect(autoIssues(draft({ windowStart: "" })).windowStart).toBeDefined();
		expect(autoIssues(draft({ timezone: "Mars/Base" })).timezone).toBeDefined();
		const rows = autoIssues(draft({ holdPackages: ["ok", "bad name"] }));
		expect(rows.holdRows[1]).toContain("package name");
		expect(rows.holdRows[0]).toBeUndefined();
		const many = Array.from({ length: 21 }, (_, i) => `pkg${i}`);
		expect(autoIssues(draft({ holdPackages: many })).holdPackages).toContain(
			"20",
		);
	});

	test("a start while clocks change is warned about", () => {
		expect(startWarning("01:30")).toContain("Clocks change");
		expect(startWarning("02:59")).toContain("Clocks change");
		expect(startWarning("03:00")).toBeNull();
		expect(startWarning("00:59")).toBeNull();
		expect(startWarning("")).toBeNull();
	});

	test("422 codes map to their field", () => {
		expect(autoErrorField("invalid_timezone")).toBe("timezone");
		expect(autoErrorField("invalid_window")).toBe("windowStart");
		expect(autoErrorField("scope_unsupported")).toBe("scope");
		expect(autoErrorField("invalid_package")).toBe("holdPackages");
		expect(autoErrorField("agent_too_old")).toBeNull();
		expect(autoErrorField("production_ack_required")).toBeNull();
		expect(autoErrorField(undefined)).toBeNull();
	});
});

describe("automatic updates status", () => {
	const now = new Date("2026-10-27T20:00:00Z"); // 21:00 in Berlin (CET)

	test("the next window is named in the schedule's zone", () => {
		expect(nextWindowText("2026-10-28T02:12:00Z", "Europe/Berlin", now)).toBe(
			"Tonight 03:12 Europe/Berlin",
		);
		expect(nextWindowText("2026-10-28T13:00:00Z", "Europe/Berlin", now)).toBe(
			"Tomorrow 14:00 Europe/Berlin",
		);
		expect(nextWindowText("2026-10-27T22:30:00Z", "Europe/Berlin", now)).toBe(
			"Tonight 23:30 Europe/Berlin",
		);
		expect(
			nextWindowText(
				"2026-10-27T14:00:00Z",
				"UTC",
				new Date("2026-10-27T08:00:00Z"),
			),
		).toBe("Today 14:00 UTC");
		expect(nextWindowText("2026-10-30T02:00:00Z", "Europe/Berlin", now)).toBe(
			"Fri 30 Oct 03:00 Europe/Berlin",
		);
		expect(nextWindowText("nonsense", "UTC", now)).toBe("");
	});

	test("the day is the schedule zone's, not the reader's", () => {
		// 23:30 UTC on the 27th is already the 28th in Tokyo, 08:30 there.
		expect(
			nextWindowText(
				"2026-10-27T23:30:00Z",
				"Asia/Tokyo",
				new Date("2026-10-27T10:00:00Z"),
			),
		).toBe("Tomorrow 08:30 Asia/Tokyo");
	});

	test("run, failure and host updater lines", () => {
		expect(autoRunStatusText({ status: "succeeded" })).toBe("Succeeded");
		expect(autoRunStatusText({ status: "failed" })).toBe("Failed");
		expect(autoRunStatusText({ status: "pending" })).toBe("Running");
		expect(autoRunStatusText({ status: "rebooting" })).toBe("Rebooting");
		expect(failuresText(0)).toBeNull();
		expect(failuresText(2)).toBe(
			"2 failed runs in a row. Three in a row switch automatic updates off.",
		);
		expect(nativeUpdatesText("", true)).toBeNull();
		expect(nativeUpdatesText("unattended-upgrades", true)).toBe(
			"NebulaCtrl turns off unattended-upgrades on this server while automatic updates are on.",
		);
		expect(nativeUpdatesText("dnf-automatic", false)).toContain(
			"dnf-automatic is active on this server",
		);
	});
});
